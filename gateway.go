package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strings"
	"time"
)

type Options struct {
	// Hosts are the names and addresses a request's Host may carry (invariant 3, DNS rebinding):
	// llm.<domain>, the listen addresses, 127.0.0.1.
	Hosts    []string
	Journal  Journal
	Resolver Resolver
	// Dial connects to an address the in-house check has already passed. Tests substitute it.
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	Now     func() time.Time
	MaxBody int64
}

type Gateway struct {
	cfg       *Config
	opts      Options
	hosts     map[string]bool
	transport *http.Transport
	limits    *limiter
}

func NewGateway(cfg *Config, opts Options) (*Gateway, error) {
	if cfg == nil {
		return nil, errors.New("no configuration")
	}
	if opts.Journal == nil {
		opts.Journal = discardJournal{}
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.Dial == nil {
		opts.Dial = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxBody == 0 {
		opts.MaxBody = 16 << 20
	}
	g := &Gateway{cfg: cfg, opts: opts, hosts: map[string]bool{}, limits: newLimiter()}
	for _, h := range opts.Hosts {
		g.hosts[normalHost(h)] = true
	}
	g.transport = &http.Transport{
		Proxy:                 nil, // never an environment proxy: a backend is reached directly, in the house
		DialContext:           g.dialInHouse,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return g, nil
}

// dialInHouse resolves the backend's host on every connection and refuses to connect when any
// answer is outside the house (invariant 7), so a name that has moved since the configuration
// was loaded is caught here, before a byte leaves.
func (g *Gateway) dialInHouse(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := g.opts.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s resolves to nothing", host)
	}
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip.IP)
		if !ok || !inHouse(a) {
			return nil, fmt.Errorf("refused: %s resolves to %s, outside the house", host, ip.IP)
		}
	}
	var last error
	for _, ip := range ips {
		c, err := g.opts.Dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

var routes = map[string]string{
	"/v1/models":           http.MethodGet,
	"/v1/chat/completions": http.MethodPost,
	"/v1/completions":      http.MethodPost,
	"/v1/embeddings":       http.MethodPost,
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := g.opts.Now()
	e := Entry{Time: start, Source: remoteIP(r.RemoteAddr)}
	status := g.serve(w, r, &e)
	e.Status = status
	e.Millis = g.opts.Now().Sub(start).Milliseconds()
	if r.URL.Path != "/health" {
		g.opts.Journal.Record(e)
	}
}

// serve answers one request and returns the status it sent. The order of the checks is the
// design: host, route, preflight, key, origin, model, limits, and only then a backend.
func (g *Gateway) serve(w http.ResponseWriter, r *http.Request, e *Entry) int {
	if !g.hosts[normalHost(r.Host)] {
		e.Reason = "foreign host"
		return refuse(w, http.StatusMisdirectedRequest, "this gateway does not serve that host")
	}
	if r.URL.Path == "/health" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok")
		return http.StatusOK
	}
	method, known := routes[r.URL.Path]
	if !known {
		e.Reason = "no such route"
		return refuse(w, http.StatusNotFound, "not found")
	}
	e.Route = r.URL.Path
	origin := r.Header.Get("Origin")
	if r.Method == http.MethodOptions {
		return g.preflight(w, origin, e)
	}
	if r.Method != method {
		e.Reason = "method"
		w.Header().Set("Allow", method)
		return refuse(w, http.StatusMethodNotAllowed, "method not allowed")
	}
	key := bearer(r.Header.Get("Authorization"))
	c := clientForKey(g.cfg.Clients, key)
	if c == nil {
		e.Reason = "bad key"
		if key == "" {
			e.Reason = "no key"
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="llm-gateway"`)
		return refuse(w, http.StatusUnauthorized, "a valid key is required")
	}
	e.Client = c.Name
	if origin != "" {
		if !c.listsOrigin(origin) {
			e.Reason = "origin"
			return refuse(w, http.StatusForbidden, "this client does not allow requests from that web page")
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
	if r.URL.Path == "/v1/models" {
		return g.models(w, c)
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.opts.MaxBody))
	if err != nil {
		e.Reason = "body"
		return refuse(w, http.StatusRequestEntityTooLarge, "request body too large or unreadable")
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" {
		e.Reason = "no model"
		return refuse(w, http.StatusBadRequest, "the body must be JSON naming a model")
	}
	e.Model, e.Stream = req.Model, req.Stream
	b := g.cfg.backendFor(req.Model)
	if b == nil || !c.allows(req.Model) {
		// One answer for "not yours" and "nobody's", so a client cannot map what else exists.
		e.Reason = "model"
		return refuse(w, http.StatusForbidden, "this client is not given that model")
	}
	e.Backend = b.Name
	release, why := g.limits.take(c, g.opts.Now())
	if why != "" {
		e.Reason = why
		return refuse(w, http.StatusTooManyRequests, "too many requests for this client")
	}
	defer release()
	return g.forward(w, r, b, body, e)
}

func (g *Gateway) preflight(w http.ResponseWriter, origin string, e *Entry) int {
	for _, c := range g.cfg.Clients {
		if origin != "" && c.listsOrigin(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "GET, POST")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
			w.WriteHeader(http.StatusNoContent)
			return http.StatusNoContent
		}
	}
	e.Reason = "origin"
	return refuse(w, http.StatusForbidden, "no client allows requests from that web page")
}

func (g *Gateway) models(w http.ResponseWriter, c *Client) int {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	list := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list", Data: []model{}}
	for _, b := range g.cfg.Backends {
		for _, m := range b.Models {
			if c.allows(m) {
				list.Data = append(list.Data, model{ID: m, Object: "model", OwnedBy: b.Name})
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
	return http.StatusOK
}

func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, b *Backend, body []byte, e *Entry) int {
	status := http.StatusBadGateway
	var sniff *usageSniffer
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme, out.URL.Host = b.URL.Scheme, b.URL.Host
			out.URL.Path = b.URL.Path + strings.TrimPrefix(r.URL.Path, "/v1")
			out.URL.RawPath, out.URL.RawQuery = "", ""
			out.Host = b.URL.Host
			for _, h := range []string{"Authorization", "Origin", "Referer", "Cookie", "Forwarded",
				"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"} {
				out.Header.Del(h)
			}
			if b.key != "" {
				out.Header.Set("Authorization", "Bearer "+b.key)
			}
			out.Body = io.NopCloser(bytes.NewReader(body))
			out.ContentLength = int64(len(body))
			out.GetBody = nil
		},
		Transport:     g.transport,
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			// The backend's own CORS headers never reach the client: Ollama, for one, can send
			// a wildcard. Only the origin this gateway allowed above is named (invariant 3).
			for k := range resp.Header {
				if strings.HasPrefix(strings.ToLower(k), "access-control-") {
					resp.Header.Del(k)
				}
			}
			status = resp.StatusCode
			stream := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
			sniff = newUsageSniffer(resp.Body, stream)
			resp.Body = sniff
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			status = http.StatusBadGateway
			e.Reason = "backend unreachable"
			if strings.Contains(err.Error(), "outside the house") {
				e.Reason = "backend outside the house"
			}
			refuse(w, http.StatusBadGateway, "the model server did not answer")
		},
	}
	rp.ServeHTTP(w, r)
	if sniff != nil {
		sniff.finish()
		e.In, e.Out = sniff.got.Prompt, sniff.got.Completion
	}
	return status
}

func refuse(w http.ResponseWriter, code int, msg string) int {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":{"message":%q,"type":"llm_gateway"}}`+"\n", msg)
	return code
}

// normalHost lowercases a Host value and drops its port and a trailing dot.
func normalHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	return h
}

func remoteIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
