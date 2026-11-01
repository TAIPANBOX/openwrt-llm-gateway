package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var version = "dev"

// env is everything the commands touch outside the process, so tests can stand in for it.
type env struct {
	configPath string
	dhcpPath   string
	uci        func(stdin string, args ...string) ([]byte, error)
	resolver   Resolver
	zones      ZoneSource
	now        func() time.Time
	listen     func(network, addr string) (net.Listener, error)
	// firewallPath and reloadFirewall serve models-zone.
	firewallPath   string
	reloadFirewall func() error
}

func defaultEnv() *env {
	return &env{
		configPath: "/etc/config/llm-gateway",
		dhcpPath:   "/etc/config/dhcp",
		uci: func(stdin string, args ...string) ([]byte, error) {
			cmd := exec.Command("uci", args...)
			cmd.Stdin = strings.NewReader(stdin)
			return cmd.CombinedOutput()
		},
		resolver:     net.DefaultResolver,
		zones:        newOpenWrtZones(),
		now:          time.Now,
		listen:       net.Listen,
		firewallPath: "/etc/config/firewall",
		reloadFirewall: func() error {
			return exec.Command("/etc/init.d/firewall", "reload").Run()
		},
	}
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, defaultEnv())) }

const usageText = `usage:
  llm-gateway serve
  llm-gateway client add NAME -model M [-model M ...] [-origin URL ...] [-rate N] [-concurrent N]
  llm-gateway client remove NAME
  llm-gateway client list
  llm-gateway models-zone NETWORK [-internet]
  llm-gateway addresses
  llm-gateway cert
  llm-gateway check
  llm-gateway version
`

func run(args []string, stdout, stderr io.Writer, e *env) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return serve(ctx, e, stderr)
	case "client":
		if len(args) < 2 {
			break
		}
		switch args[1] {
		case "add":
			return clientAdd(args[2:], stdout, stderr, e)
		case "remove":
			return clientRemove(args[2:], stdout, stderr, e)
		case "list":
			return clientList(stdout, stderr, e)
		}
	case "addresses":
		// Where serve would listen, one per line; the init script names the first for dnsmasq.
		secs, err := readSections(e.configPath)
		if err != nil {
			fmt.Fprintln(stderr, "llm-gateway:", err)
			return 1
		}
		cfg := gatewaySettings(secs)
		addrs, lerr := ListenAddrs(cfg.Zones, e.zones, cfg.Port, cfg.LoopbackPort)
		for _, a := range addrs {
			fmt.Fprintln(stdout, a)
		}
		if lerr != nil {
			fmt.Fprintln(stderr, "llm-gateway:", lerr)
			return 1
		}
		return 0
	case "models-zone":
		return modelsZone(args[1:], stdout, stderr, e)
	case "cert":
		return certCmd(stdout, stderr, e)
	case "check":
		cfg, err := LoadConfig(e.configPath, e.resolver)
		if err != nil {
			fmt.Fprintln(stderr, "llm-gateway:", err)
			return 1
		}
		fmt.Fprintf(stdout, "ok: %d backend(s), %d client(s), zones %s\n", len(cfg.Backends), len(cfg.Clients), strings.Join(cfg.Zones, " "))
		return 0
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	}
	fmt.Fprint(stderr, usageText)
	return 2
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func clientAdd(args []string, stdout, stderr io.Writer, e *env) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	name := args[0]
	fs := flag.NewFlagSet("client add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var models, origins multi
	fs.Var(&models, "model", "a model the client may use, or * for all; repeatable")
	fs.Var(&origins, "origin", "a web origin allowed to call with this key; repeatable")
	rate := fs.Int("rate", 0, "requests per minute, 0 for no limit")
	conc := fs.Int("concurrent", 0, "requests in flight, 0 for no limit")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	fail := func(f string, a ...any) int { fmt.Fprintf(stderr, "llm-gateway: "+f+"\n", a...); return 1 }
	if !nameRE.MatchString(name) {
		return fail("a client name is letters, digits and _, up to 32")
	}
	if len(models) == 0 {
		return fail("name at least one -model, or -model '*' for every model")
	}
	for _, m := range models {
		if m == "" || strings.ContainsAny(m, "'\"\n\t ") {
			return fail("model %q cannot be written safely", m)
		}
	}
	for _, o := range origins {
		if err := checkOrigin(o); err != nil {
			return fail("origin %q: %v", o, err)
		}
	}
	if *rate < 0 || *conc < 0 {
		return fail("-rate and -concurrent are counts")
	}
	secs, err := readSections(e.configPath)
	if err != nil {
		return fail("%v", err)
	}
	for _, s := range secs {
		if s.Name == name {
			return fail("a section named %s already exists", name)
		}
	}
	key, err := NewKey()
	if err != nil {
		return fail("%v", err)
	}
	var b strings.Builder
	p := "llm-gateway." + name
	fmt.Fprintf(&b, "set %s='client'\nset %s.key_hash='%s'\n", p, p, HashKey(key))
	for _, m := range models {
		fmt.Fprintf(&b, "add_list %s.model='%s'\n", p, m)
	}
	for _, o := range origins {
		fmt.Fprintf(&b, "add_list %s.origin='%s'\n", p, o)
	}
	if *rate > 0 {
		fmt.Fprintf(&b, "set %s.rate='%d'\n", p, *rate)
	}
	if *conc > 0 {
		fmt.Fprintf(&b, "set %s.concurrent='%d'\n", p, *conc)
	}
	b.WriteString("commit llm-gateway\n")
	if out, err := e.uci(b.String(), "batch"); err != nil {
		return fail("uci: %v: %s", err, out)
	}
	cfg := gatewaySettings(secs)
	host := cfg.Name + "." + readDomain(e.dhcpPath)
	fmt.Fprintf(stdout, "Client %s added. Its key is shown this once; nothing on the router can show it again.\n\n", name)
	fmt.Fprintf(stdout, "  key            %s\n", key)
	fmt.Fprintf(stdout, "  base URL       https://%s:%d/v1\n", host, cfg.Port)
	fmt.Fprintf(stdout, "  on the router  http://127.0.0.1:%d/v1\n", cfg.LoopbackPort)
	if fp := caFingerprint(cfg.CertDir); fp != "" {
		fmt.Fprintf(stdout, "  CA to trust    %s\n                 (llm-gateway cert prints it)\n", fp)
	}
	fmt.Fprintf(stdout, "\nLoad it with: service llm-gateway reload\n")
	return 0
}

// modelsZone puts NETWORK, where the model boxes live, into a firewall zone of its own that the
// router reaches and nothing else does (invariant 6): no forwarding in or out, input refused but
// for DHCP and DNS. A LAN device can then reach a model only through the gateway, with a key.
// Internet for the zone, for model downloads, is a flag the owner sets on purpose.
func modelsZone(args []string, stdout, stderr io.Writer, e *env) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	network := args[0]
	fs := flag.NewFlagSet("models-zone", flag.ContinueOnError)
	fs.SetOutput(stderr)
	internet := fs.Bool("internet", false, "let the model boxes reach the internet through wan")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	fail := func(f string, a ...any) int { fmt.Fprintf(stderr, "llm-gateway: "+f+"\n", a...); return 1 }
	if !nameRE.MatchString(network) || network == "lan" || network == "wan" || network == "wan6" {
		return fail("network %q cannot be the models network", network)
	}
	secs, err := readSections(e.firewallPath)
	if err != nil {
		return fail("%v", err)
	}
	for _, s := range secs {
		if s.Type != "zone" || s.Name == "llm_models" {
			continue
		}
		nets := append(append([]string(nil), s.Lists["network"]...), strings.Fields(s.Options["network"])...)
		for _, n := range nets {
			if n == network {
				return fail("network %s is already in the zone %s; take it out first", network, s.Options["name"])
			}
		}
	}
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	for _, sec := range []string{"llm_models", "llm_models_dhcp", "llm_models_dns", "llm_models_wan"} {
		w("delete firewall.%s", sec)
	}
	w("set firewall.llm_models='zone'")
	w("set firewall.llm_models.name='models'")
	w("set firewall.llm_models.input='REJECT'")
	w("set firewall.llm_models.output='ACCEPT'")
	w("set firewall.llm_models.forward='REJECT'")
	w("add_list firewall.llm_models.network='%s'", network)
	w("set firewall.llm_models_dhcp='rule'")
	w("set firewall.llm_models_dhcp.name='Allow-models-DHCP'")
	w("set firewall.llm_models_dhcp.src='models'")
	w("set firewall.llm_models_dhcp.proto='udp'")
	w("set firewall.llm_models_dhcp.dest_port='67'")
	w("set firewall.llm_models_dhcp.family='ipv4'")
	w("set firewall.llm_models_dhcp.target='ACCEPT'")
	w("set firewall.llm_models_dns='rule'")
	w("set firewall.llm_models_dns.name='Allow-models-DNS'")
	w("set firewall.llm_models_dns.src='models'")
	w("set firewall.llm_models_dns.proto='tcp udp'")
	w("set firewall.llm_models_dns.dest_port='53'")
	w("set firewall.llm_models_dns.target='ACCEPT'")
	if *internet {
		w("set firewall.llm_models_wan='forwarding'")
		w("set firewall.llm_models_wan.src='models'")
		w("set firewall.llm_models_wan.dest='wan'")
	}
	w("commit firewall")
	if out, err := e.uci(b.String(), "batch"); err != nil {
		return fail("uci: %v: %s", err, out)
	}
	if err := e.reloadFirewall(); err != nil {
		return fail("firewall reload: %v", err)
	}
	fmt.Fprintf(stdout, "Network %s is now the zone models: only the router reaches it.", network)
	if *internet {
		fmt.Fprint(stdout, " It may reach the internet through wan.")
	}
	fmt.Fprintln(stdout)
	return 0
}

func clientRemove(args []string, stdout, stderr io.Writer, e *env) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	secs, err := readSections(e.configPath)
	if err != nil {
		fmt.Fprintln(stderr, "llm-gateway:", err)
		return 1
	}
	for _, s := range secs {
		if s.Type == "client" && s.Name == args[0] {
			if out, err := e.uci("delete llm-gateway."+s.Name+"\ncommit llm-gateway\n", "batch"); err != nil {
				fmt.Fprintf(stderr, "llm-gateway: uci: %v: %s\n", err, out)
				return 1
			}
			fmt.Fprintf(stdout, "Client %s removed; its key no longer works after: service llm-gateway reload\n", s.Name)
			return 0
		}
	}
	fmt.Fprintf(stderr, "llm-gateway: no client named %s\n", args[0])
	return 1
}

func clientList(stdout, stderr io.Writer, e *env) int {
	secs, err := readSections(e.configPath)
	if err != nil {
		fmt.Fprintln(stderr, "llm-gateway:", err)
		return 1
	}
	for _, s := range secs {
		if s.Type != "client" {
			continue
		}
		fmt.Fprintf(stdout, "%s\tmodels %s", s.Name, strings.Join(s.Lists["model"], ","))
		if o := s.Lists["origin"]; len(o) > 0 {
			fmt.Fprintf(stdout, "\torigins %s", strings.Join(o, ","))
		}
		for _, opt := range []string{"rate", "concurrent"} {
			if v := s.Options[opt]; v != "" {
				fmt.Fprintf(stdout, "\t%s %s", opt, v)
			}
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

func certCmd(stdout, stderr io.Writer, e *env) int {
	secs, err := readSections(e.configPath)
	if err != nil {
		fmt.Fprintln(stderr, "llm-gateway:", err)
		return 1
	}
	dir := gatewaySettings(secs).CertDir
	raw, err := os.ReadFile(filepath.Join(dir, caCertFile))
	if err != nil {
		fmt.Fprintln(stderr, "llm-gateway: no CA yet; it is made when the gateway first starts")
		return 1
	}
	fmt.Fprintf(stdout, "SHA-256 fingerprint: %s\n\n%s", caFingerprint(dir), raw)
	return 0
}

func caFingerprint(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, caCertFile))
	if err != nil {
		return ""
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return ""
	}
	return fingerprint(blk.Bytes)
}

func readSections(path string) ([]uciSection, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseUCI(bufio.NewScanner(f)), nil
}

// gatewaySettings reads the gateway section without validating backends, for commands that
// must work while a backend is down or misconfigured.
func gatewaySettings(secs []uciSection) *Config {
	var b strings.Builder
	for _, s := range secs {
		if s.Type != "gateway" {
			continue
		}
		fmt.Fprintf(&b, "config gateway '%s'\n", s.Name)
		for k, v := range s.Options {
			fmt.Fprintf(&b, "\toption %s '%s'\n", k, v)
		}
		for k, vs := range s.Lists {
			for _, v := range vs {
				fmt.Fprintf(&b, "\tlist %s '%s'\n", k, v)
			}
		}
	}
	cfg, err := ParseConfig(strings.NewReader(b.String()), nil)
	if err != nil {
		cfg, _ = ParseConfig(strings.NewReader(""), nil)
	}
	return cfg
}

func readDomain(dhcpPath string) string {
	secs, err := readSections(dhcpPath)
	if err == nil {
		for _, s := range secs {
			if s.Type == "dnsmasq" && s.Options["domain"] != "" {
				return s.Options["domain"]
			}
		}
	}
	return "lan"
}

// serve runs until ctx ends. It binds only what ListenAddrs returns: TLS on the zone addresses,
// plain HTTP on loopback.
func serve(ctx context.Context, e *env, logw io.Writer) int {
	lg := log.New(logw, "llm-gateway: ", 0)
	cfg, err := LoadConfig(e.configPath, e.resolver)
	if err != nil {
		lg.Print(err)
		return 1
	}
	if !cfg.Enabled {
		lg.Print("disabled; set llm-gateway.main.enabled=1")
		return 0
	}
	addrs, lerr := ListenAddrs(cfg.Zones, e.zones, cfg.Port, cfg.LoopbackPort)
	if addrs == nil {
		lg.Print(lerr)
		return 1
	}
	if lerr != nil {
		lg.Print(lerr)
	}
	name := cfg.Name + "." + readDomain(e.dhcpPath)
	hosts := []string{name, "localhost"}
	var ips []netip.Addr
	for _, a := range addrs {
		ap := netip.MustParseAddrPort(a)
		ips = append(ips, ap.Addr())
		hosts = append(hosts, ap.Addr().String())
	}
	c, err := ensureCerts(cfg.CertDir, []string{name}, ips, e.now())
	if err != nil {
		lg.Printf("certificates: %v", err)
		return 1
	}
	j, err := NewFileJournal(cfg.JournalPath, 1<<20)
	if err != nil {
		lg.Printf("journal: %v", err)
		return 1
	}
	gw, err := NewGateway(cfg, Options{Hosts: hosts, Journal: j, Resolver: e.resolver, Now: e.now})
	if err != nil {
		lg.Print(err)
		return 1
	}
	srv := &http.Server{
		Handler:           gw,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          log.New(logw, "llm-gateway: http: ", 0),
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(addrs))
	for _, a := range addrs {
		ln, err := e.listen("tcp", a)
		if err != nil {
			lg.Printf("listen %s: %v", a, err)
			srv.Close()
			wg.Wait()
			return 1
		}
		if !strings.HasSuffix(a, ":"+strconv.Itoa(cfg.LoopbackPort)) {
			ln = tls.NewListener(ln, serverTLSConfig(c))
		}
		lg.Printf("listening on %s", a)
		wg.Go(func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		})
	}
	code := 0
	select {
	case <-ctx.Done():
	case err := <-errs:
		lg.Print(err)
		code = 1
	}
	// A client may hold a connection open (HTTP/2 does); waiting on it would hold up a restart.
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if srv.Shutdown(sctx) != nil {
		srv.Close()
	}
	wg.Wait()
	return code
}
