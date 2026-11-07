#!/usr/bin/env python3
"""mutate.py -- break the product code where a wrong answer would be silent, and require a named
test to catch each break.

Layer 7 of the house testing rule, for the T3 invariants: the key (1), the client's models (2),
the browser (3), where it listens (4), what the journal keeps (5), nothing leaves the house (7),
the house CA's reach and who may change it (14), what the daemon works from (15), who can change
the snapshot (16), the ports (18), the daemon's refusal of root and its filter (20, 21), and the
limits. Each mutant is applied to a fresh copy of the tree; the
named tests must FAIL on it. A mutant whose text is not found exactly once stops the run, so a
refactor cannot quietly turn a mutant into a no-op.

  python3 scripts/mutate.py            every mutant
  python3 scripts/mutate.py NAME ...   just those
"""
import os, re, shutil, subprocess, sys, tempfile

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# name, file, original text, mutated text, tests that must catch it
MUTANTS = [
    ("empty-key-matches-first-client", "keys.go",
     'if key == "" {\n\t\treturn nil\n\t}',
     'if key == "" && len(clients) > 0 {\n\t\treturn clients[0]\n\t}',
     ["TestRequestWithoutKeyIsRefusedBeforeAnyBackend"]),
    ("any-key-matches", "keys.go",
     "subtle.ConstantTimeCompare(sum[:], stored) == 1 && found == nil",
     "subtle.ConstantTimeCompare(sum[:], stored) >= 0 && found == nil",
     ["TestWrongKeyIsRefused"]),
    ("loopback-is-trusted", "gateway.go",
     "\tc := clientForKey(g.cfg.Clients, key)\n",
     "\tc := clientForKey(g.cfg.Clients, key)\n\tif c == nil && remoteIP(r.RemoteAddr) == \"127.0.0.1\" && len(g.cfg.Clients) > 0 {\n\t\tc = g.cfg.Clients[0]\n\t}\n",
     ["TestLoopbackRequestStillNeedsAKey"]),
    ("host-not-checked", "gateway.go",
     "if !g.hosts[normalHost(r.Host)] {",
     "if false && !g.hosts[normalHost(r.Host)] {",
     ["TestForeignHostIsRefused"]),
    ("origin-not-checked", "gateway.go",
     "if !c.listsOrigin(origin) {",
     "if false && !c.listsOrigin(origin) {",
     ["TestUnlistedOriginIsRefused"]),
    ("preflight-allows-any-origin", "gateway.go",
     'if origin != "" && c.listsOrigin(origin) {',
     'if origin != "" && c != nil {',
     ["TestUnlistedOriginIsRefused"]),
    ("backend-cors-passes-through", "gateway.go",
     'if strings.HasPrefix(strings.ToLower(k), "access-control-") {',
     'if strings.HasPrefix(strings.ToLower(k), "access-control-nothing-") {',
     ["TestNoResponseCarriesAWildcardOrigin"]),
    ("model-not-checked", "gateway.go",
     "if b == nil || !c.allows(req.Model) {",
     "if b == nil {",
     ["TestClientReachesOnlyItsModels"]),
    ("model-list-unfiltered", "gateway.go",
     "\t\t\tif c.allows(m) {",
     "\t\t\tif true || c.allows(m) {",
     ["TestModelListShowsOnlyTheClientsModels"]),
    ("star-not-honoured", "config.go",
     'return slices.Contains(c.Models, "*") || slices.Contains(c.Models, model)',
     'return slices.Contains(c.Models, model)',
     ["TestModelListShowsOnlyTheClientsModels"]),
    ("dial-not-checked", "gateway.go",
     "\t\tif !ok || !inHouse(a) {\n\t\t\treturn nil, fmt.Errorf(\"refused:",
     "\t\tif !ok || !a.IsValid() {\n\t\t\treturn nil, fmt.Errorf(\"refused:",
     ["TestBackendNameResolvingOutsideIsRefusedAtDial"]),
    ("load-not-checked", "config.go",
     "\t\tif !ok || !inHouse(a) {\n\t\t\treturn fmt.Errorf(\"host %s resolves to",
     "\t\tif !ok || !a.IsValid() {\n\t\t\treturn fmt.Errorf(\"host %s resolves to",
     ["TestPublicBackendAddressIsRefusedAtLoad"]),
    ("public-counts-as-house", "config.go",
     "(a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast())",
     "(a.IsGlobalUnicast() || a.IsLoopback() || a.IsLinkLocalUnicast())",
     ["TestPublicBackendAddressIsRefusedAtLoad", "TestBackendNameResolvingOutsideIsRefusedAtDial"]),
    ("wildcard-zone-address-bound", "listen.go",
     "\t\t\t\tif a.IsUnspecified() {",
     "\t\t\t\tif false && a.IsUnspecified() {",
     ["TestListensOnlyOnNamedZoneAddressesAndLoopback"]),
    ("public-zone-address-bound", "listen.go",
     "if !inHouse(a) || a.IsLinkLocalUnicast() || a.IsLoopback() {",
     "if a.IsLinkLocalUnicast() || a.IsLoopback() {",
     ["TestOpenWrtZonesReadTheFirewallAndNetifd"]),
    ("wan-zone-accepted", "listen.go",
     'if n == "wan" || n == "wan6" {',
     'if false {',
     ["TestListensOnlyOnNamedZoneAddressesAndLoopback"]),
    ("journal-keeps-the-offered-key", "gateway.go",
     '\t\te.Reason = "bad key"\n',
     '\t\te.Reason = "bad key " + key\n',
     ["TestJournalRecordsARefusalWithoutTheKey"]),
    ("client-key-forwarded-upstream", "gateway.go",
     'range []string{"Authorization", "Origin",',
     'range []string{"Origin",',
     ["TestUpstreamKeyReplacesTheClientsKey"]),
    ("stream-buffered", "usage.go",
     "\tn, err := s.r.Read(p)\n",
     "\tif !s.done && s.stream {\n\t\tall, _ := io.ReadAll(s.r)\n\t\ts.r = io.NopCloser(bytes.NewReader(all))\n\t\ts.done = false\n\t}\n\tn, err := s.r.Read(p)\n",
     ["TestStreamingPassesThroughAndIsRecorded"]),
    ("ca-not-name-constrained", "tls.go",
     "\t\tPermittedDNSDomainsCritical: true,\n\t\tPermittedDNSDomains:         slices.Clone(names),\n\t\tPermittedIPRanges:           ranges,\n",
     "\t\tPermittedDNSDomainsCritical: true,\n",
     ["TestHouseCACannotVouchForOtherNames"]),
    ("concurrency-not-held", "ratelimit.go",
     "if c.Concurrent > 0 && b.inFlight >= c.Concurrent {",
     "if false && c.Concurrent > 0 && b.inFlight >= c.Concurrent {",
     ["TestClientConcurrencyIsHeld"]),
    ("rate-not-held", "ratelimit.go",
     "\t\tif b.tokens < 1 {",
     "\t\tif false && b.tokens < 1 {",
     ["TestClientRateIsHeldPerMinute"]),
    ("key-stored-in-clear", "main.go",
     "p, p, HashKey(key))",
     "p, p, key)",
     ["TestClientAddShowsTheKeyOnceAndStoresOnlyItsHash"]),
    ("models-zone-takes-a-zoned-network", "main.go",
     "\t\t\tif n == network {\n\t\t\t\treturn fail(",
     "\t\t\tif false && n == network {\n\t\t\t\treturn fail(",
     ["TestModelsZoneRefusesANetworkThatIsAlreadyZoned"]),
    ("models-zone-forwards-to-wan", "main.go",
     "\tif *internet {\n\t\tw(\"set firewall.llm_models_wan='forwarding'\")",
     "\tif true || *internet {\n\t\tw(\"set firewall.llm_models_wan='forwarding'\")",
     ["TestModelsZoneIsIsolatedByConstruction"]),
    ("lan-port-plain-http", "main.go",
     "\t\t\tln = tls.NewListener(ln, serverTLSConfig(leaf))\n",
     "\t\t\tif false {\n\t\t\t\tln = tls.NewListener(ln, serverTLSConfig(leaf))\n\t\t\t}\n",
     ["TestServeBindsOnlyTheZoneAddressesAndLoopback"]),
    # The CA guard (invariant 14).
    ("daemon-creates-ca", "main.go",
     "\tleaf, err := loadLeaf(cfg.CertDir)\n",
     "\tif _, _, cerr := loadPair(cfg.CertDir, \"ca.crt\", \"ca.key\"); cerr != nil {\n"
     "\t\tnewCA(cfg.CertDir, []string{snap.Name}, e.now())\n\t}\n"
     "\tleaf, err := loadLeaf(cfg.CertDir)\n",
     ["TestDaemonLeavesTheCAAlone", "TestServeNeverExecs"]),
    ("daemon-renews-the-leaf", "main.go",
     "\tleaf, err := loadLeaf(cfg.CertDir)\n",
     "\tensureCerts(cfg.CertDir, []string{snap.Name}, nil, e.now())\n\tleaf, err := loadLeaf(cfg.CertDir)\n",
     ["TestDaemonDoesNotRepairCertificates", "TestServeNeverExecs"]),
    ("domain-read-failure-reads-as-lan", "main.go",
     "\tif err != nil {\n\t\treturn \"\", err\n\t}\n\tfor _, s := range secs {\n\t\tif s.Type == \"dnsmasq\"",
     "\tif err != nil {\n\t\treturn \"lan\", nil\n\t}\n\tfor _, s := range secs {\n\t\tif s.Type == \"dnsmasq\"",
     ["TestPrepareKeepsTheCA"]),
    ("half-read-file-reads-as-short", "main.go",
     "\tif err := sc.Err(); err != nil {\n\t\treturn nil, fmt.Errorf(\"%s: %w\", path, err)\n\t}\n\treturn secs, nil\n",
     "\treturn secs, nil\n",
     ["TestPrepareKeepsTheCA"]),
    ("no-domain-reads-as-lan", "prepare.go",
     "\tif ca, err := readCert(filepath.Join(cfg.CertDir, caCertFile)); err == nil && len(ca.PermittedDNSDomains) > 0 {\n"
     "\t\treturn ca.PermittedDNSDomains[0], nil\n\t}\n",
     "",
     ["TestPrepareKeepsTheCA"]),
    ("damaged-ca-replaced", "tls.go",
     "\tif errors.Is(certErr, fs.ErrNotExist) && errors.Is(keyErr, fs.ErrNotExist) {\n\t\treturn nil, nil, nil\n\t}\n",
     "\tif errors.Is(certErr, fs.ErrNotExist) || errors.Is(keyErr, fs.ErrNotExist) || true {\n\t\treturn nil, nil, nil\n\t}\n",
     ["TestPrepareRefusesADamagedCA"]),
    ("lone-ca-key-replaced", "tls.go",
     "\tif errors.Is(certErr, fs.ErrNotExist) && errors.Is(keyErr, fs.ErrNotExist) {\n",
     "\tif errors.Is(certErr, fs.ErrNotExist) || errors.Is(keyErr, fs.ErrNotExist) {\n",
     ["TestPrepareRefusesADamagedCA"]),
    ("new-ca-goes-unsaid", "prepare.go",
     "\t\tsay(\"the CA no longer covers %s, so a new one was made and every device must trust it again: was %s, now %s\", name, before, c.Fingerprint)\n",
     "",
     ["TestPrepareSaysWhenItMakesACA"]),
    ("ca-key-given-to-the-group", "prepare.go",
     "\tleafKey := filepath.Join(cfg.CertDir, leafKeyFile)\n",
     "\tleafKey := filepath.Join(cfg.CertDir, caKeyFile)\n",
     ["TestPrepareGivesOnlyTheLeafKeyToTheGroup"]),
    ("prepare-does-not-lock", "prepare.go",
     "\tif err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {\n\t\tf.Close()\n\t\treturn nil, err\n\t}\n",
     "\t_ = syscall.Flock\n",
     ["TestPrepareWaitsForAnotherPrepare"]),
    # The snapshot read (invariant 15).
    ("no-config-hash-check", "prepare.go",
     "\tif s.ConfigSHA256 != hex.EncodeToString(sum[:]) {\n",
     "\tif false && s.ConfigSHA256 != hex.EncodeToString(sum[:]) {\n",
     ["TestServeRefusesAStaleSnapshot"]),
    ("missing-snapshot-reads-as-loopback", "main.go",
     "\tsnap, err := readSnapshot(e.runDir, raw, cfg)\n\tif err != nil {\n",
     "\tsnap, err := readSnapshot(e.runDir, raw, cfg)\n"
     "\tif errors.Is(err, fs.ErrNotExist) {\n"
     "\t\tsnap, err = &snapshot{Name: \"llm.lan\", Listen: []string{\"127.0.0.1:\" + strconv.Itoa(cfg.LoopbackPort)}, Hosts: []string{\"llm.lan\"}}, nil\n"
     "\t}\n\tif err != nil {\n",
     ["TestServeRefusesAMissingSnapshot"]),
    ("snapshot-version-not-checked", "prepare.go",
     "\tif s.Version != snapshotVersion {\n",
     "\tif false && s.Version != snapshotVersion {\n",
     ["TestServeRefusesADamagedSnapshot"]),
    ("snapshot-unknown-fields-pass", "prepare.go",
     "\tdec.DisallowUnknownFields()\n",
     "",
     ["TestServeRefusesADamagedSnapshot"]),
    ("snapshot-trailing-data-passes", "prepare.go",
     "\tif _, err := dec.Token(); !errors.Is(err, io.EOF) {\n",
     "\tif _, err := dec.Token(); false && !errors.Is(err, io.EOF) {\n",
     ["TestServeRefusesADamagedSnapshot"]),
    ("snapshot-hosts-not-checked", "prepare.go",
     "\tif !slices.Equal(s.Hosts, hostsFor(s.Name, s.Listen)) {\n",
     "\tif false && !slices.Equal(s.Hosts, hostsFor(s.Name, s.Listen)) {\n",
     ["TestServeRefusesADamagedSnapshot"]),
    ("snapshot-listen-not-checked", "prepare.go",
     "\tif err := checkListen(s.Listen, cfg.Port, cfg.LoopbackPort); err != nil {\n",
     "\tif err := checkListen(s.Listen, cfg.Port, cfg.LoopbackPort); false && err != nil {\n",
     ["TestServeRefusesASnapshotThatListensOutside"]),
    ("snapshot-public-address-listened-on", "prepare.go",
     "\t\tcase inHouse(addr) && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() && int(ap.Port()) == port:\n",
     "\t\tcase !addr.IsUnspecified() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() && int(ap.Port()) == port:\n",
     ["TestServeRefusesASnapshotThatListensOutside"]),
    ("plain-port-allowed-on-the-lan", "prepare.go",
     "int(ap.Port()) == port:\n",
     "(int(ap.Port()) == port || int(ap.Port()) == loopbackPort):\n",
     ["TestServeRefusesASnapshotThatListensOutside"]),
    ("daemon-calls-netifd", "main.go",
     "\tgw, err := NewGateway(cfg, Options{Hosts: snap.Hosts,",
     "\tListenAddrs(cfg.Zones, newOpenWrtZones(), cfg.Port, cfg.LoopbackPort)\n\tgw, err := NewGateway(cfg, Options{Hosts: snap.Hosts,",
     ["TestServeNeverExecs"]),
    ("backend-key-read-from-its-file", "main.go",
     "\tif err := loadBackendKeys(filepath.Join(e.runDir, backendKeysFile), cfg); err != nil {\n",
     "\tif err := loadBackendKeys(filepath.Join(e.runDir, backendKeysFile), cfg); false && err != nil {\n",
     ["TestBackendKeysFileIsReadWithCare"]),
    # Who can change the snapshot (invariant 16).
    ("snapshot-world-readable", "prepare.go",
     "filepath.Join(e.runDir, snapshotFile), append(snap, '\\n'), 0o640, own)",
     "filepath.Join(e.runDir, snapshotFile), append(snap, '\\n'), 0o644, own)",
     ["TestPrepareWritesTheSnapshotAndTheKeys"]),
    ("snapshot-owned-by-the-account", "prepare.go",
     "own := func(tmp string) error { return e.chown(tmp, 0, acct.gid) }",
     "own := func(tmp string) error { return e.chown(tmp, acct.uid, acct.gid) }",
     ["TestPrepareWritesTheSnapshotAndTheKeys"]),
    ("snapshot-directory-open", "prepare.go",
     "e.ownedDir(e.runDir, 0o750, 0, acct.gid)",
     "e.ownedDir(e.runDir, 0o775, 0, acct.gid)",
     ["TestPrepareOwnsTheSnapshotDirectory"]),
    ("snapshot-directory-owned-by-the-account", "prepare.go",
     "e.ownedDir(e.runDir, 0o750, 0, acct.gid)",
     "e.ownedDir(e.runDir, 0o750, acct.uid, acct.gid)",
     ["TestPrepareOwnsTheSnapshotDirectory"]),
    ("wrong-owner-still-takes-the-name", "tls.go",
     "\t\tif err := own(tmp.Name()); err != nil {\n\t\t\treturn err\n\t\t}\n",
     "\t\t_ = own(tmp.Name())\n",
     ["TestPrepareStopsWhenAFileCannotGetItsOwner"]),
    ("leaf-key-stays-root-only", "prepare.go",
     "\tif err := os.Chmod(leafKey, 0o640); err != nil {\n\t\treturn fail(\"%v\", err)\n\t}\n",
     "",
     ["TestPrepareGivesOnlyTheLeafKeyToTheGroup"]),
    ("snapshot-mode-not-read", "prepare.go",
     "\tif st.Mode().Perm()&notForOthers != 0 {\n",
     "\tif false && st.Mode().Perm()&notForOthers != 0 {\n",
     ["TestServeRefusesASnapshotOthersCanChange"]),
    ("link-in-the-directorys-place-followed", "prepare.go",
     "\tif !st.IsDir() {\n\t\treturn fmt.Errorf(\"%s is not a directory\", path)\n\t}\n",
     "\tif false && !st.IsDir() {\n\t\treturn fmt.Errorf(\"%s is not a directory\", path)\n\t}\n",
     ["TestPrepareOwnsTheSnapshotDirectory"]),
    # The journal and the ports.
    ("journal-under-tmp", "config.go",
     'JournalPath: journalDir + "/journal.log",',
     'JournalPath: "/tmp/llm-gateway/journal.log",',
     ["TestJournalLivesUnderVarRun"]),
    ("journal-anywhere", "config.go",
     'if filepath.Dir(v) != journalDir || filepath.Clean(v) != v {',
     'if false && (filepath.Dir(v) != journalDir || filepath.Clean(v) != v) {',
     ["TestJournalLivesUnderVarRun"]),
    ("port-below-1024-accepted", "config.go",
     "if n < minPort {",
     "if false && n < minPort {",
     ["TestPortBelow1024IsRefused"]),
    ("port-1024-refused", "config.go",
     "if n < minPort {",
     "if n <= minPort {",
     ["TestPortBelow1024IsRefused"]),
    ("renewal-window-ignored", "tls.go",
     "leaf.NotAfter.Sub(now) >= leafRenewal",
     "leaf.NotAfter.After(now)",
     ["TestRenewDueSaysWhenTheCertificateNearsItsEnd"]),
    # The daemon without root (invariants 19 to 21).
    ("serve-as-root-allowed", "main.go",
     "\tif e.euid() == 0 {\n",
     "\tif false && e.euid() == 0 {\n",
     ["TestServeRefusesRoot"]),
    ("filter-failure-ignored", "main.go",
     "\t\tlg.Printf(\"refusing to run without its seccomp filter: %v\", err)\n\t\treturn false\n",
     "\t\tlg.Printf(\"refusing to run without its seccomp filter: %v\", err)\n\t\treturn true\n",
     ["TestServeRefusesWithoutItsFilter"]),
    ("filter-unseen-still-listens", "main.go",
     "\t\tlg.Printf(\"refusing to listen: %v\", err)\n\t\treturn false\n",
     "\t\tlg.Printf(\"refusing to listen: %v\", err)\n\t\treturn true\n",
     ["TestServeNeedsTheKernelToShowItsFilter"]),
    ("status-never-read", "main.go",
     "\t\terr = sandbox.CheckStatus(status)\n",
     "\t\t_ = status\n",
     ["TestServeNeedsTheKernelToShowItsFilter"]),
    ("filter-off-goes-unsaid", "main.go",
     "\t\tlg.Printf(\"no seccomp filter on linux/%s: it goes on only where a real run stands behind the list\", runtime.GOARCH)\n",
     "\t\t_ = runtime.GOARCH\n",
     ["TestServeSaysWhereItsFilterStaysOff"]),
    ("filter-after-the-configuration", "main.go",
     "\tif !confined(e, lg) {\n\t\treturn 1\n\t}\n\traw, err := os.ReadFile(e.configPath)\n\tif err != nil {\n\t\tlg.Print(err)\n\t\treturn 1\n\t}\n",
     "\traw, err := os.ReadFile(e.configPath)\n\tif err != nil {\n\t\tlg.Print(err)\n\t\treturn 1\n\t}\n\tif !confined(e, lg) {\n\t\treturn 1\n\t}\n",
     ["TestConfineComesFirst"]),
    ("upgrade-passes-through", "gateway.go",
     '"X-Real-Ip", "Upgrade", "Connection"}',
     '"X-Real-Ip"}',
     ["TestUpgradeNeverReachesABackend"]),
    ("no-tsync", "internal/sandbox/sandbox_linux.go",
     "seccompFilterFlagTSYNC = 1\n",
     "seccompFilterFlagTSYNC = 0\n",
     ["TestFilterCoversEveryThread"]),
    ("thread-left-out-passes", "internal/sandbox/sandbox_linux.go",
     "\tcase r != 0:\n",
     "\tcase false && r != 0:\n",
     ["TestSeccompResult"]),
    ("no-new-privs-not-set", "internal/sandbox/sandbox_linux.go",
     "syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0)",
     "syscall.SYS_PRCTL, prSetNoNewPrivs, 0, 0, 0, 0, 0)",
     ["TestFilterAllowsListedCalls"]),
    ("execve-in-the-list", "internal/sandbox/seccomp/common.txt",
     "\naccept4\n",
     "\naccept4\nexecve\n",
     ["TestListNeverAllowsAProgram", "TestFilterKillsExec"]),
    ("list-misses-a-needed-call", "internal/sandbox/seccomp/common.txt",
     "\nrenameat\n",
     "\n",
     ["TestListHoldsWhatEveryRunNeeds"]),
    ("status-takes-strict-mode", "internal/sandbox/list.go",
     'case seccomp != "2":',
     'case seccomp == "":',
     ["TestCheckStatus"]),
    ("unlisted-calls-pass", "internal/sandbox/filter.go",
     "\t\tInsn{Code: bpfRET | bpfK, K: retKillProcess},\n\t\tInsn{Code: bpfRET | bpfK, K: retAllow},\n",
     "\t\tInsn{Code: bpfRET | bpfK, K: retAllow},\n\t\tInsn{Code: bpfRET | bpfK, K: retAllow},\n",
     ["TestFilterAllowsOnlyTheList", "TestFilterIsRollcallsCopy"]),
    ("another-architecture-passes", "internal/sandbox/filter.go",
     "\t\t{Code: bpfRET | bpfK, K: retKillProcess},\n\t\t{Code: bpfLD | bpfW | bpfABS, K: offNr},\n",
     "\t\t{Code: bpfRET | bpfK, K: retAllow},\n\t\t{Code: bpfLD | bpfW | bpfABS, K: offNr},\n",
     ["TestFilterAllowsOnlyTheList", "TestFilterIsRollcallsCopy"]),
]


def copy_tree(dst):
    for name in os.listdir(REPO):
        if name.endswith(".go") or name in ("go.mod", "go.sum"):
            shutil.copy2(os.path.join(REPO, name), dst)
    shutil.copytree(os.path.join(REPO, "internal"), os.path.join(dst, "internal"))


def go_test(cwd, tests):
    pattern = "^(" + "|".join(tests) + ")$"
    r = subprocess.run(["go", "test", "-count=1", "-timeout", "60s", "-run", pattern, "./..."],
                       cwd=cwd, capture_output=True, text=True)
    return r.returncode, r.stdout + r.stderr


def main(names):
    chosen = [m for m in MUTANTS if not names or m[0] in names]
    if not chosen:
        print("FAIL: measured nothing: no mutant selected")
        return 1
    with tempfile.TemporaryDirectory() as clean:
        copy_tree(clean)
        rc, out = go_test(clean, sorted({t for m in chosen for t in m[4]}))
        if rc != 0:
            print("FAIL: the named tests do not pass on the clean tree:\n" + out[-2000:])
            return 1
    caught = 0
    for name, path, old, new, tests in chosen:
        with tempfile.TemporaryDirectory() as d:
            copy_tree(d)
            f = os.path.join(d, path)
            src = open(f).read()
            if src.count(old) != 1:
                print(f"FAIL: mutant {name}: its text occurs {src.count(old)} times in {path}, want 1")
                return 1
            mutated = src.replace(old, new)
            if name == "stream-buffered" and '"bytes"' not in mutated:
                mutated = mutated.replace('import (\n', 'import (\n\t"bytes"\n', 1)
            open(f, "w").write(mutated)
            rc, out = go_test(d, tests)
            if rc == 0:
                print(f"SURVIVED  {name:34s} {path}: {', '.join(tests)} still pass")
            elif re.search(r"\[build failed\]|cannot use|undefined:|syntax error", out):
                print(f"BROKEN    {name:34s} the mutant does not compile:\n{out[-800:]}")
                return 1
            else:
                caught += 1
                print(f"caught    {name:34s} by {', '.join(tests)}")
    print(f"mutants: {caught} of {len(chosen)} caught")
    return 0 if caught == len(chosen) else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
