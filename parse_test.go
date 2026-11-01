package main

import (
	"bufio"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUCIParsingHonoursQuotesAndComments(t *testing.T) {
	src := `# a comment
config gateway 'main'
	option enabled '1'   # trailing comment
	list zone 'lan'
	list zone "guest"
	option name llm

config backend 'b1'
	option url 'http://10.0.0.5:11434/v1'
	list model 'qwen3:14b'
	list model 'model with #hash'
`
	secs := parseUCI(bufio.NewScanner(strings.NewReader(src)))
	if len(secs) != 2 || secs[0].Type != "gateway" || secs[0].Name != "main" {
		t.Fatalf("sections %+v", secs)
	}
	g := secs[0]
	if g.Options["enabled"] != "1" || g.Options["name"] != "llm" || strings.Join(g.Lists["zone"], ",") != "lan,guest" {
		t.Fatalf("gateway %+v", g)
	}
	if m := secs[1].Lists["model"]; len(m) != 2 || m[1] != "model with #hash" {
		t.Fatalf("models %q", m)
	}
}

func TestConfigRefusesWhatWouldBeAmbiguousOrUnsafe(t *testing.T) {
	h := HashKey(testKey)
	base := "config gateway 'main'\n\tlist zone 'lan'\n"
	back := func(name, model string) string {
		return "config backend '" + name + "'\n\toption url 'http://10.0.0.5:11434/v1'\n\tlist model '" + model + "'\n"
	}
	cl := func(name, hash, extra string) string {
		return "config client '" + name + "'\n\toption key_hash '" + hash + "'\n\tlist model '*'\n" + extra
	}
	for name, conf := range map[string]string{
		"a model on two backends": base + back("b1", "m") + back("b2", "m"),
		"two backends one name":   base + back("b1", "m") + back("b1", "n"),
		"two clients one key":     base + cl("c1", h, "") + cl("c2", h, ""),
		"two clients one name":    base + cl("c1", h, "") + cl("c1", HashKey("llmgw_other"), ""),
		"a key stored in clear":   base + cl("c1", testKey, ""),
		"a wildcard origin":       base + cl("c1", h, "\tlist origin '*'\n"),
		"an origin with a path":   base + cl("c1", h, "\tlist origin 'http://chat.lan/app'\n"),
		"an origin in capitals":   base + cl("c1", h, "\tlist origin 'http://Chat.lan'\n"),
		"a client with no model":  base + "config client 'c1'\n\toption key_hash '" + h + "'\n",
		"a backend with no model": base + "config backend 'b1'\n\toption url 'http://10.0.0.5:11434/v1'\n",
		"a backend model of *":    base + back("b1", "*"),
		"one port for both":       "config gateway 'main'\n\toption port '8760'\n\toption loopback_port '8760'\n",
		"a port out of range":     "config gateway 'main'\n\toption port '70000'\n",
		"a negative rate":         base + cl("c1", h, "\toption rate '-1'\n"),
		"a user in the url":       base + "config backend 'b1'\n\toption url 'http://u:p@10.0.0.5:11434/v1'\n\tlist model 'm'\n",
		"a query in the url":      base + "config backend 'b1'\n\toption url 'http://10.0.0.5:11434/v1?x=1'\n\tlist model 'm'\n",
		"an unnamed client":       base + "config client\n\toption key_hash '" + h + "'\n\tlist model '*'\n",
	} {
		if _, err := ParseConfig(strings.NewReader(conf), staticResolver{}); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	good := base + back("b1", "m") + cl("c1", h, "\tlist origin 'http://chat.lan:3000'\n\toption rate '30/min'\n\toption concurrent '2'\n")
	cfg, err := ParseConfig(strings.NewReader(good), staticResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Clients[0]; c.Rate != 30 || c.Concurrent != 2 || cfg.Port != 8760 || cfg.LoopbackPort != 8761 || cfg.Name != "llm" {
		t.Fatalf("parsed %+v %+v", cfg, c)
	}
}

func TestLoadConfigReadsTheUpstreamKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "up.key")
	os.WriteFile(keyFile, []byte("upstream-secret\n"), 0o600)
	cfgFile := filepath.Join(dir, "llm-gateway")
	os.WriteFile(cfgFile, []byte("config backend 'b1'\n\toption url 'http://10.0.0.5:8080/v1'\n\toption key_file '"+keyFile+"'\n\tlist model 'm'\n"), 0o644)
	cfg, err := LoadConfig(cfgFile, staticResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backends[0].key != "upstream-secret" {
		t.Fatalf("upstream key %q", cfg.Backends[0].key)
	}
	os.Remove(keyFile)
	if _, err := LoadConfig(cfgFile, staticResolver{}); err == nil || !strings.Contains(err.Error(), "b1") {
		t.Fatalf("a missing key file: %v", err)
	}
}

func TestUpstreamKeyReplacesTheClientsKey(t *testing.T) {
	tg := newTestGateway(t)
	tg.gw.cfg.Backends[0].key = "upstream-secret"
	if w := tg.do(chatRequest("qwen3:14b", testKey)); w.Code != 200 {
		t.Fatalf("got %d", w.Code)
	}
	tg.backend.mu.Lock()
	got := tg.backend.last.Header.Get("Authorization")
	tg.backend.mu.Unlock()
	if got != "Bearer upstream-secret" {
		t.Fatalf("the backend saw Authorization %q, want the upstream key and never the client's", got)
	}
	tg.gw.cfg.Backends[0].key = ""
	tg.do(chatRequest("qwen3:14b", testKey))
	tg.backend.mu.Lock()
	got = tg.backend.last.Header.Get("Authorization")
	tg.backend.mu.Unlock()
	if got != "" {
		t.Fatalf("with no upstream key the backend saw Authorization %q", got)
	}
}

const ubusStatus = `{
	"up": true,
	"ipv4-address": [ { "address": "192.168.8.1", "mask": 24 } ],
	"ipv6-address": [ ],
	"ipv6-prefix-assignment": [ { "address": "fd12:3456:789a::", "mask": 60,
		"local-address": { "address": "fd12:3456:789a::1", "mask": 60 } },
		{ "address": "2001:db8:1::", "mask": 64, "local-address": { "address": "2001:db8:1::1", "mask": 64 } } ]
}`

func TestOpenWrtZonesReadTheFirewallAndNetifd(t *testing.T) {
	dir := t.TempDir()
	fw := filepath.Join(dir, "firewall")
	os.WriteFile(fw, []byte(`config zone
	option name 'lan'
	list network 'lan'
	list network 'iot'
config zone
	option name 'old'
	option network 'guest guest6'
config zone
	option name 'wan'
	list network 'wan'
`), 0o644)
	calls := []string{}
	o := openwrtZones{firewall: fw, ubus: func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[1] == "network.interface.lan" {
			return []byte(ubusStatus), nil
		}
		return nil, errors.New("not found")
	}}
	nets, err := o.ZoneNetworks("lan")
	if err != nil || strings.Join(nets, ",") != "lan,iot" {
		t.Fatalf("lan networks %v %v", nets, err)
	}
	if nets, _ := o.ZoneNetworks("old"); strings.Join(nets, ",") != "guest,guest6" {
		t.Fatalf("old-style networks %v", nets)
	}
	if _, err := o.ZoneNetworks("nope"); err == nil {
		t.Fatal("an unknown zone was accepted")
	}
	addrs, err := o.NetworkAddresses("lan")
	if err != nil || calls[0] != "call network.interface.lan status" {
		t.Fatalf("addresses %v %v %v", addrs, err, calls)
	}
	got, err := ListenAddrs([]string{"lan"}, fakeZones{zones: map[string][]string{"lan": {"lan"}}, addrs: map[string][]netip.Addr{"lan": addrs}}, 8760, 8761)
	if err != nil {
		t.Fatal(err)
	}
	// The delegated global prefix on the LAN is not listened on; the ULA is.
	if strings.Join(got, " ") != "192.168.8.1:8760 [fd12:3456:789a::1]:8760 127.0.0.1:8761" {
		t.Fatalf("listen %v", got)
	}
	if _, err := o.NetworkAddresses("iot"); err == nil {
		t.Fatal("a ubus failure was swallowed")
	}
	if _, err := ListenAddrs([]string{"wan"}, o, 8760, 8761); err == nil {
		t.Fatal("the wan zone was accepted")
	}
}

func TestFileJournalRotatesAndStaysPrivate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "j", "journal.log")
	j, err := NewFileJournal(p, 300)
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		j.Record(Entry{Source: "192.168.8.30", Client: "laptop", Status: 200})
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 || st.Size() > 300 {
		t.Fatalf("journal mode %v size %d", st.Mode().Perm(), st.Size())
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("no rotated journal")
	}
	dst, _ := os.Stat(filepath.Dir(p))
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("journal directory mode %v", dst.Mode().Perm())
	}
}
