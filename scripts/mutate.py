#!/usr/bin/env python3
"""mutate.py -- break the product code where a wrong answer would be silent, and require a named
test to catch each break.

Layer 7 of the house testing rule, for the T3 invariants: the key (1), the client's models (2),
the browser (3), where it listens (4), what the journal keeps (5), nothing leaves the house (7),
the house CA's reach, and the limits. Each mutant is applied to a fresh copy of the tree; the
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
     "\t\t\tln = tls.NewListener(ln, serverTLSConfig(c))\n",
     "\t\t\tif false {\n\t\t\t\tln = tls.NewListener(ln, serverTLSConfig(c))\n\t\t\t}\n",
     ["TestServeBindsOnlyTheZoneAddressesAndLoopback"]),
]


def copy_tree(dst):
    for name in os.listdir(REPO):
        if name.endswith(".go") or name in ("go.mod", "go.sum"):
            shutil.copy2(os.path.join(REPO, name), dst)


def go_test(cwd, tests):
    pattern = "^(" + "|".join(tests) + ")$"
    r = subprocess.run(["go", "test", "-count=1", "-timeout", "60s", "-run", pattern, "."],
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
