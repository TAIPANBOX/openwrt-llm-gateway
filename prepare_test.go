package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const testUID, testGID = 5001, 5002

// fakeOwners stands in for chown, which a test run without root cannot do: it records who was
// given what. A file written atomically is given its owner under a temporary name, so that name
// is read back as the file's own. A path that is not there fails as chown would.
type fakeOwners struct {
	mu     sync.Mutex
	owners map[string][2]int
}

func newFakeOwners() *fakeOwners { return &fakeOwners{owners: map[string][2]int{}} }

var tempName = regexp.MustCompile(`^\.(.+)\.\d+$`)

func (f *fakeOwners) chown(path string, uid, gid int) error {
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	if m := tempName.FindStringSubmatch(filepath.Base(path)); m != nil {
		path = filepath.Join(filepath.Dir(path), m[1])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owners[path] = [2]int{uid, gid}
	return nil
}

func (f *fakeOwners) of(t *testing.T, path string) [2]int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.owners[path]
	if !ok {
		t.Fatalf("%s was given no owner; given: %v", path, f.owners)
	}
	return o
}

// gatewayConf is an enabled gateway on the zone lan, with the given sections after it.
func gatewayConf(certDir, more string) string {
	return "config gateway 'main'\n\toption enabled '1'\n\tlist zone 'lan'\n\toption cert_dir '" + certDir + "'\n" + more
}

const aBackendAndAClient = "config backend 'desktop'\n\toption url 'http://127.0.0.1:9/v1'\n\tlist model 'm1'\n" +
	"config client 'laptop'\n\toption key_hash 'sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + "'\n\tlist model '*'\n"

func prepareEnv(t *testing.T, more string) (*env, *fakeOwners, string) {
	t.Helper()
	certDir := filepath.Join(t.TempDir(), "certs")
	e, _ := testEnv(t, gatewayConf(certDir, more))
	o := newFakeOwners()
	e.chown = o.chown
	return e, o, certDir
}

func setDomain(t *testing.T, e *env, domain string) {
	t.Helper()
	if err := os.WriteFile(e.dhcpPath, []byte("config dnsmasq\n\toption domain '"+domain+"'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runPrepare fails the test unless prepare succeeds, and returns what it said.
func runPrepare(t *testing.T, e *env) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := run([]string{"prepare"}, &out, &errOut, e); code != 0 {
		t.Fatalf("prepare exited %d: %s", code, errOut.String())
	}
	return errOut.String()
}

// prepareRefuses fails the test unless prepare refuses, and returns what it said.
func prepareRefuses(t *testing.T, e *env) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := run([]string{"prepare"}, &out, &errOut, e); code != 1 {
		t.Fatalf("prepare exited %d, want 1: %s", code, errOut.String())
	}
	return errOut.String()
}

type snapshotOnDisk struct {
	Version int      `json:"version"`
	Hash    string   `json:"config_sha256"`
	Name    string   `json:"name"`
	Listen  []string `json:"listen"`
	Hosts   []string `json:"hosts"`
}

func snapshotPath(e *env) string { return filepath.Join(e.runDir, "snapshot.json") }

func readSnapshotFile(t *testing.T, e *env) snapshotOnDisk {
	t.Helper()
	raw, err := os.ReadFile(snapshotPath(e))
	if err != nil {
		t.Fatal(err)
	}
	var s snapshotOnDisk
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("the snapshot is not JSON: %v\n%s", err, raw)
	}
	return s
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func readAll(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// serveRefuses runs the daemon and fails the test unless it stops by itself, binding nothing. It
// returns the daemon's log. The bind is a stand-in, always: on a machine without the test's address
// a real one fails too, and a daemon that took a bad snapshot would pass for one that refused it.
func serveRefuses(t *testing.T, e *env) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	var log bytes.Buffer
	var bound atomic.Int64
	e.listen = func(network, addr string) (net.Listener, error) {
		bound.Add(1)
		return net.Listen(network, "127.0.0.1:0")
	}
	if code := serve(ctx, e.daemon(), &log); code != 1 {
		t.Fatalf("serve exited %d, want 1: %s", code, log.String())
	}
	if n := bound.Load(); n != 0 {
		t.Fatalf("serve bound %d address(es) before refusing", n)
	}
	return log.String()
}

// daemonUp runs the daemon until the test ends. It returns the addresses serve asked for, in
// order, and the real ones each stands for.
func daemonUp(t *testing.T, e *env, want int) (asked []string, real map[string]string) {
	t.Helper()
	var mu sync.Mutex
	real = map[string]string{}
	e.listen = func(network, addr string) (net.Listener, error) {
		ln, err := net.Listen(network, "127.0.0.1:0")
		if err == nil {
			mu.Lock()
			asked = append(asked, addr)
			real[addr] = ln.Addr().String()
			mu.Unlock()
		}
		return ln, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	var log syncWriter
	var logged bytes.Buffer
	log.w = &logged
	done := make(chan int, 1)
	go func() { done <- serve(ctx, e.daemon(), &log) }()
	t.Cleanup(func() {
		http.DefaultClient.CloseIdleConnections()
		cancel()
		<-done
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(asked)
		mu.Unlock()
		if n >= want {
			return asked, real
		}
		select {
		case code := <-done:
			done <- code
			t.Fatalf("serve exited %d before listening: %s", code, logged.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("serve did not listen on %d address(es)", want)
	return nil, nil
}

func TestPrepareWritesTheSnapshotAndTheKeys(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "upstream.key")
	if err := os.WriteFile(keyFile, []byte("upstream-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, o, _ := prepareEnv(t, "config backend 'desktop'\n\toption url 'http://127.0.0.1:9/v1'\n\toption key_file '"+keyFile+"'\n\tlist model 'm1'\n")
	e.zones = fakeZones{zones: map[string][]string{"lan": {"lan"}},
		addrs: map[string][]netip.Addr{"lan": {netip.MustParseAddr("192.168.8.1"), netip.MustParseAddr("fd00:8::1")}}}
	runPrepare(t, e)
	s := readSnapshotFile(t, e)
	sum := sha256.Sum256(readAll(t, e.configPath))
	if s.Version != 1 || s.Hash != hex.EncodeToString(sum[:]) || s.Name != "llm.lan" {
		t.Fatalf("snapshot %+v", s)
	}
	if got := strings.Join(s.Listen, " "); got != "192.168.8.1:8760 [fd00:8::1]:8760 127.0.0.1:8761" {
		t.Fatalf("listen %q", got)
	}
	if got := strings.Join(s.Hosts, " "); got != "llm.lan localhost 192.168.8.1 fd00:8::1 127.0.0.1" {
		t.Fatalf("hosts %q", got)
	}
	keys := filepath.Join(e.runDir, "backend.keys")
	if got := string(readAll(t, keys)); got != "desktop upstream-secret\n" {
		t.Fatalf("backend.keys holds %q", got)
	}
	for _, f := range []string{snapshotPath(e), keys} {
		if p := perm(t, f); p != 0o640 {
			t.Fatalf("%s has mode %o, want 640", f, p)
		}
		if got := o.of(t, f); got != [2]int{0, testGID} {
			t.Fatalf("%s belongs to %v, want root and the account's group", f, got)
		}
	}
}

func TestPrepareOwnsTheSnapshotDirectory(t *testing.T) {
	e, o, _ := prepareEnv(t, "")
	// Left open by something else: prepare takes it back, not only on first creation.
	if err := os.MkdirAll(e.runDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.runDir, 0o777); err != nil {
		t.Fatal(err)
	}
	runPrepare(t, e)
	if p := perm(t, e.runDir); p != 0o750 {
		t.Fatalf("the snapshot's directory has mode %o, want 750", p)
	}
	if got := o.of(t, e.runDir); got != [2]int{0, testGID} {
		t.Fatalf("the snapshot's directory belongs to %v, want root and the account's group", got)
	}
	j := filepath.Join(e.runDir, "journal")
	if p := perm(t, j); p != 0o700 {
		t.Fatalf("the journal's directory has mode %o, want 700", p)
	}
	if got := o.of(t, j); got != [2]int{testUID, testGID} {
		t.Fatalf("the journal's directory belongs to %v, want the account", got)
	}
	// A journal the root daemon wrote is the account's once the daemon runs without root.
	log := filepath.Join(j, "journal.log")
	if err := os.WriteFile(log, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPrepare(t, e)
	if got := o.of(t, log); got != [2]int{testUID, testGID} {
		t.Fatalf("the journal belongs to %v, want the account", got)
	}
	// A symbolic link in the directory's place is refused, not followed.
	e2, _, _ := prepareEnv(t, "")
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, e2.runDir); err != nil {
		t.Fatal(err)
	}
	if out := prepareRefuses(t, e2); !strings.Contains(out, "not a directory") {
		t.Fatalf("a symbolic link: %q", out)
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("prepare wrote through the link: %v", ents)
	}
}

func TestPrepareGivesOnlyTheLeafKeyToTheGroup(t *testing.T) {
	e, o, certDir := prepareEnv(t, "")
	runPrepare(t, e)
	leafKey := filepath.Join(certDir, "tls.key")
	if p := perm(t, leafKey); p != 0o640 {
		t.Fatalf("tls.key has mode %o, want 640", p)
	}
	if got := o.of(t, leafKey); got != [2]int{0, testGID} {
		t.Fatalf("tls.key belongs to %v, want root and the account's group", got)
	}
	if p := perm(t, filepath.Join(certDir, "ca.key")); p != 0o600 {
		t.Fatalf("ca.key has mode %o, want 600", p)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if got, ok := o.owners[filepath.Join(certDir, "ca.key")]; ok {
		t.Fatalf("ca.key was given to %v; it stays root's alone", got)
	}
	if p := perm(t, certDir); p != 0o700 {
		t.Fatalf("the certificates' directory has mode %o, want 700", p)
	}
}

func TestPrepareTwiceChangesNothing(t *testing.T) {
	e, _, certDir := prepareEnv(t, "")
	runPrepare(t, e)
	files := []string{"ca.crt", "ca.key", "tls.crt", "tls.key"}
	before := map[string][]byte{}
	for _, f := range files {
		before[f] = readAll(t, filepath.Join(certDir, f))
	}
	snap := readAll(t, snapshotPath(e))
	if out := runPrepare(t, e); out != "" {
		t.Fatalf("a second prepare has something to say: %q", out)
	}
	for _, f := range files {
		if !bytes.Equal(before[f], readAll(t, filepath.Join(certDir, f))) {
			t.Fatalf("a second prepare changed %s", f)
		}
	}
	if !bytes.Equal(snap, readAll(t, snapshotPath(e))) {
		t.Fatal("a second prepare changed the snapshot")
	}
}

func TestPrepareKeepsTheCA(t *testing.T) {
	fresh := func(t *testing.T) (*env, string, []byte, []byte) {
		e, _, certDir := prepareEnv(t, "")
		setDomain(t, e, "home.arpa")
		runPrepare(t, e)
		if got := readSnapshotFile(t, e).Name; got != "llm.home.arpa" {
			t.Fatalf("name %q, want llm.home.arpa", got)
		}
		return e, certDir, readAll(t, filepath.Join(certDir, "ca.crt")), readAll(t, filepath.Join(certDir, "ca.key"))
	}
	same := func(t *testing.T, certDir string, crt, key []byte) {
		t.Helper()
		if !bytes.Equal(crt, readAll(t, filepath.Join(certDir, "ca.crt"))) || !bytes.Equal(key, readAll(t, filepath.Join(certDir, "ca.key"))) {
			t.Fatal("the CA changed: every device that trusted it would have to trust it again")
		}
	}
	t.Run("a read that fails stops prepare and changes nothing", func(t *testing.T) {
		e, certDir, crt, key := fresh(t)
		snap := readAll(t, snapshotPath(e))
		// A directory opens and then fails on read, which is how an unreadable file reaches
		// the scanner; removing it first leaves nothing of the file behind.
		os.Remove(e.dhcpPath)
		if err := os.Mkdir(e.dhcpPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if out := prepareRefuses(t, e); !strings.Contains(out, "domain") {
			t.Fatalf("the refusal does not name the domain: %q", out)
		}
		same(t, certDir, crt, key)
		if !bytes.Equal(snap, readAll(t, snapshotPath(e))) {
			t.Fatal("a refused prepare changed the snapshot")
		}
	})
	t.Run("a file that may not be read stops prepare", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a file of any mode")
		}
		e, certDir, crt, key := fresh(t)
		if err := os.Chmod(e.dhcpPath, 0); err != nil {
			t.Fatal(err)
		}
		prepareRefuses(t, e)
		same(t, certDir, crt, key)
	})
	t.Run("no dhcp file keeps the name the CA was made for", func(t *testing.T) {
		e, certDir, crt, key := fresh(t)
		os.Remove(e.dhcpPath)
		runPrepare(t, e)
		same(t, certDir, crt, key)
		if got := readSnapshotFile(t, e).Name; got != "llm.home.arpa" {
			t.Fatalf("name %q, want the CA's own", got)
		}
	})
	t.Run("a dhcp file without the option keeps it too", func(t *testing.T) {
		e, certDir, crt, key := fresh(t)
		if err := os.WriteFile(e.dhcpPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		runPrepare(t, e)
		same(t, certDir, crt, key)
	})
	t.Run("a router with neither takes the default", func(t *testing.T) {
		e, _, _ := prepareEnv(t, "")
		os.Remove(e.dhcpPath)
		runPrepare(t, e)
		if got := readSnapshotFile(t, e).Name; got != "llm.lan" {
			t.Fatalf("name %q, want llm.lan", got)
		}
	})
}

func TestPrepareRefusesADamagedCA(t *testing.T) {
	for name, damage := range map[string]func(dir string) error{
		"the key is garbage":         func(d string) error { return os.WriteFile(filepath.Join(d, "ca.key"), []byte("garbage"), 0o600) },
		"the certificate is garbage": func(d string) error { return os.WriteFile(filepath.Join(d, "ca.crt"), []byte("garbage"), 0o644) },
		"the key is gone":            func(d string) error { return os.Remove(filepath.Join(d, "ca.key")) },
		"the certificate is gone":    func(d string) error { return os.Remove(filepath.Join(d, "ca.crt")) },
		"the key is another's": func(d string) error {
			other := t.TempDir()
			if _, err := ensureCerts(other, houseNames, houseIPs, t0); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(d, "ca.key"), readAll(t, filepath.Join(other, "ca.key")), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, _, certDir := prepareEnv(t, "")
			runPrepare(t, e)
			if err := damage(certDir); err != nil {
				t.Fatal(err)
			}
			crt, _ := os.ReadFile(filepath.Join(certDir, "ca.crt"))
			key, _ := os.ReadFile(filepath.Join(certDir, "ca.key"))
			if out := prepareRefuses(t, e); !strings.Contains(out, "CA") {
				t.Fatalf("the refusal does not name the CA: %q", out)
			}
			crt2, _ := os.ReadFile(filepath.Join(certDir, "ca.crt"))
			key2, _ := os.ReadFile(filepath.Join(certDir, "ca.key"))
			if !bytes.Equal(crt, crt2) || !bytes.Equal(key, key2) {
				t.Fatal("prepare rewrote a CA it could not read")
			}
		})
	}
}

func TestPrepareSaysWhenItMakesACA(t *testing.T) {
	e, _, certDir := prepareEnv(t, "")
	out := runPrepare(t, e)
	fp := caFingerprint(certDir)
	if !strings.Contains(out, "made the house CA") || !strings.Contains(out, fp) {
		t.Fatalf("the first CA is not announced with its fingerprint: %q", out)
	}
	// The owner changed the domain: the CA no longer covers the name, and that is said out loud.
	setDomain(t, e, "home.arpa")
	out = runPrepare(t, e)
	now := caFingerprint(certDir)
	if now == fp {
		t.Fatal("a CA that does not cover the name was kept")
	}
	if !strings.Contains(out, fp) || !strings.Contains(out, now) || !strings.Contains(out, "trust it again") {
		t.Fatalf("a replaced CA is not announced: %q", out)
	}
	if n := strings.Count(strings.TrimSpace(out), "\n"); n != 0 {
		t.Fatalf("a replaced CA takes %d lines, want one: %q", n+1, out)
	}
}

func TestPrepareRefusesWithoutTheAccount(t *testing.T) {
	e, _, certDir := prepareEnv(t, "")
	e.account = func() (serviceAccount, error) { return serviceAccount{}, errors.New("unknown user") }
	if out := prepareRefuses(t, e); !strings.Contains(out, "llm-gateway") || !strings.Contains(out, "unknown user") {
		t.Fatalf("the refusal does not name the account: %q", out)
	}
	if _, err := os.Stat(certDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("prepare went on to the certificates")
	}
}

func TestPrepareRefusesWhatServeWouldNotBind(t *testing.T) {
	e, _, certDir := prepareEnv(t, "")
	e.zones = fakeZones{zones: map[string][]string{"lan": {"lan", "wan"}}, addrs: map[string][]netip.Addr{}}
	if out := prepareRefuses(t, e); !strings.Contains(out, "wan") {
		t.Fatalf("the refusal does not name wan: %q", out)
	}
	if _, err := os.Stat(snapshotPath(e)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a snapshot was written")
	}
	if _, err := os.Stat(certDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("certificates were made")
	}
}

func TestPrepareWithNoAddressYetLeavesLoopbackAndSaysSo(t *testing.T) {
	e, _, _ := prepareEnv(t, "")
	e.zones = fakeZones{zones: map[string][]string{"lan": {"lan"}}, addrs: map[string][]netip.Addr{}}
	out := runPrepare(t, e)
	if !strings.Contains(out, "no address") {
		t.Fatalf("not said: %q", out)
	}
	if got := strings.Join(readSnapshotFile(t, e).Listen, " "); got != "127.0.0.1:8761" {
		t.Fatalf("listen %q, want loopback alone", got)
	}
}

func TestPrepareDoesNotWaitForDNS(t *testing.T) {
	// A backend by a name DNS cannot answer yet: prepare runs once, at boot, and does not fail on
	// it. The daemon still refuses it, and procd retries the daemon.
	e, _, _ := prepareEnv(t, "config backend 'desktop'\n\toption url 'http://box.lan:9/v1'\n\tlist model 'm1'\n")
	runPrepare(t, e)
	if log := serveRefuses(t, e); !strings.Contains(log, "box.lan") {
		t.Fatalf("the daemon took an unresolved backend: %q", log)
	}
}

func TestPrepareWaitsForAnotherPrepare(t *testing.T) {
	e, _, _ := prepareEnv(t, "")
	runPrepare(t, e)
	first := filepath.Join(e.runDir, "snapshot.json")
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(e.runDir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- run([]string{"prepare"}, io.Discard, io.Discard, e) }()
	select {
	case code := <-done:
		t.Fatalf("prepare ran while another held the lock, and exited %d", code)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(first); err == nil {
		t.Fatal("prepare wrote the snapshot while another held the lock")
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("prepare exited %d after the lock came free", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prepare did not finish after the lock came free")
	}
}

func TestDaemonLeavesTheCAAlone(t *testing.T) {
	e, _, certDir := prepareEnv(t, aBackendAndAClient)
	setDomain(t, e, "home.arpa")
	runPrepare(t, e)
	crt := filepath.Join(certDir, "ca.crt")
	key := filepath.Join(certDir, "ca.key")
	// The daemon runs with the CA out of reach, as it will in its jail. Nothing puts it back.
	if err := os.Remove(crt); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	daemonUp(t, e, 2)
	for _, f := range []string{crt, key} {
		if _, err := os.Stat(f); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the daemon made %s", f)
		}
	}
}

func TestDaemonDoesNotRepairCertificates(t *testing.T) {
	e, _, certDir := prepareEnv(t, aBackendAndAClient)
	runPrepare(t, e)
	before, err := os.ReadDir(certDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(certDir, "tls.crt")); err != nil {
		t.Fatal(err)
	}
	if log := serveRefuses(t, e); !strings.Contains(log, "certificates") {
		t.Fatalf("no line about the certificates: %q", log)
	}
	after, err := os.ReadDir(certDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)-1 {
		t.Fatalf("the daemon wrote into the certificates' directory: %v", after)
	}
}

func TestDaemonAsksTheRouterNothing(t *testing.T) {
	e, _, _ := prepareEnv(t, aBackendAndAClient)
	runPrepare(t, e)
	// Everything that needs root now fails the test if it is touched.
	asked := make(chan string, 8)
	e.zones = spyZones{asked}
	e.uci = func(string, ...string) ([]byte, error) { asked <- "uci"; return nil, nil }
	os.Remove(e.dhcpPath)
	os.Mkdir(e.dhcpPath, 0o755)
	asks, _ := daemonUp(t, e, 2)
	if got := strings.Join(asks, " "); got != "192.168.8.1:8760 127.0.0.1:8761" {
		t.Fatalf("serve asked to listen on %q", got)
	}
	select {
	case what := <-asked:
		t.Fatalf("the daemon asked %s", what)
	default:
	}
}

type spyZones struct{ asked chan string }

func (s spyZones) ZoneNetworks(zone string) ([]string, error) {
	s.asked <- "the firewall"
	return nil, errors.New("not for the daemon")
}

func (s spyZones) NetworkAddresses(network string) ([]netip.Addr, error) {
	s.asked <- "netifd"
	return nil, errors.New("not for the daemon")
}

func TestServeRefusesAMissingSnapshot(t *testing.T) {
	e, _, _ := prepareEnv(t, aBackendAndAClient)
	runPrepare(t, e)
	if err := os.Remove(snapshotPath(e)); err != nil {
		t.Fatal(err)
	}
	if log := serveRefuses(t, e); !strings.Contains(log, "snapshot.json") {
		t.Fatalf("no line about the snapshot: %q", log)
	}
}

func TestServeRefusesADamagedSnapshot(t *testing.T) {
	e, _, _ := prepareEnv(t, aBackendAndAClient)
	runPrepare(t, e)
	good := readAll(t, snapshotPath(e))
	put := func(b []byte) {
		t.Helper()
		if err := os.WriteFile(snapshotPath(e), b, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	// Cut at every length: a power cut, a full disk, a bad copy. Only the whole file will do.
	for i := range bytes.TrimSpace(good) {
		put(good[:i])
		t.Run(fmt.Sprintf("cut at byte %d", i), func(t *testing.T) { serveRefuses(t, e) })
	}
	for name, b := range map[string]string{
		"not JSON":                   "listen 192.168.8.1:8760\n",
		"another version":            strings.Replace(string(good), `"version":1`, `"version":2`, 1),
		"no version":                 strings.Replace(string(good), `"version":1,`, ``, 1),
		"an unknown field":           strings.Replace(string(good), `"version":1`, `"version":1,"extra":true`, 1),
		"something after it":         string(good) + "{}\n",
		"no name":                    strings.Replace(string(good), `"name":"llm.lan"`, `"name":""`, 1),
		"hosts of its own":           strings.Replace(string(good), `"localhost"`, `"evil.example"`, 1),
		"an array":                   "[]\n",
		"null":                       "null\n",
		"empty":                      "",
		"no listen":                  regexp.MustCompile(`"listen":\[[^\]]*\]`).ReplaceAllString(string(good), `"listen":[]`),
		"a listen of the wrong type": regexp.MustCompile(`"listen":\[[^\]]*\]`).ReplaceAllString(string(good), `"listen":"127.0.0.1:8761"`),
	} {
		put([]byte(b))
		t.Run(name, func(t *testing.T) { serveRefuses(t, e) })
	}
	put(good)
	daemonUp(t, e, 2)
}

func TestServeRefusesAStaleSnapshot(t *testing.T) {
	e, _, _ := prepareEnv(t, aBackendAndAClient)
	runPrepare(t, e)
	conf := readAll(t, e.configPath)
	if err := os.WriteFile(e.configPath, append(conf, "\toption port '9000'\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if log := serveRefuses(t, e); !strings.Contains(log, "another configuration") {
		t.Fatalf("no line about the configuration: %q", log)
	}
	// The same bytes back: the snapshot is good again, so a stale one is not a broken one.
	if err := os.WriteFile(e.configPath, conf, 0o644); err != nil {
		t.Fatal(err)
	}
	daemonUp(t, e, 2)
}

func TestServeRefusesASnapshotThatListensOutside(t *testing.T) {
	e, _, _ := prepareEnv(t, aBackendAndAClient)
	runPrepare(t, e)
	good := readSnapshotFile(t, e)
	for name, listen := range map[string][]string{
		"the IPv4 wildcard":         {"0.0.0.0:8760", "127.0.0.1:8761"},
		"the IPv6 wildcard":         {"[::]:8760", "127.0.0.1:8761"},
		"a public address":          {"203.0.113.20:8760", "127.0.0.1:8761"},
		"a public IPv6 address":     {"[2001:db8::1]:8760", "127.0.0.1:8761"},
		"a link-local address":      {"[fe80::1]:8760", "127.0.0.1:8761"},
		"the plain port on the LAN": {"192.168.8.1:8761", "127.0.0.1:8761"},
		"the TLS port on loopback":  {"127.0.0.1:8760", "127.0.0.1:8761"},
		"another port":              {"192.168.8.1:9000", "127.0.0.1:8761"},
		"another loopback address":  {"192.168.8.1:8760", "127.0.0.2:8761"},
		"no loopback":               {"192.168.8.1:8760"},
		"a name":                    {"llm.lan:8760", "127.0.0.1:8761"},
		"no port":                   {"192.168.8.1", "127.0.0.1:8761"},
		"a port past the end":       {"192.168.8.1:70000", "127.0.0.1:8761"},
	} {
		t.Run(name, func(t *testing.T) {
			hosts := []string{good.Name, "localhost"}
			for _, a := range listen {
				if ap, err := netip.ParseAddrPort(a); err == nil {
					hosts = append(hosts, ap.Addr().String())
				}
			}
			s := good
			s.Listen, s.Hosts = listen, hosts
			raw, _ := json.Marshal(s)
			if err := os.WriteFile(snapshotPath(e), raw, 0o640); err != nil {
				t.Fatal(err)
			}
			if log := serveRefuses(t, e); !strings.Contains(log, "listen") {
				t.Fatalf("no line about where it listens: %q", log)
			}
		})
	}
}

func TestServeRefusesASnapshotOthersCanChange(t *testing.T) {
	for name, mode := range map[string]os.FileMode{"group write": 0o660, "world read": 0o644, "world write": 0o666} {
		t.Run(name, func(t *testing.T) {
			e, _, _ := prepareEnv(t, aBackendAndAClient)
			runPrepare(t, e)
			if err := os.Chmod(snapshotPath(e), mode); err != nil {
				t.Fatal(err)
			}
			if log := serveRefuses(t, e); !strings.Contains(log, "mode") {
				t.Fatalf("no line about the mode: %q", log)
			}
		})
	}
	t.Run("the directory", func(t *testing.T) {
		e, _, _ := prepareEnv(t, aBackendAndAClient)
		runPrepare(t, e)
		if err := os.Chmod(e.runDir, 0o770); err != nil {
			t.Fatal(err)
		}
		if log := serveRefuses(t, e); !strings.Contains(log, "mode") {
			t.Fatalf("no line about the mode: %q", log)
		}
	})
	t.Run("a symbolic link", func(t *testing.T) {
		e, _, _ := prepareEnv(t, aBackendAndAClient)
		runPrepare(t, e)
		real := filepath.Join(e.runDir, "real.json")
		if err := os.Rename(snapshotPath(e), real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, snapshotPath(e)); err != nil {
			t.Fatal(err)
		}
		serveRefuses(t, e)
	})
}

func TestBackendKeyReachesTheDaemonThroughPrepare(t *testing.T) {
	b := newBackend(t)
	keyFile := filepath.Join(t.TempDir(), "upstream.key")
	if err := os.WriteFile(keyFile, []byte("upstream-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, _, _ := prepareEnv(t, "config backend 'desktop'\n\toption url '"+b.srv.URL+"/v1'\n\toption key_file '"+keyFile+"'\n\tlist model 'm1'\n"+
		"config client 'laptop'\n\toption key_hash '"+HashKey(testKey)+"'\n\tlist model '*'\n")
	runPrepare(t, e)
	// The daemon has no way to the key_file: it will not, in its jail.
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	_, real := daemonUp(t, e, 2)
	body := `{"model":"m1","messages":[{"role":"user","content":"hello"}]}`
	req, err := http.NewRequest(http.MethodPost, "http://"+real["127.0.0.1:8761"]+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "127.0.0.1"
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("a request through the daemon: %d", resp.StatusCode)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if got := b.last.Header.Get("Authorization"); got != "Bearer upstream-secret" {
		t.Fatalf("the backend was sent %q", got)
	}
}

func TestBackendKeysFileIsReadWithCare(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "upstream.key")
	if err := os.WriteFile(keyFile, []byte("upstream-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conf := "config backend 'desktop'\n\toption url 'http://127.0.0.1:9/v1'\n\toption key_file '" + keyFile + "'\n\tlist model 'm1'\n"
	for name, c := range map[string]struct {
		body string
		mode os.FileMode
		says string
	}{
		"a backend missing":  {"other upstream-secret\n", 0o640, "desktop"},
		"empty":              {"", 0o640, "desktop"},
		"a line with no key": {"upstream-secret\n", 0o640, "line 1"},
		"open to the world":  {"desktop upstream-secret\n", 0o644, "mode"},
	} {
		t.Run(name, func(t *testing.T) {
			e, _, _ := prepareEnv(t, conf)
			runPrepare(t, e)
			keys := filepath.Join(e.runDir, "backend.keys")
			if err := os.WriteFile(keys, []byte(c.body), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(keys, c.mode); err != nil {
				t.Fatal(err)
			}
			log := serveRefuses(t, e)
			if !strings.Contains(log, c.says) {
				t.Fatalf("the log %q does not say %q", log, c.says)
			}
			if strings.Contains(log, "upstream-secret") {
				t.Fatalf("the log holds the key: %q", log)
			}
		})
	}
	t.Run("a key file of two lines is refused by prepare", func(t *testing.T) {
		e, _, _ := prepareEnv(t, conf)
		if err := os.WriteFile(keyFile, []byte("first-secret\nsecond-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := prepareRefuses(t, e)
		if !strings.Contains(out, "desktop") || strings.Contains(out, "secret") {
			t.Fatalf("the refusal %q must name the backend and not the key", out)
		}
	})
	t.Run("a key file that cannot be read stops prepare", func(t *testing.T) {
		e, _, _ := prepareEnv(t, conf)
		if err := os.Remove(keyFile); err != nil {
			t.Fatal(err)
		}
		if out := prepareRefuses(t, e); !strings.Contains(out, "key_file") {
			t.Fatalf("the refusal %q does not name key_file", out)
		}
	})
}

func TestLANAddressChangeReachesTheDaemonThroughPrepare(t *testing.T) {
	e, _, certDir := prepareEnv(t, aBackendAndAClient)
	runPrepare(t, e)
	ca := caFingerprint(certDir)
	e.zones = fakeZones{zones: map[string][]string{"lan": {"lan"}},
		addrs: map[string][]netip.Addr{"lan": {netip.MustParseAddr("192.168.50.1")}}}
	runPrepare(t, e)
	if caFingerprint(certDir) != ca {
		t.Fatal("a new address changed the CA")
	}
	asks, _ := daemonUp(t, e, 2)
	if got := strings.Join(asks, " "); got != "192.168.50.1:8760 127.0.0.1:8761" {
		t.Fatalf("serve asked to listen on %q", got)
	}
	blk, _ := pem.Decode(readAll(t, filepath.Join(certDir, "tls.crt")))
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !leaf.IPAddresses[0].Equal(net.ParseIP("192.168.50.1")) {
		t.Fatalf("the certificate names %v", leaf.IPAddresses)
	}
}

func TestPortBelow1024IsRefused(t *testing.T) {
	for _, c := range []struct {
		option, port string
		ok           bool
	}{
		{"port", "1", false}, {"port", "80", false}, {"port", "443", false}, {"port", "1023", false},
		{"loopback_port", "443", false}, {"loopback_port", "1023", false},
		{"port", "1024", true}, {"loopback_port", "1024", true}, {"port", "65535", true},
	} {
		t.Run(c.option+" "+c.port, func(t *testing.T) {
			e, _, _ := prepareEnv(t, "")
			conf := strings.Replace(string(readAll(t, e.configPath)), "\toption enabled", "\toption "+c.option+" '"+c.port+"'\n\toption enabled", 1)
			if err := os.WriteFile(e.configPath, []byte(conf), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.ok {
				runPrepare(t, e)
				return
			}
			out := prepareRefuses(t, e)
			if strings.Count(out, "\n") != 1 || !strings.Contains(out, c.port) || !strings.Contains(out, "1024") {
				t.Fatalf("want one line naming %s and 1024, got %q", c.port, out)
			}
			if _, err := os.Stat(snapshotPath(e)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("a snapshot was written for a refused port")
			}
			// The same refusal from `check`, and from a daemon that respawned onto it.
			var errOut bytes.Buffer
			if code := run([]string{"check"}, io.Discard, &errOut, e); code == 0 || !strings.Contains(errOut.String(), "1024") {
				t.Fatalf("check: %d %q", code, errOut.String())
			}
			if log := serveRefuses(t, e); !strings.Contains(log, "1024") {
				t.Fatalf("the daemon's line: %q", log)
			}
		})
	}
	for _, port := range []string{"0", "-1", "65536", "https"} {
		_, err := ParseConfig(strings.NewReader("config gateway 'main'\n\toption port '"+port+"'\n"), nil)
		if err == nil || !strings.Contains(err.Error(), "is not a port") {
			t.Fatalf("port %s: %v", port, err)
		}
	}
}

func TestJournalLivesUnderVarRun(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader("config gateway 'main'\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JournalPath != "/var/run/llm-gateway/journal/journal.log" {
		t.Fatalf("the journal is at %s", cfg.JournalPath)
	}
	for _, path := range []string{"/tmp/llm-gateway/journal.log", "/etc/llm-gateway/journal.log", "/var/run/llm-gateway/journal",
		"/var/run/llm-gateway/journal/", "/var/run/llm-gateway/journal/a/b.log", "/var/run/llm-gateway/journal/../x.log", "journal.log"} {
		if _, err := ParseConfig(strings.NewReader("config gateway 'main'\n\toption journal '"+path+"'\n"), nil); err == nil {
			t.Fatalf("the journal was accepted at %s", path)
		}
	}
	cfg, err = ParseConfig(strings.NewReader("config gateway 'main'\n\toption journal '/var/run/llm-gateway/journal/other.log'\n"), nil)
	if err != nil || cfg.JournalPath != "/var/run/llm-gateway/journal/other.log" {
		t.Fatalf("a journal of another name: %v, %v", cfg, err)
	}
	// And that is where the daemon writes it.
	e, _, _ := prepareEnv(t, aBackendAndAClient)
	runPrepare(t, e)
	_, real := daemonUp(t, e, 2)
	req, err := http.NewRequest(http.MethodGet, "http://"+real["127.0.0.1:8761"]+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "127.0.0.1"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st, err := os.Stat(filepath.Join(e.runDir, "journal", "journal.log")); err != nil || st.Size() == 0 {
		t.Fatalf("the refusal is not in the journal under the run directory: %v", err)
	}
}

func TestRenewDueSaysWhenTheCertificateNearsItsEnd(t *testing.T) {
	e, _, certDir := prepareEnv(t, "")
	due := func(at time.Time) int {
		e.now = func() time.Time { return at }
		return run([]string{"renew-due"}, io.Discard, io.Discard, e)
	}
	if got := due(t0); got != 0 {
		t.Fatalf("no certificate yet: exited %d, want 0", got)
	}
	e.now = func() time.Time { return t0 }
	runPrepare(t, e)
	if got := due(t0); got != 1 {
		t.Fatalf("a fresh certificate: exited %d, want 1", got)
	}
	blk, _ := pem.Decode(readAll(t, filepath.Join(certDir, "tls.crt")))
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if got := due(leaf.NotAfter.Add(-30*24*time.Hour - time.Second)); got != 1 {
		t.Fatalf("a second before the window: exited %d, want 1", got)
	}
	if got := due(leaf.NotAfter.Add(-30*24*time.Hour + time.Second)); got != 0 {
		t.Fatalf("a second inside the window: exited %d, want 0", got)
	}
	// And prepare, which is what the restart runs, renews it under the same CA.
	ca := caFingerprint(certDir)
	e.now = func() time.Time { return leaf.NotAfter.Add(-29 * 24 * time.Hour) }
	runPrepare(t, e)
	if got := due(e.now()); got != 1 {
		t.Fatalf("after prepare: exited %d, want 1", got)
	}
	if caFingerprint(certDir) != ca {
		t.Fatal("renewing the certificate changed the CA")
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"check"}, &out, &errOut, e); code != 0 || !strings.Contains(out.String(), "certificate until") {
		t.Fatalf("check: %d %q %q", code, out.String(), errOut.String())
	}
	// A gateway that is switched off asks for nothing.
	off := strings.Replace(string(readAll(t, e.configPath)), "option enabled '1'", "option enabled '0'", 1)
	if err := os.WriteFile(e.configPath, []byte(off), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := due(leaf.NotAfter); got != 1 {
		t.Fatalf("switched off: exited %d, want 1", got)
	}
}

func TestServeSaysWhenTheCertificateHasEnded(t *testing.T) {
	e, _, _ := prepareEnv(t, aBackendAndAClient)
	runPrepare(t, e)
	e.now = func() time.Time { return time.Now().Add(400 * 24 * time.Hour) }
	var log bytes.Buffer
	var w syncWriter
	w.w = &log
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	e.listen = func(network, addr string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") }
	serve(ctx, e.daemon(), &w)
	if !strings.Contains(log.String(), "the certificate ended") {
		t.Fatalf("not said: %q", log.String())
	}
}

func TestPrepareStopsWhenAFileCannotGetItsOwner(t *testing.T) {
	for _, target := range []string{"run", "tls.key", "journal", "backend.keys", "snapshot.json"} {
		t.Run(target, func(t *testing.T) {
			e, o, _ := prepareEnv(t, "")
			e.chown = func(path string, uid, gid int) error {
				name := filepath.Base(path)
				if m := tempName.FindStringSubmatch(name); m != nil {
					name = m[1]
				}
				if name == target {
					return errors.New("chown refused")
				}
				return o.chown(path, uid, gid)
			}
			if out := prepareRefuses(t, e); !strings.Contains(out, "chown refused") {
				t.Fatalf("the refusal does not say why: %q", out)
			}
			if _, err := os.Stat(snapshotPath(e)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("a snapshot stands there although a file has the wrong owner")
			}
			ents, err := os.ReadDir(e.runDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, ent := range ents {
				if strings.HasPrefix(ent.Name(), ".") {
					t.Fatalf("a temporary file was left: %s", ent.Name())
				}
			}
		})
	}
}

func TestAConfigurationThatCannotBeReadStopsBothSteps(t *testing.T) {
	t.Run("gone", func(t *testing.T) {
		e, _, _ := prepareEnv(t, aBackendAndAClient)
		runPrepare(t, e)
		if err := os.Remove(e.configPath); err != nil {
			t.Fatal(err)
		}
		if out := prepareRefuses(t, e); !strings.Contains(out, e.configPath) {
			t.Fatalf("prepare: %q", out)
		}
		if log := serveRefuses(t, e); !strings.Contains(log, e.configPath) {
			t.Fatalf("serve: %q", log)
		}
		if code := run([]string{"renew-due"}, io.Discard, io.Discard, e); code != 1 {
			t.Fatalf("renew-due exited %d, want 1", code)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		e, _, _ := prepareEnv(t, "config client 'bad'\n\toption key_hash 'not a hash'\n\tlist model '*'\n")
		if out := prepareRefuses(t, e); !strings.Contains(out, "key_hash") {
			t.Fatalf("prepare: %q", out)
		}
	})
}

func TestPrepareOnADisabledGatewayMakesNoCertificates(t *testing.T) {
	e, _, certDir := prepareEnv(t, "")
	off := strings.Replace(string(readAll(t, e.configPath)), "option enabled '1'", "option enabled '0'", 1)
	if err := os.WriteFile(e.configPath, []byte(off), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := runPrepare(t, e); !strings.Contains(out, "disabled") {
		t.Fatalf("not said: %q", out)
	}
	if _, err := os.Stat(certDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("certificates were made for a gateway that is off")
	}
}
