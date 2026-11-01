package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestWithoutKeyIsRefusedBeforeAnyBackend(t *testing.T) {
	tg := newTestGateway(t)
	w := tg.do(chatRequest("qwen3:14b", ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no key: got %d, want 401", w.Code)
	}
	if n := tg.backend.hits.Load(); n != 0 {
		t.Fatalf("a request with no key reached the backend %d time(s)", n)
	}
}

func TestWrongKeyIsRefused(t *testing.T) {
	tg := newTestGateway(t)
	for _, key := range []string{"llmgw_wrong", testKey + "x", testKey[:len(testKey)-1], strings.ToUpper(testKey)} {
		w := tg.do(chatRequest("qwen3:14b", key))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("key %q: got %d, want 401", key, w.Code)
		}
	}
	// A key sent in a header the gateway does not read is no key at all.
	r := chatRequest("qwen3:14b", "")
	r.Header.Set("X-Api-Key", testKey)
	if w := tg.do(r); w.Code != http.StatusUnauthorized {
		t.Fatalf("key in X-Api-Key: got %d, want 401", w.Code)
	}
	if n := tg.backend.hits.Load(); n != 0 {
		t.Fatalf("a request with a wrong key reached the backend %d time(s)", n)
	}
}

func TestLoopbackRequestStillNeedsAKey(t *testing.T) {
	tg := newTestGateway(t)
	r := chatRequest("qwen3:14b", "")
	r.RemoteAddr = "127.0.0.1:40000"
	r.Host = "127.0.0.1:8761"
	if w := tg.do(r); w.Code != http.StatusUnauthorized {
		t.Fatalf("loopback with no key: got %d, want 401", w.Code)
	}
	if n := tg.backend.hits.Load(); n != 0 {
		t.Fatalf("a loopback request with no key reached the backend %d time(s)", n)
	}
}

func TestClientReachesOnlyItsModels(t *testing.T) {
	tg := newTestGateway(t)
	w := tg.do(chatRequest("llama3.3:70b", testKey))
	if w.Code != http.StatusForbidden {
		t.Fatalf("model not given to the client: got %d, want 403", w.Code)
	}
	// A model nobody serves is refused the same way, so the answer does not reveal what exists.
	if w := tg.do(chatRequest("nobody:1b", testKey)); w.Code != http.StatusForbidden {
		t.Fatalf("model nobody serves: got %d, want 403", w.Code)
	}
	if n := tg.backend.hits.Load(); n != 0 {
		t.Fatalf("a request for a model the client is not given reached the backend %d time(s)", n)
	}
	// The model it is given goes through, so the refusals above are about the model.
	if w := tg.do(chatRequest("qwen3:14b", testKey)); w.Code != http.StatusOK {
		t.Fatalf("the client's own model: got %d, want 200: %s", w.Code, w.Body.String())
	}
	if n := tg.backend.hits.Load(); n != 1 {
		t.Fatalf("the client's own model reached the backend %d time(s), want 1", n)
	}
}

func TestModelListShowsOnlyTheClientsModels(t *testing.T) {
	tg := newTestGateway(t)
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := tg.do(r)
	if w.Code != http.StatusOK {
		t.Fatalf("/v1/models: got %d, want 200", w.Code)
	}
	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("/v1/models is not JSON: %v: %s", err, w.Body.String())
	}
	if list.Object != "list" || len(list.Data) != 1 || list.Data[0].ID != "qwen3:14b" {
		t.Fatalf("/v1/models = %s, want only qwen3:14b", w.Body.String())
	}
	// A client given "*" sees every model the backends serve.
	tg.client.Models = []string{"*"}
	w = tg.do(r.Clone(r.Context()))
	if !strings.Contains(w.Body.String(), "llama3.3:70b") || !strings.Contains(w.Body.String(), "qwen3:14b") {
		t.Fatalf("client given * sees %s, want both models", w.Body.String())
	}
	// Without a key the list is not shown at all.
	r2 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if w := tg.do(r2); w.Code != http.StatusUnauthorized {
		t.Fatalf("/v1/models with no key: got %d, want 401", w.Code)
	}
}

func TestHealthAnswersOkAndNothingElse(t *testing.T) {
	tg := newTestGateway(t)
	w := tg.do(httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("/health: got %d %q, want 200 \"ok\"", w.Code, w.Body.String())
	}
	if n := tg.backend.hits.Load(); n != 0 {
		t.Fatalf("/health reached the backend %d time(s)", n)
	}
	// Any other path, with or without a key, is not found and reaches nothing.
	for _, p := range []string{"/", "/v1", "/v1/files", "/api/tags", "/../v1/models"} {
		r := httptest.NewRequest(http.MethodGet, "/health", nil)
		r.URL.Path = p
		r.Header.Set("Authorization", "Bearer "+testKey)
		if w := tg.do(r); w.Code != http.StatusNotFound {
			t.Fatalf("%s with a key: got %d, want 404", p, w.Code)
		}
	}
	if n := tg.backend.hits.Load(); n != 0 {
		t.Fatalf("an unknown path reached the backend %d time(s)", n)
	}
}

func TestNoResponseCarriesAWildcardOrigin(t *testing.T) {
	tg := newTestGateway(t)
	// The backend itself answers with a wildcard, as Ollama can.
	tg.backend.reply = func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}
	tg.client.Origins = []string{"http://chat.lan:3000"}
	withOrigin := func(r *http.Request, o string) *http.Request { r.Header.Set("Origin", o); return r }
	preflight := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	reqs := map[string]*http.Request{
		"answered":             chatRequest("qwen3:14b", testKey),
		"answered with origin": withOrigin(chatRequest("qwen3:14b", testKey), "http://chat.lan:3000"),
		"no key":               chatRequest("qwen3:14b", ""),
		"wrong key":            chatRequest("qwen3:14b", "llmgw_wrong"),
		"refused origin":       withOrigin(chatRequest("qwen3:14b", testKey), "https://example.com"),
		"refused model":        chatRequest("llama3.3:70b", testKey),
		"unknown path":         httptest.NewRequest(http.MethodGet, "/nope", nil),
		"health":               httptest.NewRequest(http.MethodGet, "/health", nil),
		"preflight listed":     withOrigin(preflight.Clone(preflight.Context()), "http://chat.lan:3000"),
		"preflight unlisted":   withOrigin(preflight.Clone(preflight.Context()), "https://example.com"),
	}
	for name, r := range reqs {
		w := tg.do(r)
		for k, v := range w.Header() {
			if !strings.HasPrefix(strings.ToLower(k), "access-control-") {
				continue
			}
			for _, s := range v {
				if strings.Contains(s, "*") {
					t.Fatalf("%s: header %s: %q carries a wildcard", name, k, s)
				}
			}
			if strings.EqualFold(k, "Access-Control-Allow-Credentials") {
				t.Fatalf("%s: the backend's Access-Control-Allow-Credentials reached the client", name)
			}
		}
	}
	// With the backend down the gateway still answers without a wildcard.
	tg.backend.srv.Close()
	w := tg.do(chatRequest("qwen3:14b", testKey))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("backend down: got %d, want 502", w.Code)
	}
	if v := w.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("backend down: Access-Control-Allow-Origin %q", v)
	}
}

func TestUnlistedOriginIsRefused(t *testing.T) {
	tg := newTestGateway(t)
	r := chatRequest("qwen3:14b", testKey)
	r.Header.Set("Origin", "https://example.com")
	w := tg.do(r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unlisted origin: got %d, want 403", w.Code)
	}
	if v := w.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("unlisted origin was named back: %q", v)
	}
	// "null" is what a sandboxed frame or a file:// page sends; it is never allowed.
	r = chatRequest("qwen3:14b", testKey)
	r.Header.Set("Origin", "null")
	if w := tg.do(r); w.Code != http.StatusForbidden {
		t.Fatalf("Origin null: got %d, want 403", w.Code)
	}
	// A preflight from an origin no client lists is refused too.
	p := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	p.Header.Set("Origin", "https://example.com")
	p.Header.Set("Access-Control-Request-Method", "POST")
	if w := tg.do(p); w.Code != http.StatusForbidden {
		t.Fatalf("preflight from an unlisted origin: got %d, want 403", w.Code)
	}
	if n := tg.backend.hits.Load(); n != 0 {
		t.Fatalf("a request from an unlisted origin reached the backend %d time(s)", n)
	}
}

func TestListedOriginIsNamedBack(t *testing.T) {
	tg := newTestGateway(t)
	const origin = "http://chat.lan:3000"
	tg.client.Origins = []string{origin}
	r := chatRequest("qwen3:14b", testKey)
	r.Header.Set("Origin", origin)
	w := tg.do(r)
	if w.Code != http.StatusOK {
		t.Fatalf("listed origin: got %d, want 200: %s", w.Code, w.Body.String())
	}
	if v := w.Header().Get("Access-Control-Allow-Origin"); v != origin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", v, origin)
	}
	if !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Origin") {
		t.Fatalf("Vary does not name Origin: %q", w.Header().Values("Vary"))
	}
	// The browser's preflight carries no key; it is answered for an origin some client lists.
	p := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	p.Header.Set("Origin", origin)
	p.Header.Set("Access-Control-Request-Method", "POST")
	p.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	w = tg.do(p)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight: got %d, want 204", w.Code)
	}
	if v := w.Header().Get("Access-Control-Allow-Origin"); v != origin {
		t.Fatalf("preflight Access-Control-Allow-Origin = %q, want %q", v, origin)
	}
	// A listed origin does not lend itself to a client that does not list it.
	other := &Client{Name: "phone", KeyHash: HashKey("llmgw_phone-key-0123456789abcdefghijklmnop"), Models: []string{"*"}}
	tg.gw.cfg.Clients = append(tg.gw.cfg.Clients, other)
	r = chatRequest("qwen3:14b", "llmgw_phone-key-0123456789abcdefghijklmnop")
	r.Header.Set("Origin", origin)
	if w := tg.do(r); w.Code != http.StatusForbidden {
		t.Fatalf("origin listed by another client: got %d, want 403", w.Code)
	}
}

func TestForeignHostIsRefused(t *testing.T) {
	tg := newTestGateway(t)
	for _, host := range []string{"attacker.example", "attacker.example:8760", "llm.lan.attacker.example", "10.9.9.9"} {
		r := chatRequest("qwen3:14b", testKey)
		r.Host = host
		w := tg.do(r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Fatalf("Host %q: got %d, want 421", host, w.Code)
		}
	}
	if n := tg.backend.hits.Load(); n != 0 {
		t.Fatalf("a request for a foreign host reached the backend %d time(s)", n)
	}
	// The gateway's own names, with or without a port, are served.
	for _, host := range []string{"llm.lan", "LLM.LAN:8760", "192.168.8.1:8760", "127.0.0.1:8761"} {
		r := chatRequest("qwen3:14b", testKey)
		r.Host = host
		if w := tg.do(r); w.Code != http.StatusOK {
			t.Fatalf("Host %q: got %d, want 200", host, w.Code)
		}
	}
}
