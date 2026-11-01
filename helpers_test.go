package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// recordingBackend is a stand-in OpenAI server that counts every request it receives, so a test
// can assert that a refused request never reached it.
type recordingBackend struct {
	srv   *httptest.Server
	hits  atomic.Int64
	mu    sync.Mutex
	last  *http.Request
	body  []byte
	reply func(w http.ResponseWriter, r *http.Request, body []byte)
}

func newBackend(t *testing.T) *recordingBackend {
	t.Helper()
	b := &recordingBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.last, b.body = r, body
		reply := b.reply
		b.mu.Unlock()
		if reply != nil {
			reply(w, r, body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *recordingBackend) url(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse(b.srv.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

const testKey = "llmgw_test-key-for-the-laptop-client-0123456789abcd"

// testGateway builds a gateway with one backend serving qwen3:14b and llama3.3:70b and one
// client "laptop" that holds testKey and is given qwen3:14b only.
type testGateway struct {
	gw      *Gateway
	backend *recordingBackend
	client  *Client
	journal *bytes.Buffer
}

func newTestGateway(t *testing.T) *testGateway {
	t.Helper()
	b := newBackend(t)
	c := &Client{Name: "laptop", KeyHash: HashKey(testKey), Models: []string{"qwen3:14b"}}
	cfg := &Config{
		Backends: []*Backend{{Name: "desktop", URL: b.url(t), Models: []string{"qwen3:14b", "llama3.3:70b"}}},
		Clients:  []*Client{c},
	}
	var buf bytes.Buffer
	gw, err := NewGateway(cfg, Options{
		Hosts:    []string{"llm.lan", "192.168.8.1", "127.0.0.1"},
		Journal:  NewWriterJournal(&syncWriter{w: &buf}),
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testGateway{gw: gw, backend: b, client: c, journal: &buf}
}

// do sends one request straight to the handler, with Host llm.lan unless the caller set one.
func (tg *testGateway) do(r *http.Request) *httptest.ResponseRecorder {
	if r.Host == "" || r.Host == "example.com" {
		r.Host = "llm.lan"
	}
	w := httptest.NewRecorder()
	tg.gw.ServeHTTP(w, r)
	return w
}

func chatRequest(model, key string) *http.Request {
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hello"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	return r
}

// journalEntries decodes the JSON lines the journal wrote.
func journalEntries(t *testing.T, buf *bytes.Buffer) []Entry {
	t.Helper()
	var out []Entry
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for dec.More() {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			t.Fatalf("journal line does not decode: %v\n%s", err, buf.String())
		}
		out = append(out, e)
	}
	return out
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// staticResolver answers from a fixed table and falls back to parsing literal addresses.
type staticResolver map[string][]string

func (s staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IPAddr{{IP: ip}}, nil
	}
	addrs, ok := s[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	var out []net.IPAddr
	for _, a := range addrs {
		out = append(out, net.IPAddr{IP: net.ParseIP(a)})
	}
	return out, nil
}
