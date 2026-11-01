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

echo "teeth: $pass ok, $bad bad"
[ $bad = 0 ]
