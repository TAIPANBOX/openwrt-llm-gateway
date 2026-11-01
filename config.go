package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Backend is a model server in the house that speaks the OpenAI protocol under URL (".../v1").
type Backend struct {
	Name    string
	URL     *url.URL
	KeyFile string // an upstream key, read once at start; empty when the server has none
	Models  []string
	key     string
}

// Client is a device or an agent. Its key is never stored, only its SHA-256.
type Client struct {
	Name       string
	KeyHash    string
	Models     []string // model names, or "*" for every model the backends serve
	Origins    []string // web origins allowed to call with this client's key; empty by default
	Rate       int      // requests per minute; 0 is no limit
	Concurrent int      // requests in flight; 0 is no limit
}

func (c *Client) allows(model string) bool {
	return slices.Contains(c.Models, "*") || slices.Contains(c.Models, model)
}

func (c *Client) listsOrigin(origin string) bool {
	return slices.Contains(c.Origins, origin)
}

type Config struct {
	Enabled      bool
	Zones        []string
	Port         int
	LoopbackPort int
	Name         string // the host label dnsmasq answers, "llm" for llm.<domain>
	JournalPath  string
	CertDir      string
	Backends     []*Backend
	Clients      []*Client
}

func (c *Config) backendFor(model string) *Backend {
	for _, b := range c.Backends {
		for _, m := range b.Models {
			if m == model {
				return b
			}
		}
	}
	return nil
}

// Resolver is the part of net.Resolver the gateway uses; tests substitute a table.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

var (
	keyHashRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	nameRE    = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
)

// LoadConfig reads the UCI file and the upstream key files the backends name.
func LoadConfig(path string, res Resolver) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, err := ParseConfig(f, res)
	if err != nil {
		return nil, err
	}
	for _, b := range cfg.Backends {
		if b.KeyFile == "" {
			continue
		}
		raw, err := os.ReadFile(b.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("backend %s: key_file: %w", b.Name, err)
		}
		b.key = strings.TrimSpace(string(raw))
	}
	return cfg, nil
}

// ParseConfig reads and validates a configuration. Every backend's host must be, or resolve
// only to, an address in the house (invariant 7); the dialer checks again on each connection.
func ParseConfig(r io.Reader, res Resolver) (*Config, error) {
	cfg := &Config{Port: 8760, LoopbackPort: 8761, Name: "llm",
		JournalPath: "/tmp/llm-gateway/journal.log", CertDir: "/etc/llm-gateway"}
	seenModel := map[string]string{}
	seenName := map[string]bool{}
	seenHash := map[string]string{}
	for _, s := range parseUCI(bufio.NewScanner(r)) {
		switch s.Type {
		case "gateway":
			cfg.Enabled = s.Options["enabled"] == "1"
			cfg.Zones = s.Lists["zone"]
			for opt, dst := range map[string]*int{"port": &cfg.Port, "loopback_port": &cfg.LoopbackPort} {
				if v, ok := s.Options[opt]; ok {
					n, err := strconv.Atoi(v)
					if err != nil || n < 1 || n > 65535 {
						return nil, fmt.Errorf("gateway: %s %q is not a port", opt, v)
					}
					*dst = n
				}
			}
			if v, ok := s.Options["name"]; ok {
				cfg.Name = v
			}
			if v, ok := s.Options["journal"]; ok {
				cfg.JournalPath = v
			}
			if v, ok := s.Options["cert_dir"]; ok {
				cfg.CertDir = v
			}
		case "backend":
			b, err := parseBackend(s, res)
			if err != nil {
				return nil, err
			}
			if seenName["backend "+b.Name] {
				return nil, fmt.Errorf("backend %s: named twice", b.Name)
			}
			seenName["backend "+b.Name] = true
			for _, m := range b.Models {
				if other, ok := seenModel[m]; ok {
					return nil, fmt.Errorf("backend %s: model %q is already served by backend %s", b.Name, m, other)
				}
				seenModel[m] = b.Name
			}
			cfg.Backends = append(cfg.Backends, b)
		case "client":
			c, err := parseClient(s)
			if err != nil {
				return nil, err
			}
			if seenName["client "+c.Name] {
				return nil, fmt.Errorf("client %s: named twice", c.Name)
			}
			seenName["client "+c.Name] = true
			if other, ok := seenHash[c.KeyHash]; ok {
				return nil, fmt.Errorf("client %s: has the same key as client %s", c.Name, other)
			}
			seenHash[c.KeyHash] = c.Name
			cfg.Clients = append(cfg.Clients, c)
		}
	}
	if cfg.Port == cfg.LoopbackPort {
		return nil, fmt.Errorf("gateway: port and loopback_port are both %d", cfg.Port)
	}
	if !nameRE.MatchString(strings.ReplaceAll(cfg.Name, "-", "_")) {
		return nil, fmt.Errorf("gateway: name %q is not a host label", cfg.Name)
	}
	return cfg, nil
}

func parseBackend(s uciSection, res Resolver) (*Backend, error) {
	if !nameRE.MatchString(s.Name) {
		return nil, fmt.Errorf("backend %q: a backend needs a section name of letters, digits and _", s.Name)
	}
	b := &Backend{Name: s.Name, KeyFile: s.Options["key_file"], Models: s.Lists["model"]}
	u, err := url.Parse(s.Options["url"])
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("backend %s: url %q is not an http(s) URL", s.Name, s.Options["url"])
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/v1") {
		return nil, fmt.Errorf("backend %s: url %q must end in /v1, the base of the OpenAI routes", s.Name, s.Options["url"])
	}
	b.URL = u
	if len(b.Models) == 0 {
		return nil, fmt.Errorf("backend %s: names no model", s.Name)
	}
	for _, m := range b.Models {
		if m == "" || m == "*" || strings.ContainsAny(m, " \t\"") {
			return nil, fmt.Errorf("backend %s: model %q is not a model name", s.Name, m)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := checkInHouse(ctx, res, u.Hostname()); err != nil {
		return nil, fmt.Errorf("backend %s: %w", s.Name, err)
	}
	return b, nil
}

func parseClient(s uciSection) (*Client, error) {
	if !nameRE.MatchString(s.Name) {
		return nil, fmt.Errorf("client %q: a client needs a section name of letters, digits and _", s.Name)
	}
	c := &Client{Name: s.Name, KeyHash: s.Options["key_hash"], Models: s.Lists["model"], Origins: s.Lists["origin"]}
	if !keyHashRE.MatchString(c.KeyHash) {
		return nil, fmt.Errorf("client %s: key_hash is not sha256:<64 hex>", s.Name)
	}
	if len(c.Models) == 0 {
		return nil, fmt.Errorf("client %s: is given no model", s.Name)
	}
	for _, o := range c.Origins {
		if err := checkOrigin(o); err != nil {
			return nil, fmt.Errorf("client %s: origin %q: %w", s.Name, o, err)
		}
	}
	for opt, dst := range map[string]*int{"rate": &c.Rate, "concurrent": &c.Concurrent} {
		if v, ok := s.Options[opt]; ok {
			n, err := strconv.Atoi(strings.TrimSuffix(v, "/min"))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("client %s: %s %q is not a count", s.Name, opt, v)
			}
			*dst = n
		}
	}
	return c, nil
}

// checkOrigin accepts exactly what a browser sends in Origin: scheme://host[:port].
func checkOrigin(o string) error {
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(o, "*") {
		return fmt.Errorf("not scheme://host[:port]")
	}
	if o != strings.ToLower(o) {
		return fmt.Errorf("browsers send origins in lower case")
	}
	return nil
}

// inHouse is the set of addresses a backend may have: private, unique-local, loopback and
// link-local. Shared address space (100.64/10) is a carrier or a tailnet, so it is outside.
func inHouse(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && !a.IsUnspecified() && (a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast())
}

func checkInHouse(ctx context.Context, res Resolver, host string) error {
	ips, err := res.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("host %s does not resolve: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %s resolves to nothing", host)
	}
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip.IP)
		if !ok || !inHouse(a) {
			return fmt.Errorf("host %s resolves to %s, which is outside the house", host, ip.IP)
		}
	}
	return nil
}
