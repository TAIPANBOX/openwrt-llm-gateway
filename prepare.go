package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// The gateway is split in two so that the daemon can lose root. `llm-gateway prepare` runs as root
// from the init script before every start and does what needs root: it reads the firewall, netifd
// and dnsmasq, looks after the certificates, and copies the backends' keys. `serve` then works
// from what prepare left and from nothing else: it calls no program, reads neither the firewall's
// nor dnsmasq's configuration, and has no way to make or change the CA (invariants 14 to 16).
//
// respawn does not run the init script, only the command, so a snapshot has to stay good for
// every respawn; a configuration changed since it was made is refused, and a reload makes a new one.

const (
	accountName     = "llm-gateway"
	snapshotFile    = "snapshot.json"
	backendKeysFile = "backend.keys"
	journalSubdir   = "journal"
	snapshotVersion = 1
	// Root writes, the account's group reads, nobody else does anything. Group write would let a
	// daemon without root change what it listens on.
	notForOthers = 0o027
)

// snapshot is what root found out before the daemon started.
type snapshot struct {
	Version      int      `json:"version"`
	ConfigSHA256 string   `json:"config_sha256"`
	Name         string   `json:"name"`
	Listen       []string `json:"listen"`
	Hosts        []string `json:"hosts"`
}

// serviceAccount is the user and group the daemon runs as once it has lost root.
type serviceAccount struct{ uid, gid int }

func lookupAccount() (serviceAccount, error) {
	u, err := user.Lookup(accountName)
	if err != nil {
		return serviceAccount{}, err
	}
	g, err := user.LookupGroup(accountName)
	if err != nil {
		return serviceAccount{}, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return serviceAccount{}, err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return serviceAccount{}, err
	}
	return serviceAccount{uid: uid, gid: gid}, nil
}

// prepare does the root work and writes the snapshot last, so a daemon never finds a snapshot
// whose certificate is not there yet. A refusal is one line, which the init script hands to the
// system log, and leaves what was on disk.
func prepare(stderr io.Writer, e *env) int {
	say := func(f string, a ...any) { fmt.Fprintf(stderr, "llm-gateway: "+f+"\n", a...) }
	fail := func(f string, a ...any) int { say(f+"; not starting", a...); return 1 }

	acct, err := e.account()
	if err != nil {
		return fail("the account %s: %v", accountName, err)
	}
	if err := e.ownedDir(e.runDir, 0o750, 0, acct.gid); err != nil {
		return fail("%v", err)
	}
	// Two init scripts can run at once (a reload and an interface coming up). Before prepare
	// existed only procd's one instance ever made certificates; two of these making the first CA
	// together could leave the certificate of one beside the key of the other.
	unlock, err := lockDir(e.runDir)
	if err != nil {
		return fail("%v", err)
	}
	defer unlock()

	raw, err := os.ReadFile(e.configPath)
	if err != nil {
		return fail("%v", err)
	}
	// No resolver: prepare runs once, and DNS may not be up yet. The daemon checks every backend
	// at load and again on every connection (invariant 7), and procd retries it.
	cfg, err := ParseConfig(bytes.NewReader(raw), nil)
	if err != nil {
		return fail("%v", err)
	}
	if !cfg.Enabled {
		say("disabled; set llm-gateway.main.enabled=1")
		return 0
	}
	addrs, lerr := ListenAddrs(cfg.Zones, e.zones, cfg.Port, cfg.LoopbackPort)
	if addrs == nil {
		return fail("%v", lerr)
	}
	if lerr != nil {
		say("%v", lerr)
	}
	keys, err := backendKeyLines(cfg)
	if err != nil {
		return fail("%v", err)
	}
	name, err := houseName(cfg, e.dhcpPath)
	if err != nil {
		return fail("%v", err)
	}
	var ips []netip.Addr
	for _, a := range addrs {
		ips = append(ips, netip.MustParseAddrPort(a).Addr())
	}
	before := caFingerprint(cfg.CertDir)
	c, err := ensureCerts(cfg.CertDir, []string{name}, ips, e.now())
	if err != nil {
		return fail("certificates: %v", err)
	}
	switch before {
	case c.Fingerprint:
	case "":
		say("made the house CA for %s, fingerprint %s", name, c.Fingerprint)
	default:
		say("the CA no longer covers %s, so a new one was made and every device must trust it again: was %s, now %s", name, before, c.Fingerprint)
	}
	// The leaf's key is the one key the daemon needs. The CA's key stays root's alone.
	leafKey := filepath.Join(cfg.CertDir, leafKeyFile)
	if err := e.chown(leafKey, 0, acct.gid); err != nil {
		return fail("%v", err)
	}
	if err := os.Chmod(leafKey, 0o640); err != nil {
		return fail("%v", err)
	}
	jdir := filepath.Join(e.runDir, journalSubdir)
	if err := e.ownedDir(jdir, 0o700, acct.uid, acct.gid); err != nil {
		return fail("%v", err)
	}
	// A journal written while the daemon ran as root stays readable to it without root.
	base := filepath.Base(cfg.JournalPath)
	for _, f := range []string{base, base + ".1"} {
		if err := e.chown(filepath.Join(jdir, f), acct.uid, acct.gid); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fail("%v", err)
		}
	}
	own := func(tmp string) error { return e.chown(tmp, 0, acct.gid) }
	if err := writeFileOwned(filepath.Join(e.runDir, backendKeysFile), keys, 0o640, own); err != nil {
		return fail("%v", err)
	}
	sum := sha256.Sum256(raw)
	snap, err := json.Marshal(snapshot{Version: snapshotVersion, ConfigSHA256: hex.EncodeToString(sum[:]),
		Name: name, Listen: addrs, Hosts: hostsFor(name, addrs)})
	if err != nil {
		return fail("%v", err)
	}
	if err := writeFileOwned(filepath.Join(e.runDir, snapshotFile), append(snap, '\n'), 0o640, own); err != nil {
		return fail("%v", err)
	}
	return 0
}

// ownedDir makes sure path is a directory of its own with this owner and mode. A symbolic link in
// its place is refused rather than followed.
func (e *env) ownedDir(path string, mode os.FileMode, uid, gid int) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if err := e.chown(path, uid, gid); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// lockDir holds an exclusive lock on dir until the function it returns is called.
func lockDir(dir string) (func(), error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// houseName is the name the gateway goes by: <name>.<domain>. A domain that cannot be read is
// unknown, not "lan": guessing would replace the CA on every router whose domain is something else,
// and every device would lose its trust in it without a word. With no domain set, the CA the
// devices already trust keeps its name, and the default is for a router that has neither.
func houseName(cfg *Config, dhcpPath string) (string, error) {
	domain, err := dnsDomain(dhcpPath)
	if err != nil {
		return "", fmt.Errorf("the LAN domain cannot be read, and a guess could replace the CA: %w", err)
	}
	if domain != "" {
		return cfg.Name + "." + domain, nil
	}
	if ca, err := readCert(filepath.Join(cfg.CertDir, caCertFile)); err == nil && len(ca.PermittedDNSDomains) > 0 {
		return ca.PermittedDNSDomains[0], nil
	}
	return cfg.Name + ".lan", nil
}

// backendKeyLines copies the backends' keys, each in a file only root can read, into the lines of
// backend.keys: "<backend> <key>".
func backendKeyLines(cfg *Config) ([]byte, error) {
	var b bytes.Buffer
	for _, be := range cfg.Backends {
		if be.KeyFile == "" {
			continue
		}
		raw, err := os.ReadFile(be.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("backend %s: key_file: %w", be.Name, err)
		}
		key := strings.TrimSpace(string(raw))
		if strings.ContainsAny(key, "\r\n") {
			return nil, fmt.Errorf("backend %s: key_file holds more than one line", be.Name)
		}
		fmt.Fprintf(&b, "%s %s\n", be.Name, key)
	}
	return b.Bytes(), nil
}

// hostsFor is the Host values the gateway answers to, in the order of the snapshot's format.
func hostsFor(name string, addrs []string) []string {
	hosts := []string{name, "localhost"}
	for _, a := range addrs {
		hosts = append(hosts, netip.MustParseAddrPort(a).Addr().String())
	}
	return hosts
}

// keptFromOthers refuses a path that anyone but root and the account's group can touch, and a
// symbolic link, whose mode reads as open to all.
func keptFromOthers(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&notForOthers != 0 {
		return fmt.Errorf("%s has mode %o, open to more than root and its group", path, st.Mode().Perm())
	}
	return nil
}

// readSnapshot is the daemon's only source for where it listens and what it answers to. A snapshot
// that is missing, damaged, made from another configuration or open to others is refused: there is
// no fallback, because the fallback would need root. The checks on the addresses repeat invariant
// 4 on the way in, so a snapshot that names a wildcard or puts the plain loopback port on the LAN
// stops the start even though only root can write it.
func readSnapshot(dir string, configRaw []byte, cfg *Config) (*snapshot, error) {
	path := filepath.Join(dir, snapshotFile)
	for _, p := range []string{dir, path} {
		if err := keptFromOthers(p); err != nil {
			return nil, err
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s snapshot
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", snapshotFile, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: something follows the snapshot", snapshotFile)
	}
	if s.Version != snapshotVersion {
		return nil, fmt.Errorf("%s: version %d, want %d", snapshotFile, s.Version, snapshotVersion)
	}
	sum := sha256.Sum256(configRaw)
	if s.ConfigSHA256 != hex.EncodeToString(sum[:]) {
		return nil, fmt.Errorf("%s was made from another configuration", snapshotFile)
	}
	if s.Name == "" {
		return nil, fmt.Errorf("%s: no name", snapshotFile)
	}
	if err := checkListen(s.Listen, cfg.Port, cfg.LoopbackPort); err != nil {
		return nil, fmt.Errorf("%s: %w", snapshotFile, err)
	}
	if !slices.Equal(s.Hosts, hostsFor(s.Name, s.Listen)) {
		return nil, fmt.Errorf("%s: hosts do not match the name and the addresses", snapshotFile)
	}
	return &s, nil
}

// checkListen holds the snapshot's addresses to what ListenAddrs can produce: the TLS port on a
// house address, the plain port on 127.0.0.1 and nowhere else, and that one always present.
func checkListen(listen []string, port, loopbackPort int) error {
	loopback := false
	for _, a := range listen {
		ap, err := netip.ParseAddrPort(a)
		if err != nil {
			return fmt.Errorf("listen: %q is not an address and port", a)
		}
		addr := ap.Addr().Unmap()
		switch {
		case addr == netip.AddrFrom4([4]byte{127, 0, 0, 1}) && int(ap.Port()) == loopbackPort:
			loopback = true
		case inHouse(addr) && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() && int(ap.Port()) == port:
		default:
			return fmt.Errorf("listen: the gateway does not listen on %s", a)
		}
	}
	if !loopback {
		return fmt.Errorf("listen: 127.0.0.1:%d is missing", loopbackPort)
	}
	return nil
}

// loadBackendKeys gives each backend that names a key_file its key from backend.keys. An error
// names the backend or the line, never the key.
func loadBackendKeys(path string, cfg *Config) error {
	if err := keptFromOthers(path); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	keys := map[string]string{}
	for i, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		name, key, ok := strings.Cut(line, " ")
		if !ok {
			return fmt.Errorf("%s: line %d is not <backend> <key>", backendKeysFile, i+1)
		}
		keys[name] = key
	}
	for _, b := range cfg.Backends {
		if b.KeyFile == "" {
			continue
		}
		key, ok := keys[b.Name]
		if !ok {
			return fmt.Errorf("%s: no key for the backend %s", backendKeysFile, b.Name)
		}
		b.key = key
	}
	return nil
}
