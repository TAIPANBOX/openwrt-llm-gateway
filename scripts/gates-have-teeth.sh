#!/bin/sh
# gates-have-teeth.sh -- each gate goes red on its own planted fault, stays green on the clean
# tree, and says "measured nothing" when its subject is gone. A gate that cannot fail is ceremony.
set -eu
REPO=$(cd "$(dirname "$0")/.." && pwd)
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
# A copy of the tree, made a git checkout of its own: TestNoPrivateKeyCommittable reads git's list.
fresh() { rm -rf "$TMP/r"; mkdir -p "$TMP/r"; (cd "$REPO" && tar cf - --exclude .git --exclude docs --exclude ./build --exclude ./feed-out . ) | (cd "$TMP/r" && tar xf - && git -c init.defaultBranch=main init -q); }
pass=0; bad=0
expect() { # want(red|red:<text it must say>|green|nothing) name command...
	want=$1 name=$2; shift 2
	out=$("$@" 2>&1) && rc=0 || rc=$?
	case $want in
	green)   [ $rc = 0 ] && ok=1 || ok=0 ;;
	red)     [ $rc != 0 ] && ! echo "$out" | grep -q 'measured nothing' && ok=1 || ok=0 ;;
	red:*)   [ $rc != 0 ] && echo "$out" | grep -qF -- "${want#red:}" && ok=1 || ok=0 ;;
	nothing) [ $rc != 0 ] && echo "$out" | grep -q 'measured nothing' && ok=1 || ok=0 ;;
	esac
	if [ $ok = 1 ]; then pass=$((pass+1)); echo "ok   $name"; else bad=$((bad+1)); echo "BAD  $name (rc=$rc)"; echo "$out" | tail -5; fi
}
B="sh scripts/gate-scenarios-bound.sh"
W="sh scripts/gate-no-wildcard.sh"

fresh; expect green "bound: clean tree" sh -c "cd $TMP/r && $B"
fresh; perl -i -ne 'print unless /^\s*# bound to: TestWrongKeyIsRefused$/' "$TMP/r/features/access.feature"
grep -q 'TestWrongKeyIsRefused' "$TMP/r/features/access.feature" && { echo "BAD  the unbinding plant did not apply"; exit 1; }
expect red "bound: a scenario loses its binding" sh -c "cd $TMP/r && BOUND_SKIP_RUN=1 $B"
fresh; sed -i.bak 's/# bound to: TestWrongKeyIsRefused/# bound to: TestThatDoesNotExist/' "$TMP/r/features/access.feature"
expect red "bound: a binding names no test" sh -c "cd $TMP/r && BOUND_SKIP_RUN=1 $B"
fresh; sed -i.bak 's/# bound to vlab: models_zone_has_no_internet/# bound to vlab: no_such_check/' "$TMP/r/features/reach.feature"
expect red "bound: a vlab binding names no check" sh -c "cd $TMP/r && BOUND_SKIP_RUN=1 $B"
fresh; perl -i -pe 's/^(func TestWrongKeyIsRefused\(t \*testing.T\) \{)$/$1\n\tt.Fatal("planted")/' "$TMP/r/access_test.go"
grep -q 't.Fatal("planted")' "$TMP/r/access_test.go" || { echo "BAD  the failing-test plant did not apply"; exit 1; }
expect red "bound: a bound test fails" sh -c "cd $TMP/r && $B"
fresh; rm -f "$TMP/r"/features/*.feature
expect nothing "bound: no feature files" sh -c "cd $TMP/r && $B"

fresh; expect green "wildcard: clean tree" sh -c "cd $TMP/r && $W"
for plant in 'var _ = "0.0.0.0:8760"' 'var _ = "[::]:8760"' 'var _ = func() { http.ListenAndServe(":8760", nil) }' \
	'var _ = &http.Server{Addr: ":8760"}' 'var _ = func(w http.ResponseWriter) { w.Header().Set("Access-Control-Allow-Origin", "*") }'; do
	fresh; printf 'package main\n\nimport "net/http"\n\nvar _ = http.StatusOK\n%s\n' "$plant" > "$TMP/r/planted.go"
	expect red "wildcard: plant $plant" sh -c "cd $TMP/r && $W"
done
fresh; printf 'package main\n\nvar _ = "0.0.0.0"\n' > "$TMP/r/planted_test.go"
expect green "wildcard: a test file may name it" sh -c "cd $TMP/r && $W"
fresh; rm -f "$TMP/r"/*.go
expect nothing "wildcard: no source" sh -c "cd $TMP/r && $W"

# The feed's tree: a feed of the right shape with dummy bytes is all gate-feed-tree.sh reads. Its
# signatures are gate-feed.sh's, whose teeth need forge (scripts/teeth-feed.sh).
FT="sh $REPO/scripts/gate-feed-tree.sh"
GF="sh $REPO/scripts/gate-feed.sh"
fakefeed() {
	rm -rf "$TMP/feed"; mkdir -p "$TMP/feed"
	touch "$TMP/feed/.nojekyll"; echo page > "$TMP/feed/index.html"; cp "$REPO/keys/llm-gateway.pem" "$TMP/feed/"
	for a in $(sed -n 's/.* apk: \([a-z0-9_-]*\) }.*/\1/p' "$REPO/.github/workflows/ci.yml"); do
		mkdir -p "$TMP/feed/25.12/$a"; echo index > "$TMP/feed/25.12/$a/packages.adb"; echo pkg > "$TMP/feed/25.12/$a/llm-gateway-0.1.0-r2.apk"
	done
}
throwaway() { openssl ecparam -name "$1" -genkey -noout 2>/dev/null; }
fakefeed; expect green "feed tree: clean" sh -c "FEED=$TMP/feed $FT"
fakefeed; touch "$TMP/feed/25.12/x86_64/.DS_Store"
expect "red:FAIL check_only_feed_files: a .DS_Store" "feed tree: a .DS_Store" sh -c "FEED=$TMP/feed $FT"
fakefeed; echo notes > "$TMP/feed/notes.txt"
expect "red:FAIL check_only_feed_files: files a feed does not carry: ./notes.txt" "feed tree: a stray file" sh -c "FEED=$TMP/feed $FT"
fakefeed; ln -s ../../index.html "$TMP/feed/25.12/x86_64/llm-gateway-0.1.0-r3.apk"
expect "red:FAIL check_only_feed_files: symbolic links" "feed tree: a symbolic link" sh -c "FEED=$TMP/feed $FT"
fakefeed; rm "$TMP/feed/.nojekyll"
expect "red:FAIL check_nojekyll" "feed tree: no .nojekyll" sh -c "FEED=$TMP/feed $FT"
fakefeed; throwaway prime256v1 | openssl ec -pubout -out "$TMP/feed/llm-gateway.pem" 2>/dev/null
expect "red:FAIL check_published_key: the feed's llm-gateway.pem is not" "feed tree: another key published" sh -c "FEED=$TMP/feed $FT"
fakefeed; rm -rf "$TMP/feed/25.12/mipsel_24kc"
expect "red:FAIL check_architectures: architectures" "feed tree: an architecture missing" sh -c "FEED=$TMP/feed $FT"
fakefeed; mkdir -p "$TMP/feed/25.12/mips_24kc"; echo index > "$TMP/feed/25.12/mips_24kc/packages.adb"; echo pkg > "$TMP/feed/25.12/mips_24kc/llm-gateway-0.1.0-r2.apk"
expect "red:FAIL check_architectures: architectures" "feed tree: an architecture CI does not build" sh -c "FEED=$TMP/feed $FT"
fakefeed; echo pkg > "$TMP/feed/25.12/x86_64/llm-gateway-0.1.0-r3.apk"
expect "red:FAIL check_architectures: x86_64 holds" "feed tree: two packages in one architecture" sh -c "FEED=$TMP/feed $FT"
fakefeed; throwaway prime256v1 >> "$TMP/feed/index.html"
expect "red:FAIL check_no_private_key" "feed tree: a private key in the feed" sh -c "FEED=$TMP/feed $FT"
rm -rf "$TMP/feed"; expect nothing "feed tree: no feed" sh -c "FEED=$TMP/feed $FT"
# gate-feed.sh runs the tree first, and stops there without reaching forge
fakefeed; touch "$TMP/feed/25.12/x86_64/.DS_Store"
expect "red:FAIL check_only_feed_files: a .DS_Store" "feed: a tree fault stops gate-feed.sh" sh -c "FEED=$TMP/feed $GF"
rm -rf "$TMP/feed"; expect nothing "feed: no feed for gate-feed.sh" sh -c "FEED=$TMP/feed $GF"

# The feed's key in git: the public half's shape, and no private key committable
KT() { echo "cd $TMP/r && go test -count=1 -run '^$1\$' ."; }
fresh; expect green "feed key: the public key" sh -c "$(KT TestFeedPublicKey)"
fresh; throwaway prime256v1 > "$TMP/r/keys/llm-gateway.pem"
expect "red:not PUBLIC KEY" "feed key: the private half in its place" sh -c "$(KT TestFeedPublicKey)"
fresh; throwaway secp384r1 | openssl ec -pubout -out "$TMP/r/keys/llm-gateway.pem" 2>/dev/null
expect "red:curve P-384" "feed key: another curve" sh -c "$(KT TestFeedPublicKey)"
fresh; cat "$REPO/keys/llm-gateway.pem" >> "$TMP/r/keys/llm-gateway.pem"
expect "red:more than one PEM block" "feed key: a second block" sh -c "$(KT TestFeedPublicKey)"
fresh; rm "$TMP/r/keys/llm-gateway.pem"; expect nothing "feed key: no public key" sh -c "$(KT TestFeedPublicKey)"
fresh; expect green "private key: a clean checkout" sh -c "$(KT TestNoPrivateKeyCommittable)"
fresh; mkdir -p "$TMP/r/docs"; throwaway prime256v1 > "$TMP/r/docs/signing-key.txt"
expect "red:docs/signing-key.txt: holds a private key" "private key: under another name" sh -c "$(KT TestNoPrivateKeyCommittable)"
fresh; throwaway prime256v1 > "$TMP/r/keys/llm-gateway.private.pem"; (cd "$TMP/r" && git add -f keys/llm-gateway.private.pem)
expect "red:keys/llm-gateway.private.pem: a private key file is tracked" "private key: force-added" sh -c "$(KT TestNoPrivateKeyCommittable)"
fresh; throwaway prime256v1 > "$TMP/r/keys/llm-gateway.private.pem"; perl -i -ne 'print unless /^(\*\.pem|keys\/\*\.private\.pem)$/' "$TMP/r/.gitignore"
expect "red:keys/llm-gateway.private.pem: a private key file is tracked or would be committed" "private key: .gitignore loses the rule" sh -c "$(KT TestNoPrivateKeyCommittable)"
fresh; rm -rf "$TMP/r/.git"; expect nothing "private key: not a checkout" sh -c "$(KT TestNoPrivateKeyCommittable)"

# Where the feed is signed: this Mac by default, forge only when asked
S=TestFeedSigningStaysOnTheMacUnlessForgeIsAsked
fresh; expect green "sign on: the Mac by default" sh -c "$(KT $S)"
fresh; sed -i.bak 's/^FEED_SIGN_ON=${FEED_SIGN_ON:-local}$/FEED_SIGN_ON=${FEED_SIGN_ON:-forge}/' "$TMP/r/scripts/feed-common.sh"
grep -q 'FEED_SIGN_ON:-forge' "$TMP/r/scripts/feed-common.sh" || { echo "BAD  the default-forge plant did not apply"; exit 1; }
expect "red:--- FAIL: $S/default" "sign on: forge is the default" sh -c "$(KT $S)"
fresh; perl -i -pe 's/^(\t+)docker ps >\/dev\/null 2>&1 \|\| \{$/$1true || {/' "$TMP/r/scripts/feed-common.sh"
grep -q 'docker ps >/dev/null' "$TMP/r/scripts/feed-common.sh" && { echo "BAD  the docker-ps plant did not apply"; exit 1; }
expect "red:--- FAIL: $S/docker_down" "sign on: Docker is not asked first" sh -c "$(KT $S)"
fresh; perl -i -pe 's/^(\t+)echo "feed: FEED_SIGN_ON=forge: the container runs on/$1true "/' "$TMP/r/scripts/feed-common.sh"
expect "red:--- FAIL: $S/forge" "sign on: forge without a word in the log" sh -c "$(KT $S)"

# Publishing's Pages step: a 409 from GitHub is Pages already on, and the source is read back
P=TestPagesStepTakesAlreadyEnabledAsOn
fresh; expect green "pages: the step as written" sh -c "$(KT $P)"
fresh; sed -i.bak "s/grep -q '(HTTP 409)'/grep -q '(HTTP 499)'/" "$TMP/r/scripts/feed-common.sh"
grep -q "(HTTP 499)" "$TMP/r/scripts/feed-common.sh" || { echo "BAD  the 409 plant did not apply"; exit 1; }
expect "red:--- FAIL: $P/the_push_turned_it_on" "pages: a 409 stops publishing" sh -c "$(KT $P)"
fresh; sed -i.bak "s/elif grep -q '(HTTP 404)' \"\$err\"; then/elif true; then/" "$TMP/r/scripts/feed-common.sh"
grep -q "elif true; then" "$TMP/r/scripts/feed-common.sh" || { echo "BAD  the read-failure plant did not apply"; exit 1; }
expect "red:--- FAIL: $P/the_read_fails" "pages: any failed read taken as no site" sh -c "$(KT $P)"
fresh; sed -i.bak 's/turned on by the push; reading it again"$/turned on by the push"; return 0/' "$TMP/r/scripts/feed-common.sh"
grep -q 'turned on by the push"; return 0' "$TMP/r/scripts/feed-common.sh" || { echo "BAD  the no-read-back plant did not apply"; exit 1; }
expect "red:--- FAIL: $P/never_serves_gh-pages" "pages: a 409 taken as on without reading it back" sh -c "$(KT $P)"

# The daemon's reach: the walk from serve (invariants 14 and 15). DAEMON_SRC points it at a planted
# tree while the test itself compiles from the clean one, so a plant needs no imports.
D=TestServeNeverExecs
DG() { echo "cd $REPO && DAEMON_SRC=$TMP/r go test -count=1 -run '^$D\$' ."; }
fresh; expect green "daemon: as written" sh -c "$(DG)"
fresh; perl -i -pe 's/^(func loadLeaf\(dir string\) \(tls.Certificate, error\) \{)$/$1\n\texec.Command("ubus", "call")/' "$TMP/r/tls.go"
grep -q 'exec.Command("ubus", "call")' "$TMP/r/tls.go" || { echo "BAD  the exec plant did not apply"; exit 1; }
expect "red:the daemon can start a program" "daemon: a program started on its path" sh -c "$(DG)"
fresh; perl -i -pe 's/^(func \(g \*Gateway\) ServeHTTP\(.*\{)$/$1\n\texec.Command("uci", "show")/' "$TMP/r/gateway.go"
grep -q 'exec.Command("uci", "show")' "$TMP/r/gateway.go" || { echo "BAD  the handler plant did not apply"; exit 1; }
expect "red:the daemon can start a program" "daemon: a program started from a handler http.Server calls" sh -c "$(DG)"
fresh; perl -i -pe 's/^(\tleaf, err := loadLeaf\(cfg.CertDir\))$/\tensureCerts(cfg.CertDir, nil, nil, e.now())\n$1/' "$TMP/r/main.go"
grep -q 'ensureCerts(cfg.CertDir, nil' "$TMP/r/main.go" || { echo "BAD  the ensureCerts plant did not apply"; exit 1; }
expect "red:the daemon reaches ensureCerts" "daemon: reaches the CA's maker" sh -c "$(DG)"
fresh; perl -i -pe 's/^(func loadLeaf\(dir string\) \(tls.Certificate, error\) \{)$/$1\n\tos.ReadFile("\/etc\/config\/dhcp")/' "$TMP/r/tls.go"
expect "red:the daemon names /etc/config/dhcp" "daemon: reads dnsmasq's configuration by path" sh -c "$(DG)"
fresh; perl -i -pe 's/^(type daemonEnv struct \{)$/$1\n\tuci func()/' "$TMP/r/main.go"
expect "red:daemonEnv has the field uci" "daemon: its env gains uci" sh -c "$(DG)"
fresh; perl -i -pe 's/^(func clientList\(stdout, stderr io.Writer, e \*env\) int \{)$/$1\n\texec.Command("uci", "show")\n\tensureCerts("", nil, nil, e.now())/' "$TMP/r/main.go"
grep -q 'ensureCerts("", nil' "$TMP/r/main.go" || { echo "BAD  the CLI plant did not apply"; exit 1; }
expect green "daemon: a CLI command may do both" sh -c "$(DG)"
fresh; rm -f "$TMP/r"/*.go
expect nothing "daemon: no source" sh -c "$(DG)"
fresh; perl -i -pe 's/^func \(g \*Gateway\) ServeHTTP\(/func (g *Gateway) Handle(/' "$TMP/r/gateway.go"
expect nothing "daemon: the walk misses the handler" sh -c "$(DG)"

# The init script, run for real against stand-ins
I=files/etc/init.d/llm-gateway
fresh; expect green "init: as written" sh -c "$(KT TestInitPreparesBeforeAnythingElse)"
fresh; perl -i -ne 'print unless /^\tout=\$\("\$PROG" prepare 2>&1\)$/' "$TMP/r/$I"
grep -q '"$PROG" prepare' "$TMP/r/$I" && { echo "BAD  the no-prepare plant did not apply"; exit 1; }
expect "red:start_service did not run prepare" "init: prepare is not run" sh -c "$(KT TestInitPreparesBeforeAnythingElse)"
fresh; perl -i -0pe 's/(\tllmgw_account \|\| \{)/\tsync_firewall\n$1/' "$TMP/r/$I"
expect "red:out of order" "init: the firewall comes first" sh -c "$(KT TestInitPreparesBeforeAnythingElse)"
fresh; perl -i -pe 's/^(\tprocd_set_param stderr 1)$/$1\n\tprocd_set_param user llm-gateway/' "$TMP/r/$I"
expect "red:the daemon is given a user already" "init: the daemon is given a user in this step" sh -c "$(KT TestInitPreparesBeforeAnythingElse)"
fresh; perl -i -ne 'print unless /^\t\[ "\$rc" = 0 \] \|\| return 1$/' "$TMP/r/$I"
expect "red:--- FAIL: TestInitRefusalIsOneLineAndStartsNothing" "init: a refusal still starts" sh -c "$(KT TestInitRefusalIsOneLineAndStartsNothing)"
fresh; perl -i -pe 's/^(\t\[ "\$rc" = 0 \] \|\| return 1)$/\t[ "\$rc" = 0 ] || logger -t llm-gateway "prepare failed"\n$1/' "$TMP/r/$I"
expect "red:a refusal took 2 log lines" "init: a refusal takes two lines" sh -c "$(KT TestInitRefusalIsOneLineAndStartsNothing)"
fresh; perl -i -ne 'print unless /^\tllmgw_account \|\| \{/' "$TMP/r/$I"
expect "red:--- FAIL: TestInitWithoutTheAccountStartsNothing" "init: goes on without the account" sh -c "$(KT TestInitWithoutTheAccountStartsNothing)"
fresh; rm "$TMP/r/$I"; expect nothing "init: no script" sh -c "$(KT TestInitPreparesBeforeAnythingElse)"

# The package's scripts, from mkapk.sh as far as it runs without apk
U=TestPackageRunsPostUpgrade
fresh; expect green "package: as written" sh -c "$(KT $U)"
fresh; perl -i -ne 'print unless /^\t--script "post-upgrade:\$BUILD\/post-upgrade" \\$/' "$TMP/r/mkapk.sh"
expect "red:the direct mkpkg names post-upgrade 0 times" "package: CI's mkpkg drops post-upgrade" sh -c "$(KT $U)"
fresh; sed -i.bak 's/ --script post-upgrade:post-upgrade//' "$TMP/r/mkapk.sh"
expect "red:the staged mkpkg does not pack post-upgrade" "package: the staged mkpkg drops post-upgrade" sh -c "$(KT $U)"
fresh; perl -i -pe 's/^# post-install runs on a fresh install only\..*$/# post-install covers a fresh install and an upgrade./' "$TMP/r/mkapk.sh"
expect "red:the comment that says post-install covers an upgrade is back" "package: the wrong comment is back" sh -c "$(KT $U)"
fresh; perl -i -0pe 's/(if \/etc\/init\.d\/\$PKG enabled)/\/etc\/init.d\/\$PKG enable\n$1/' "$TMP/r/mkapk.sh"
expect "red:post-upgrade enables the service" "package: post-upgrade enables" sh -c "$(KT $U)"
fresh; perl -i -0pe 's/(<<EOT\n#!\/bin\/sh\n)\/etc\/init\.d\/\$PKG account\n(if )/$1$2/' "$TMP/r/mkapk.sh"
expect "red:want account and restart" "package: post-upgrade makes no account" sh -c "$(KT $U)"
fresh; perl -i -0pe 's/if \/etc\/init\.d\/\$PKG enabled \|\| \/etc\/init\.d\/\$PKG running; then\n\t\/etc\/init\.d\/\$PKG restart\nfi\n//' "$TMP/r/mkapk.sh"
expect "red:want account and restart" "package: post-upgrade does not restart" sh -c "$(KT $U)"
fresh; perl -i -ne 'print unless /^rm -rf \/tmp\/\$PKG$/' "$TMP/r/mkapk.sh"
expect "red:post-upgrade left the old journal in /tmp" "package: the old journal stays" sh -c "$(KT $U)"
fresh; sed -i.bak '/^# r3:/s/$/ More words./' "$TMP/r/mkapk.sh"
expect green "package: a comment may change" sh -c "$(KT $U)"
fresh; rm "$TMP/r/mkapk.sh"; expect nothing "package: no mkapk.sh" sh -c "$(KT $U)"

# The built package's scripts, read from `apk adbdump`, in the shape OpenWrt 25.12's apk 3.0.5 prints
PS="sh $REPO/scripts/gate-package-scripts.sh"
dump() { # post-upgrade's text
	cat > "$TMP/dump.txt" <<EOD
#%SCHEMA: 676B6370
# ADB block, size: 1452, compat: 0, ver: 0
info:
  name: llm-gateway
  version: 0.1.0-r3
scripts:
  post-install: |
    #!/bin/sh
    /etc/init.d/llm-gateway account
    /etc/init.d/llm-gateway enable
    /etc/init.d/llm-gateway restart
    exit 0
  pre-deinstall: |
    #!/bin/sh
    /etc/init.d/llm-gateway stop
    exit 0
$1
# sig v00 h04 7702ecfc36eb9767: UNTRUSTED signature
# data block, size: 1106
EOD
}
UP='  post-upgrade: |
    #!/bin/sh
    /etc/init.d/llm-gateway account
    if /etc/init.d/llm-gateway enabled || /etc/init.d/llm-gateway running; then
    	/etc/init.d/llm-gateway restart
    fi
    rm -rf /tmp/llm-gateway
    exit 0'
dump "$UP"; expect green "package scripts: as built" $PS "$TMP/dump.txt"
dump ""; expect "red:the package has no post-upgrade script" "package scripts: post-upgrade dropped" $PS "$TMP/dump.txt"
dump "$(echo "$UP" | grep -v 'llm-gateway account')"; expect "red:post-upgrade does not run: /etc/init.d/llm-gateway account" "package scripts: no account" $PS "$TMP/dump.txt"
dump "$(echo "$UP" | grep -v 'restart')"; expect "red:post-upgrade does not run: /etc/init.d/llm-gateway restart" "package scripts: no restart" $PS "$TMP/dump.txt"
dump "$(echo "$UP" | sed 's/^    exit 0$/    \/etc\/init.d\/llm-gateway enable/')"; expect "red:post-upgrade enables the service" "package scripts: enables on upgrade" $PS "$TMP/dump.txt"
: > "$TMP/dump.txt"; expect nothing "package scripts: an empty dump" $PS "$TMP/dump.txt"
rm "$TMP/dump.txt"; expect nothing "package scripts: no dump" $PS "$TMP/dump.txt"
echo "package: {}" > "$TMP/dump.txt"; expect nothing "package scripts: a dump with no scripts" $PS "$TMP/dump.txt"

echo "teeth: $pass ok, $bad bad"
[ $bad = 0 ]
