package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeUCI struct {
	mu    sync.Mutex
	calls []string // the stdin of each call
}

func (f *fakeUCI) run(stdin string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " ")+"\n"+stdin)
	return nil, nil
}

func testEnv(t *testing.T, conf string) (*env, *fakeUCI) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "llm-gateway")
	if err := os.WriteFile(cfgPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	dhcp := filepath.Join(dir, "dhcp")
	os.WriteFile(dhcp, []byte("config dnsmasq\n\toption domain 'lan'\n"), 0o644)
	u := &fakeUCI{}
	return &env{configPath: cfgPath, dhcpPath: dhcp, uci: u.run, resolver: staticResolver{},
		zones: fakeZones{zones: map[string][]string{"lan": {"lan"}},
			addrs: map[string][]netip.Addr{"lan": {netip.MustParseAddr("192.168.8.1")}}},
		now: time.Now, listen: net.Listen}, u
}

var keyRE = regexp.MustCompile(`llmgw_[A-Za-z0-9_-]{43}`)

func TestClientAddShowsTheKeyOnceAndStoresOnlyItsHash(t *testing.T) {
	e, u := testEnv(t, "config gateway 'main'\n\tlist zone 'lan'\n")
	var out, errOut bytes.Buffer
	code := run([]string{"client", "add", "laptop", "-model", "qwen3:14b", "-origin", "http://chat.lan:3000", "-rate", "30"}, &out, &errOut, e)
	if code != 0 {
		t.Fatalf("client add exited %d: %s", code, errOut.String())
	}
	key := keyRE.FindString(out.String())
	if key == "" {
		t.Fatalf("no key in the output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "https://llm.lan:8760/v1") || !strings.Contains(out.String(), "http://127.0.0.1:8761/v1") {
		t.Fatalf("the output does not give the base URLs:\n%s", out.String())
	}
	if len(u.calls) != 1 {
		t.Fatalf("uci called %d times, want 1", len(u.calls))
	}
	batch := u.calls[0]
	if strings.Contains(batch, key) || strings.Contains(batch, key[6:30]) {
		t.Fatalf("the key itself reached the configuration:\n%s", batch)
	}
	for _, want := range []string{"set llm-gateway.laptop='client'", "key_hash='" + HashKey(key) + "'",
		"add_list llm-gateway.laptop.model='qwen3:14b'", "add_list llm-gateway.laptop.origin='http://chat.lan:3000'",
		"set llm-gateway.laptop.rate='30'", "commit llm-gateway"} {
		if !strings.Contains(batch, want) {
			t.Fatalf("the uci batch lacks %q:\n%s", want, batch)
		}
	}
}

func TestClientAddRefusesWhatItCannotWriteSafely(t *testing.T) {
	conf := "config gateway 'main'\n\tlist zone 'lan'\n" +
		"config client 'laptop'\n\toption key_hash '" + HashKey(testKey) + "'\n\tlist model '*'\n"
	for name, args := range map[string][]string{
		"existing name":   {"client", "add", "laptop", "-model", "m"},
		"quote in model":  {"client", "add", "phone", "-model", "m'; delete llm-gateway"},
		"bad origin":      {"client", "add", "phone", "-model", "m", "-origin", "*"},
		"no model":        {"client", "add", "phone"},
		"bad name":        {"client", "add", "ph.one", "-model", "m"},
		"name of section": {"client", "add", "main", "-model", "m"},
	} {
		e, u := testEnv(t, conf)
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut, e); code == 0 {
			t.Fatalf("%s: accepted:\n%s", name, out.String())
		}
		if len(u.calls) != 0 {
			t.Fatalf("%s: uci was called: %v", name, u.calls)
		}
		if keyRE.MatchString(out.String()) {
			t.Fatalf("%s: a key was printed for a client that was not added", name)
		}
	}
}

func TestServeBindsOnlyTheZoneAddressesAndLoopback(t *testing.T) {
	certDir := t.TempDir()
	conf := "config gateway 'main'\n\toption enabled '1'\n\tlist zone 'lan'\n" +
		"\toption cert_dir '" + certDir + "'\n\toption journal '" + filepath.Join(certDir, "j.log") + "'\n" +
		"config backend 'desktop'\n\toption url 'http://127.0.0.1:9/v1'\n\tlist model 'm1'\n" +
		"config client 'laptop'\n\toption key_hash '" + HashKey(testKey) + "'\n\tlist model '*'\n"
	e, _ := testEnv(t, conf)
	var mu sync.Mutex
	asked := []string{}
	real := map[string]string{}
	e.listen = func(network, addr string) (net.Listener, error) {
		ln, err := net.Listen(network, "127.0.0.1:0") // stand in for the router's address
		if err == nil {
			mu.Lock()
			asked = append(asked, addr)
			real[addr] = ln.Addr().String()
			mu.Unlock()
		}
		return ln, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- serve(ctx, e, io.Discard) }()
	var clients []*http.Client
	defer func() {
		for _, c := range clients {
			c.CloseIdleConnections()
		}
		http.DefaultClient.CloseIdleConnections()
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("serve did not stop within 4 s of its context ending")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(asked)
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	got := strings.Join(asked, " ")
	mu.Unlock()
	if got != "192.168.8.1:8760 127.0.0.1:8761" {
		t.Fatalf("serve asked to listen on %q, want the LAN address and loopback only", got)
	}
	// The LAN listener speaks TLS with a certificate under the house CA.
	caPEM, err := os.ReadFile(filepath.Join(certDir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	tc := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "llm.lan"},
		DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, n, real["192.168.8.1:8760"])
		},
	}}
	clients = append(clients, tc)
	req, _ := http.NewRequest(http.MethodGet, "https://llm.lan:8760/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := tc.Do(req)
	if err != nil {
		t.Fatalf("HTTPS to the LAN listener: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "m1") {
		t.Fatalf("HTTPS /v1/models: %d %s", resp.StatusCode, body)
	}
	// Plain HTTP on the LAN port is not served.
	pc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, n, real["192.168.8.1:8760"])
	}}, Timeout: 2 * time.Second}
	clients = append(clients, pc)
	if resp, err := pc.Get("http://llm.lan:8760/health"); err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) == "ok" {
			t.Fatal("the LAN port answered plain HTTP")
		}
	}
	// The loopback listener is plain HTTP and still wants a key.
	lb := "http://" + real["127.0.0.1:8761"]
	resp, err = http.Get(lb + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = http.Get(lb + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("loopback /v1/models with no key: %d, want 401", resp.StatusCode)
	}
}
