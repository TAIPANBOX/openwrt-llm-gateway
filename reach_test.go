package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeZones struct {
	zones map[string][]string     // zone -> networks
	addrs map[string][]netip.Addr // network -> addresses
}

func (f fakeZones) ZoneNetworks(zone string) ([]string, error) {
	n, ok := f.zones[zone]
	if !ok {
		return nil, errors.New("no zone " + zone)
	}
	return n, nil
}

func (f fakeZones) NetworkAddresses(network string) ([]netip.Addr, error) {
	return f.addrs[network], nil
}

func TestListensOnlyOnNamedZoneAddressesAndLoopback(t *testing.T) {
	src := fakeZones{
		zones: map[string][]string{"lan": {"lan"}, "guest": {"guest"}, "wan": {"wan", "wan6"}},
		addrs: map[string][]netip.Addr{
			"lan":   {netip.MustParseAddr("192.168.8.1"), netip.MustParseAddr("fd00:8::1")},
			"guest": {netip.MustParseAddr("192.168.9.1")},
			"wan":   {netip.MustParseAddr("203.0.113.20")},
		},
	}
	got, err := ListenAddrs([]string{"lan"}, src, 8760, 8761)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.8.1:8760", "[fd00:8::1]:8760", "127.0.0.1:8761"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("listen on %v, want %v", got, want)
	}
	for _, a := range got {
		host, _, _ := net.SplitHostPort(a)
		ip := net.ParseIP(host)
		if host == "" || ip == nil || ip.IsUnspecified() {
			t.Fatalf("listen address %q is a wildcard", a)
		}
		if strings.HasPrefix(a, "192.168.9.") || strings.HasPrefix(a, "203.0.113.") {
			t.Fatalf("listens on %q, which belongs to a zone it does not name", a)
		}
	}
	// A wildcard that a broken network status reports is refused, not bound.
	// It stops the start outright, naming the cause, rather than falling back to loopback: a
	// netifd that reports a wildcard is broken, and the owner should hear of it.
	for _, w := range []string{"0.0.0.0", "::"} {
		bad := fakeZones{zones: map[string][]string{"lan": {"lan"}}, addrs: map[string][]netip.Addr{
			"lan": {netip.MustParseAddr("192.168.8.1"), netip.MustParseAddr(w)}}}
		got, err := ListenAddrs([]string{"lan"}, bad, 8760, 8761)
		if err == nil || got != nil || !strings.Contains(err.Error(), "wildcard") {
			t.Fatalf("a zone address of %s: got %v, %v; want nothing to listen on and an error naming the wildcard", w, got, err)
		}
	}
	// Naming the wan zone is refused outright: the gateway is for the house.
	if _, err := ListenAddrs([]string{"lan", "wan"}, src, 8760, 8761); err == nil {
		t.Fatal("the wan zone was accepted")
	}
	// A named zone with no address yet listens on loopback only, and says so.
	empty := fakeZones{zones: map[string][]string{"lan": {"lan"}}, addrs: map[string][]netip.Addr{}}
	got, err = ListenAddrs([]string{"lan"}, empty, 8760, 8761)
	if err == nil || strings.Join(got, " ") != "127.0.0.1:8761" {
		t.Fatalf("zone without address: got %v, %v; want loopback only and an error naming it", got, err)
	}
}

func TestPublicBackendAddressIsRefusedAtLoad(t *testing.T) {
	res := staticResolver{
		"api.example.com": {"93.184.216.34"},
		"box.lan":         {"10.0.60.10"},
		"split.lan":       {"10.0.60.11", "93.184.216.35"},
	}
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://api.example.com/v1", false},
		{"http://8.8.8.8:11434/v1", false},
		{"http://100.64.0.5:11434/v1", false}, // shared address space: a tailnet or a carrier, not the house
		{"http://split.lan:8080/v1", false},   // one public answer is enough to refuse
		{"http://box.lan:11434/v1", true},
		{"http://10.0.60.10:11434/v1", true},
		{"http://192.168.8.20:8080/v1", true},
		{"http://172.20.0.2:8080/v1", true},
		{"http://169.254.10.1:8080/v1", true},
		{"http://127.0.0.1:8080/v1", true},
		{"http://[fd00:60::10]:11434/v1", true},
		{"http://[2001:db8::1]:11434/v1", false},
		{"ftp://10.0.60.10/v1", false},
		{"http://10.0.60.10:11434", false}, // the URL must name the /v1 base
	}
	for _, c := range cases {
		conf := "config gateway 'main'\n\tlist zone 'lan'\n" +
			"config backend 'b1'\n\toption url '" + c.url + "'\n\tlist model 'm1'\n" +
			"config client 'c1'\n\toption key_hash '" + HashKey(testKey) + "'\n\tlist model '*'\n"
		_, err := ParseConfig(strings.NewReader(conf), res)
		if c.ok && err != nil {
			t.Fatalf("%s: refused: %v", c.url, err)
		}
		if !c.ok {
			if err == nil {
				t.Fatalf("%s: accepted", c.url)
			}
			if !strings.Contains(err.Error(), "b1") {
				t.Fatalf("%s: the error does not name the backend: %v", c.url, err)
			}
		}
	}
}

func TestBackendNameResolvingOutsideIsRefusedAtDial(t *testing.T) {
	var dials atomic.Int64
	u, _ := url.Parse("http://box.lan:8080/v1")
	cfg := &Config{
		Backends: []*Backend{{Name: "box", URL: u, Models: []string{"m1"}}},
		Clients:  []*Client{{Name: "laptop", KeyHash: HashKey(testKey), Models: []string{"*"}}},
	}
	gw, err := NewGateway(cfg, Options{
		Hosts:    []string{"llm.lan"},
		Resolver: staticResolver{"box.lan": {"203.0.113.7"}}, // it said 10.0.60.10 at load; now it does not
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("the test dialer must not be reached")
		},
		Journal: NewWriterJournal(&syncWriter{w: &strings.Builder{}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	tg := &testGateway{gw: gw}
	w := tg.do(chatRequest("m1", testKey))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("backend resolving to a public address: got %d, want 502", w.Code)
	}
	if n := dials.Load(); n != 0 {
		t.Fatalf("the dialer was asked to connect %d time(s) to a public address", n)
	}
	// Resolving back into the house, the same request is dialled.
	gw.opts.Resolver = staticResolver{"box.lan": {"10.0.60.10"}}
	tg.do(chatRequest("m1", testKey))
	if n := dials.Load(); n != 1 {
		t.Fatalf("a private answer was dialled %d time(s), want 1", n)
	}
}
