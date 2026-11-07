package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// What the daemon must not reach, for the reason each is there. The names are the root work:
// the certificates' makers, the zones, the domain, the CA's files. A daemon that reaches one of
// these has found a way back to needing root, or to changing the CA (invariants 14 and 15).
var notForTheDaemon = map[string]string{
	"ensureCerts":      "makes the CA and the certificate",
	"loadCA":           "reads the CA's key",
	"newCA":            "makes a CA",
	"newLeaf":          "signs with the CA's key",
	"writePair":        "writes a key",
	"writeFileAtomic":  "writes into the certificates' directory",
	"writeFileOwned":   "writes what root keeps",
	"caKeyFile":        "names the CA's key",
	"caCertFile":       "names the CA's certificate",
	"ListenAddrs":      "asks the firewall and netifd",
	"newOpenWrtZones":  "calls ubus",
	"ZoneNetworks":     "reads the firewall",
	"NetworkAddresses": "calls ubus",
	"readDomain":       "reads dnsmasq's configuration",
	"dnsDomain":        "reads dnsmasq's configuration",
	"houseName":        "reads dnsmasq's configuration",
	"readSections":     "reads a UCI file by path",
	"LoadConfig":       "reads the backends' key files",
	"backendKeyLines":  "reads the backends' key files",
	"prepare":          "is root's",
}

// ... and what must not appear in anything it reaches, wherever it came from.
var notInTheDaemonText = []string{"/etc/config/dhcp", "/etc/config/firewall", "ubus", "ca.key"}

// daemonSources is the source tree this gate reads: the repository, or a tree the teeth planted.
func daemonSources() string {
	if d := os.Getenv("DAEMON_SRC"); d != "" {
		return d
	}
	return "."
}

// TestServeNeverExecs follows everything serve can reach through the package, and fails on any
// program it could start and on any of the root work above. It over-approximates on purpose: a
// method is reached by its bare name, and every method of a type the code names counts, which is
// how http.Server reaches Gateway.ServeHTTP. A false alarm is a rename; a miss is a daemon that
// needs root again, or that makes a new CA.
func TestServeNeverExecs(t *testing.T) {
	dir := daemonSources()
	paths, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	fset := token.NewFileSet()
	funcs := map[string][]*ast.FuncDecl{}
	methods := map[string][]string{}
	var daemonFields []string
	found := false
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				funcs[d.Name.Name] = append(funcs[d.Name.Name], d)
				if d.Recv != nil && len(d.Recv.List) == 1 {
					typ := d.Recv.List[0].Type
					if s, ok := typ.(*ast.StarExpr); ok {
						typ = s.X
					}
					if id, ok := typ.(*ast.Ident); ok {
						methods[id.Name] = append(methods[id.Name], d.Name.Name)
					}
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					ts, ok := s.(*ast.TypeSpec)
					if !ok || ts.Name.Name != "daemonEnv" {
						continue
					}
					found = true
					if st, ok := ts.Type.(*ast.StructType); ok {
						for _, field := range st.Fields.List {
							for _, n := range field.Names {
								daemonFields = append(daemonFields, n.Name)
							}
						}
					}
				}
			}
		}
	}
	if len(funcs["serve"]) == 0 || !found {
		t.Fatalf("measured nothing: no func serve and type daemonEnv in %s", dir)
	}
	for _, field := range daemonFields {
		if slices.Contains([]string{"uci", "zones", "reloadFirewall", "dhcpPath", "firewallPath", "account", "chown"}, field) {
			t.Errorf("daemonEnv has the field %s, which is prepare's", field)
		}
	}
	seen := map[string]string{"serve": "serve"}
	queue := []string{"serve"}
	visit := func(name, from string) {
		if _, ok := seen[name]; !ok && len(funcs[name]) > 0 {
			seen[name] = from + " -> " + name
			queue = append(queue, name)
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, fd := range funcs[name] {
			if fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "exec" {
						t.Errorf("the daemon can start a program: exec.%s in %s (%s)", x.Sel.Name, name, seen[name])
					}
				case *ast.Ident:
					if why, bad := notForTheDaemon[x.Name]; bad {
						t.Errorf("the daemon reaches %s, which %s: in %s (%s)", x.Name, why, name, seen[name])
					}
					visit(x.Name, seen[name])
					for _, m := range methods[x.Name] {
						visit(m, seen[name])
					}
				case *ast.BasicLit:
					for _, text := range notInTheDaemonText {
						if x.Kind == token.STRING && strings.Contains(x.Value, text) {
							t.Errorf("the daemon names %s: %s in %s (%s)", text, x.Value, name, seen[name])
						}
					}
				}
				return true
			})
		}
	}
	if _, ok := seen["ServeHTTP"]; !ok {
		t.Errorf("measured nothing: the walk from serve did not reach Gateway.ServeHTTP, so it follows less than the daemon runs: %v", seen)
	}
}

// shellHarness runs a piece of shell with stand-ins for what OpenWrt's rc.common gives the init
// script, and returns its trace, one word per step.
func shellHarness(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("the harness failed: %v\n%s", err, out)
	}
	return string(out)
}

const initStandIns = `
config_load() { :; }
config_get_bool() { eval "$1=${ENABLED:-1}"; }
config_get() { if [ "$3" = cert_dir ] && [ -n "${CERT_DIR:-}" ]; then eval "$1=$CERT_DIR"; else eval "$1=\"\${4:-}\""; fi; }
sync_firewall() { echo firewall; }
sync_dns() { echo dns; }
procd_open_instance() { echo open; }
procd_set_param() { echo "param $*"; }
procd_add_jail() { echo "jail $*"; }
procd_add_jail_mount() { for p in "$@"; do echo "mount ro $p"; done; }
procd_add_jail_mount_rw() { for p in "$@"; do echo "mount rw $p"; done; }
procd_close_instance() { echo close; }
logger() { while read -r line; do echo "logger $line"; done; [ $# -lt 3 ] || echo "logger $3"; }
group_add_next() { [ -n "${NO_GROUP:-}" ] || { echo group >&2; echo 500; }; }
user_exists() { [ -f "$WORK/user" ]; }
user_add() { echo "user_add $*" >&2; [ -n "${NO_USER:-}" ] || touch "$WORK/user"; }
`

// initTrace runs start_service from the real init script, with its program a stand-in that
// prints line (when given) to stderr and exits with code.
func initTrace(t *testing.T, env, line, code string) []string {
	t.Helper()
	const path = "files/etc/init.d/llm-gateway"
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("measured nothing: %v", err)
	}
	work := t.TempDir()
	prog := filepath.Join(work, "prog")
	body := "#!/bin/sh\necho \"$1\" >> " + work + "/called\n"
	if line != "" {
		body += "echo 'llm-gateway: " + line + "' >&2\n"
	}
	body += "exit " + code + "\n"
	if err := os.WriteFile(prog, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	trace := shellHarness(t, "WORK="+work+"; "+env+"\n. ./"+path+"\nPROG="+prog+"\n"+initStandIns+"\nstart_service 2>&1\necho rc=$?\ncat "+work+"/called 2>/dev/null | sed 's/^/called /'\n")
	return strings.Split(strings.TrimSpace(trace), "\n")
}

func indexOf(lines []string, prefix string) int {
	return slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, prefix) })
}

func TestInitPreparesBeforeAnythingElse(t *testing.T) {
	trace := initTrace(t, "", "", "0")
	order := []string{"group", "user_add", "firewall", "dns", "open", "close"}
	last := -1
	for _, step := range order {
		i := indexOf(trace, step)
		if i <= last {
			t.Fatalf("%s is out of order in %q", step, trace)
		}
		last = i
	}
	if indexOf(trace, "called prepare") < 0 {
		t.Fatalf("start_service did not run prepare: %q", trace)
	}
	if indexOf(trace, "param command") < 0 || !strings.HasSuffix(trace[indexOf(trace, "param command")], " serve") {
		t.Fatalf("the instance does not run serve: %q", trace)
	}
}

// TestInitScriptHardening reads what the real start_service asks of procd: the daemon's own user,
// no_new_privs, a jail it cannot start without, no capability, and no procd seccomp profile, which a
// static binary never loads. The mounts are the point: what the daemon sees is what serve reads, the
// journal's directory alone is writable, and the CA's key is not there.
func TestInitScriptHardening(t *testing.T) {
	trace := initTrace(t, "", "", "0")
	for _, want := range []string{"param user llm-gateway", "param group llm-gateway", "param no_new_privs 1",
		"param capabilities /etc/capabilities/llm-gateway.json", "jail llm-gateway log procfs ronly requirejail"} {
		if !slices.Contains(trace, want) {
			t.Errorf("start_service does not ask for %q", want)
		}
	}
	allowed := []string{"/etc/config/llm-gateway", "/etc/llm-gateway/tls.crt", "/etc/llm-gateway/tls.key",
		"/var/run/llm-gateway", "/etc/hosts", "/etc/ssl/certs", "/etc/resolv.conf", "/tmp/resolv.conf", "/tmp/resolv.conf.d"}
	var ro, rw []string
	for _, l := range trace {
		switch {
		case strings.HasPrefix(l, "param seccomp"):
			t.Errorf("start_service asks procd for a seccomp profile, which a static binary never loads: %q", l)
		case strings.HasPrefix(l, "jail ") && strings.Contains(l, "sysfs"):
			t.Errorf("the jail has sysfs, which nothing here reads: %q", l)
		case strings.HasPrefix(l, "mount ro "):
			ro = append(ro, strings.TrimPrefix(l, "mount ro "))
		case strings.HasPrefix(l, "mount rw "):
			rw = append(rw, strings.TrimPrefix(l, "mount rw "))
		}
	}
	for _, p := range ro {
		if !slices.Contains(allowed, p) {
			t.Errorf("the jail mounts %s, which serve does not read", p)
		}
	}
	for _, p := range append(ro, rw...) {
		for _, never := range []string{"ca.key", "ca.crt", "/etc/config/dhcp", "/etc/config/firewall", "/etc/config/network", "/etc/config/system"} {
			if strings.Contains(p, never) {
				t.Errorf("the jail mounts %s", p)
			}
		}
		// A directory that holds what must not be there is that thing mounted by another name.
		for _, holder := range []string{"/", "/etc", "/etc/config", "/etc/llm-gateway", "/var", "/var/run", "/tmp"} {
			if strings.TrimSuffix(p, "/") == strings.TrimSuffix(holder, "/") {
				t.Errorf("the jail mounts %s, which holds what it must not", p)
			}
		}
	}
	if len(ro) == 0 {
		t.Fatalf("measured nothing: no mounts in %q", trace)
	}
	if !slices.Equal(rw, []string{"/var/run/llm-gateway/journal"}) {
		t.Errorf("writable in the jail: %v, want the journal's directory alone", rw)
	}
	// A cert_dir of the owner's own is followed, and its CA key stays out.
	moved := initTrace(t, "CERT_DIR=/srv/gw", "", "0")
	if !slices.Contains(moved, "mount ro /srv/gw/tls.key") || slices.Contains(moved, "mount ro /etc/llm-gateway/tls.key") {
		t.Errorf("the jail does not follow cert_dir: %q", moved)
	}
}

// TestCapabilitiesFileIsEmpty: all five sets named and empty. procd 2026.03.13 (58eb263d) leaves a
// set the file does not name as it is, and with no file at all it leaves the bounding set full.
func TestCapabilitiesFileIsEmpty(t *testing.T) {
	const path = "files/etc/capabilities/llm-gateway.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("measured nothing: %v", err)
	}
	var sets map[string][]string
	if err := json.Unmarshal(raw, &sets); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for _, name := range []string{"bounding", "effective", "inheritable", "permitted", "ambient"} {
		caps, ok := sets[name]
		if !ok {
			t.Errorf("the set %s is not named, which procd reads as leaving it alone", name)
		}
		if len(caps) != 0 {
			t.Errorf("the set %s grants %v", name, caps)
		}
	}
	if len(sets) != 5 {
		t.Errorf("%d sets, want 5: %v", len(sets), sets)
	}
	stage := staged(t)
	if !bytes.Equal(raw, readAll(t, filepath.Join(stage, "root", "etc", "capabilities", "llm-gateway.json"))) {
		t.Error("the package does not carry the file")
	}
	if !strings.Contains(string(readAll(t, filepath.Join(stage, "run-mkpkg.sh"))), "procd-ujail") {
		t.Error("the package does not depend on procd-ujail, and requirejail means no jail, no start")
	}
}

// TestConfineComesFirst: before the daemon opens a file or a socket it has refused root and put its
// filter on. Everything after this runs filtered, so the list holds every call it makes.
func TestConfineComesFirst(t *testing.T) {
	dir := daemonSources()
	path := filepath.Join(dir, "main.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("measured nothing: %v", err)
	}
	var body []ast.Stmt
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "serve" && fn.Recv == nil {
			body = fn.Body.List
		}
	}
	if len(body) == 0 {
		t.Fatalf("measured nothing: no func serve in %s", path)
	}
	calls := func(n ast.Node) []string {
		var out []string
		ast.Inspect(n, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				switch fun := c.Fun.(type) {
				case *ast.Ident:
					out = append(out, fun.Name)
				case *ast.SelectorExpr:
					if id, ok := fun.X.(*ast.Ident); ok {
						out = append(out, id.Name+"."+fun.Sel.Name)
					}
				}
			}
			return true
		})
		return out
	}
	free := []string{"log.New", "e.euid", "lg.Print"}
	euidAt, confinedAt := -1, -1
	for i, s := range body {
		for _, c := range calls(s) {
			switch {
			case c == "e.euid" && euidAt < 0:
				euidAt = i
			case c == "confined" && confinedAt < 0:
				confinedAt = i
			case confinedAt < 0 && !slices.Contains(free, c):
				t.Errorf("serve calls %s before its filter is on", c)
			}
		}
	}
	if euidAt < 0 || confinedAt < 0 || euidAt > confinedAt {
		t.Fatalf("serve checks root at statement %d and puts its filter on at %d", euidAt, confinedAt)
	}
	// And a refusal of either ends it.
	for _, i := range []int{euidAt, confinedAt} {
		ifs, ok := body[i].(*ast.IfStmt)
		if !ok || len(ifs.Body.List) == 0 {
			t.Fatalf("statement %d of serve is no check", i)
		}
		if _, ok := ifs.Body.List[len(ifs.Body.List)-1].(*ast.ReturnStmt); !ok {
			t.Errorf("statement %d of serve goes on after a refusal", i)
		}
	}
}

func TestInitRefusalIsOneLineAndStartsNothing(t *testing.T) {
	trace := initTrace(t, "", "gateway: port 443 is below 1024, which the gateway does not bind; not starting", "1")
	if trace[len(trace)-2] != "rc=1" {
		t.Fatalf("start_service returned %q, want rc=1: %q", trace[len(trace)-2], trace)
	}
	for _, step := range []string{"firewall", "dns", "open"} {
		if indexOf(trace, step) >= 0 {
			t.Fatalf("a refused prepare still reached %s: %q", step, trace)
		}
	}
	var logged []string
	for _, l := range trace {
		if strings.HasPrefix(l, "logger ") {
			logged = append(logged, l)
		}
	}
	if len(logged) != 1 {
		t.Fatalf("a refusal took %d log lines, want 1: %q", len(logged), trace)
	}
	if !strings.Contains(logged[0], "port 443") || strings.Contains(logged[0], "logger llm-gateway:") {
		t.Fatalf("the log line reads %q", logged[0])
	}
}

func TestInitWithoutTheAccountStartsNothing(t *testing.T) {
	for name, env := range map[string]string{"no group": "NO_GROUP=1", "no user": "NO_USER=1"} {
		t.Run(name, func(t *testing.T) {
			trace := initTrace(t, env, "", "0")
			if indexOf(trace, "called prepare") >= 0 || indexOf(trace, "open") >= 0 {
				t.Fatalf("it went on without the account: %q", trace)
			}
			if indexOf(trace, "logger the llm-gateway account") < 0 {
				t.Fatalf("no line about the account: %q", trace)
			}
		})
	}
	// A gateway that is off makes no account and prepares nothing.
	trace := initTrace(t, "ENABLED=0", "", "0")
	if indexOf(trace, "called prepare") >= 0 || indexOf(trace, "group") >= 0 || indexOf(trace, "open") >= 0 {
		t.Fatalf("a gateway that is off went on: %q", trace)
	}
}

// staged runs mkapk.sh as far as it goes without apk: it writes the package's tree and scripts.
func staged(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("mkapk.sh"); err != nil {
		t.Fatalf("measured nothing: %v", err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(dir, "stage")
	cmd := exec.Command("sh", "mkapk.sh", bin, "0.1.0", "x86_64")
	cmd.Env = append(os.Environ(), "MKAPK_STAGE="+stage)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mkapk.sh: %v\n%s", err, out)
	}
	return stage
}

func TestPackageRunsPostUpgrade(t *testing.T) {
	stage := staged(t)
	src := string(readAll(t, "mkapk.sh"))
	// Both ways to the package: the one CI takes and the one the vlab run takes.
	if got := regexp.MustCompile(`--script "post-upgrade:`).FindAllString(src, -1); len(got) != 1 {
		t.Fatalf("the direct mkpkg names post-upgrade %d times, want 1", len(got))
	}
	if !strings.Contains(string(readAll(t, filepath.Join(stage, "run-mkpkg.sh"))), "--script post-upgrade:post-upgrade") {
		t.Fatal("the staged mkpkg does not pack post-upgrade")
	}
	if regexp.MustCompile(`(?m)^# post-install covers a fresh install and an upgrade`).MatchString(src) {
		t.Fatal("the comment that says post-install covers an upgrade is back")
	}
	up := string(readAll(t, filepath.Join(stage, "post-upgrade")))
	if regexp.MustCompile(`(?m) enable$`).MatchString(up) {
		t.Fatalf("post-upgrade enables the service:\n%s", up)
	}
	// post-upgrade runs against a stand-in for the init script.
	work := t.TempDir()
	stub := filepath.Join(work, "init")
	oldTmp := filepath.Join(work, "old")
	body := "#!/bin/sh\necho \"$1\" >> " + work + "/calls\n" +
		"case \"$1\" in enabled) [ -f " + work + "/enabled ] ;; running) [ -f " + work + "/running ] ;; esac\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.NewReplacer("/etc/init.d/llm-gateway", stub, "/tmp/llm-gateway", oldTmp).Replace(up)
	if strings.Contains(script, "/tmp/llm-gateway") {
		t.Fatal("the script still names the real path")
	}
	runIt := func(flags ...string) []string {
		t.Helper()
		for _, f := range []string{"calls", "enabled", "running"} {
			os.Remove(filepath.Join(work, f))
		}
		for _, f := range flags {
			if err := os.WriteFile(filepath.Join(work, f), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(oldTmp, 0o700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("post-upgrade: %v\n%s", err, out)
		}
		if _, err := os.Stat(oldTmp); err == nil {
			t.Fatal("post-upgrade left the old journal in /tmp")
		}
		return strings.Fields(string(readAll(t, filepath.Join(work, "calls"))))
	}
	if got := runIt("enabled"); !slices.Contains(got, "account") || !slices.Contains(got, "restart") {
		t.Fatalf("enabled at boot: %v, want account and restart", got)
	}
	if got := runIt("running"); !slices.Contains(got, "restart") {
		t.Fatalf("running but not enabled at boot: %v, want restart", got)
	}
	if got := runIt(); slices.Contains(got, "restart") || !slices.Contains(got, "account") {
		t.Fatalf("neither enabled nor running: %v, want the account and no start", got)
	}
	// A fresh install runs post-install alone, so it needs the account as well.
	if in := string(readAll(t, filepath.Join(stage, "post-install"))); !strings.Contains(in, "/etc/init.d/llm-gateway account") {
		t.Fatalf("post-install makes no account:\n%s", in)
	}
	// Removal takes the copy of the backends' keys and leaves the account.
	de := string(readAll(t, filepath.Join(stage, "pre-deinstall")))
	if !strings.Contains(de, "rm -rf /var/run/llm-gateway") {
		t.Fatalf("pre-deinstall leaves the keys' copy in RAM:\n%s", de)
	}
	if strings.Contains(de, "deluser") || strings.Contains(de, "/etc/passwd") {
		t.Fatalf("pre-deinstall touches the account:\n%s", de)
	}
	if !strings.Contains(string(readAll(t, filepath.Join(stage, "root", "etc", "hotplug.d", "ntp", "50-llm-gateway"))), "renew-due") {
		t.Fatal("the package does not carry the ntp hook")
	}
}

func TestNtpHookRenewsOnlyWhenTheClockCanBeBelieved(t *testing.T) {
	const path = "files/etc/hotplug.d/ntp/50-llm-gateway"
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("measured nothing: %v", err)
	}
	work := t.TempDir()
	calls := filepath.Join(work, "calls")
	initStub := filepath.Join(work, "init")
	binStub := filepath.Join(work, "bin")
	for p, body := range map[string]string{
		initStub: "#!/bin/sh\necho \"init $1\" >> " + calls + "\n[ \"$1\" != running ] || [ -f " + work + "/up ]\n",
		binStub:  "#!/bin/sh\necho \"bin $1\" >> " + calls + "\n[ -f " + work + "/due ]\n",
	} {
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer("/etc/init.d/llm-gateway", initStub, "/usr/bin/llm-gateway", binStub).Replace(string(readAll(t, path)))
	// The background job is the hook's; the test waits for it with `wait`.
	script = strings.Replace(script, "\nexit 0\n", "\nwait\nexit 0\n", 1)
	script = strings.Replace(script, "[ -x "+binStub+" ] || exit 0\n", "[ -x "+binStub+" ] || exit 0\nlogger() { :; }\n", 1)
	hook := func(action, stratum string, flags ...string) string {
		t.Helper()
		for _, f := range []string{"calls", "up", "due"} {
			os.Remove(filepath.Join(work, f))
		}
		for _, f := range flags {
			os.WriteFile(filepath.Join(work, f), nil, 0o644)
		}
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(os.Environ(), "ACTION="+action, "stratum="+stratum)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("the hook: %v\n%s", err, out)
		}
		got, _ := os.ReadFile(calls)
		return strings.TrimSpace(strings.ReplaceAll(string(got), "\n", ", "))
	}
	if got := hook("periodic", "3", "up", "due"); !strings.Contains(got, "init restart") {
		t.Fatalf("a synced clock and a certificate due: %q, want a restart", got)
	}
	for name, got := range map[string]string{
		"the certificate is not due":  hook("periodic", "3", "up"),
		"the gateway is not running":  hook("periodic", "3", "due"),
		"the clock is not synced":     hook("periodic", "16", "up", "due"),
		"no stratum":                  hook("periodic", "", "up", "due"),
		"a stratum that is no number": hook("periodic", "x", "up", "due"),
		"another event":               hook("stratum", "3", "up", "due"),
	} {
		if strings.Contains(got, "restart") {
			t.Fatalf("%s: %q, want no restart", name, got)
		}
	}
}
