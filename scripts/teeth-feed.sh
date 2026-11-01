#!/bin/sh
# teeth-feed.sh -- gate-feed.sh must go red where the feed is wrong, and stay green where it is not.
#
# Runs gate-feed.sh against copies of the feed build-feed.sh wrote:
#
#   clean       the feed as built: it must PASS
#   resigned    every package and index re-signed with a throwaway key, and that key published as
#               the feed's: a sound feed, so it must PASS. Without it the teeth below could be red
#               for a reason that has nothing to do with them.
#   wrongkey    the re-signed files with keys/llm-gateway.pem as the feed's key: FAIL
#               check_signed_by_the_feed_key
#   unsignedidx the x86_64 index rebuilt and left unsigned: FAIL check_signed_by_the_feed_key
#   tampered    one byte of the x86_64 package changed: FAIL check_signed_by_the_feed_key
#   wrongarch   the aarch64_cortex-a53 package in the x86_64 directory, signed by the right key:
#               FAIL check_arch_matches_its_directory
#   wrongindex  the aarch64_cortex-a53 index in the x86_64 directory, signed by the right key: FAIL
#               check_index_lists_the_packages
#   nobody      the x86_64 package replaced by one with an entry owned by nobody, signed with the
#               throwaway key: FAIL check_entries_owned_by_root
#   staleindex  the re-signed feed with an x86_64 index made over another package of the same name
#               and version: FAIL check_index_lists_the_packages
#   renamed     every package renamed to a version it does not hold, signatures intact: FAIL
#               check_index_lists_the_packages, on the name apk would ask for
#   dsstore     a .DS_Store in an architecture's directory: FAIL check_only_feed_files, through
#               gate-feed-tree.sh (whose other teeth are in gates-have-teeth.sh)
#   missing     no feed at all: it must say it measured nothing
#
# The throwaway key is made here with openssl into a 0700 directory that is removed on exit, and
# reaches the container on standard input. The real private key is never read: nothing here signs
# with it. GATE=<script> runs another gate instead, for the gate's own mutants.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
. "$ROOT/scripts/feed-common.sh"
FEED=${FEED:-$ROOT/feed-out}
GATE=${GATE:-$ROOT/scripts/gate-feed.sh}
REAL=$ROOT/keys/llm-gateway.pem
[ -f "$FEED/$RELEASE/x86_64/packages.adb" ] && [ -f "$FEED/$RELEASE/aarch64_cortex-a53/packages.adb" ] ||
	{ echo "teeth-feed: measured nothing: no feed at $FEED; run build-feed.sh first" >&2; exit 1; }

mkdir -p "$ROOT/build"
T=$(mktemp -d "$ROOT/build/feed-teeth.XXXXXX")
chmod 0700 "$T"
trap 'rm -rf "$T"' EXIT
openssl ecparam -name prime256v1 -genkey -noout -out "$T/throwaway.key"
openssl ec -in "$T/throwaway.key" -pubout -out "$T/throwaway.pem" 2>/dev/null

# variant <name>: a copy of the feed's top level, with the given packages and key
variant() {
	mkdir -p "$T/$1"
	cp "$FEED/.nojekyll" "$FEED/index.html" "$T/$1/"
}
for m in clean wrongarch wrongindex tampered dsstore renamed; do
	variant $m
	cp -R "$FEED/$RELEASE" "$T/$m/"
	cp "$REAL" "$T/$m/llm-gateway.pem"
done
X=$RELEASE/x86_64
A=$RELEASE/aarch64_cortex-a53
PKG=$(cd "$FEED/$X" && ls -1 llm-gateway-*.apk)
cp "$FEED/$A/$PKG" "$T/wrongarch/$X/$PKG"
cp "$FEED/$A/packages.adb" "$T/wrongindex/$X/packages.adb"
# One byte inside the package's first, signed block.
printf 'Z' | dd of="$T/tampered/$X/$PKG" bs=1 seek=200 conv=notrunc 2>/dev/null
cmp -s "$T/tampered/$X/$PKG" "$FEED/$X/$PKG" && { echo "teeth-feed: the tampering did not change the package" >&2; exit 1; }
touch "$T/dsstore/$X/.DS_Store"
for d in "$T/renamed/$RELEASE"/*; do mv "$d/$PKG" "$d/llm-gateway-0.0.1-r9.apk"; done
mkdir -p "$T/missing"

{
	echo "RELEASE='$RELEASE'"
	cat <<'CONTAINER'
set -eu
mkdir -p /w/key /w/in /w/out /w/real /w/throw /w/tree/usr/bin
while IFS= read -r l; do
	printf '%s\n' "$l" >> /w/key/throwaway.key
	case "$l" in "-----END "*) break ;; esac
done
cd /w/in && tar -xf -
cp /w/in/real.pem /w/real/llm-gateway.pem
cp /w/in/throwaway.pem /w/throw/llm-gateway.pem
sign() { apk adbsign --allow-untrusted --reset-signatures --sign-key /w/key/throwaway.key "$1" >&2; }
index() { # <dir> <keys dir> [sign]
	cd "$1"
	rm -f packages.adb
	apk --keys-dir "$2" mkndx --output packages.adb ./*.apk >&2
	[ "${3:-}" != sign ] || sign packages.adb
}

mkdir -p /w/out/resigned
cp -R /w/in/$RELEASE /w/out/resigned/
for d in /w/out/resigned/$RELEASE/*; do
	for f in "$d"/*.apk; do sign "$f"; done
	index "$d" /w/throw sign
done

cp -R /w/out/resigned /w/out/nobody
d=/w/out/nobody/$RELEASE/x86_64
f=$(ls "$d"/*.apk)
version=$(apk adbdump "$f" | sed -n 's/^  version: //p' | head -n 1)
printf '#!/bin/sh\necho stub\n' > /w/tree/usr/bin/llm-gateway
echo planted > /w/tree/usr/bin/planted
chmod 0755 /w/tree/usr/bin/llm-gateway
chown -R 0:0 /w/tree
chown 65534:65534 /w/tree/usr/bin/planted
rm -f "$f"
apk mkpkg --info name:llm-gateway --info "version:$version" --info arch:x86_64 \
	--files /w/tree --output "$d/llm-gateway-$version.apk" >&2
sign "$d/llm-gateway-$version.apk"
index "$d" /w/throw sign

# staleindex: an index over a root-owned stub of the same name and version, beside the real package
cp -R /w/out/resigned /w/out/staleindex
mkdir -p /w/stub
chown 0:0 /w/tree/usr/bin/planted
apk mkpkg --info name:llm-gateway --info "version:$version" --info arch:x86_64 \
	--files /w/tree --output "/w/stub/llm-gateway-$version.apk" >&2
sign "/w/stub/llm-gateway-$version.apk"
index /w/stub /w/throw sign
cp /w/stub/packages.adb /w/out/staleindex/$RELEASE/x86_64/packages.adb

mkdir -p /w/out/unsignedidx
cp -R /w/in/$RELEASE /w/out/unsignedidx/
index /w/out/unsignedidx/$RELEASE/x86_64 /w/real

cd /w/out && tar -cf - resigned nobody staleindex unsignedidx
CONTAINER
} > "$T/make.sh"
mkdir -p "$T/stage"
cp -R "$FEED/$RELEASE" "$T/stage/"
cp "$REAL" "$T/stage/real.pem"
cp "$T/throwaway.pem" "$T/stage/throwaway.pem"
echo "==> making the re-signed copies in $ALPINE ($FEED_SIGN_ON)"
{ cat "$T/throwaway.key"; feed_tar "$T/stage" "$RELEASE" real.pem throwaway.pem; } | in_pinned_alpine "$T/make.sh" > "$T/made.tar"
mkdir -p "$T/made"
tar -xf "$T/made.tar" -C "$T/made"
for m in resigned wrongkey nobody staleindex unsignedidx; do variant $m; done
cp -R "$T/made/resigned/$RELEASE" "$T/resigned/"
cp -R "$T/made/resigned/$RELEASE" "$T/wrongkey/"
cp -R "$T/made/nobody/$RELEASE" "$T/nobody/"
cp -R "$T/made/staleindex/$RELEASE" "$T/staleindex/"
cp -R "$T/made/unsignedidx/$RELEASE" "$T/unsignedidx/"
cp "$T/throwaway.pem" "$T/resigned/llm-gateway.pem"
cp "$T/throwaway.pem" "$T/nobody/llm-gateway.pem"
cp "$T/throwaway.pem" "$T/staleindex/llm-gateway.pem"
cp "$REAL" "$T/wrongkey/llm-gateway.pem"
cp "$REAL" "$T/unsignedidx/llm-gateway.pem"

failures=0
n=0
# tooth <name> <pass|fail> <text the output must contain> <public key>
tooth() {
	name=$1 want=$2 expect=$3
	rc=0
	FEED="$T/$name" PUB_KEY="$4" sh "$GATE" > "$T/$name.log" 2>&1 || rc=$?
	ok=1
	case "$want" in
		pass) [ "$rc" -eq 0 ] || ok=0 ;;
		fail) [ "$rc" -ne 0 ] || ok=0 ;;
	esac
	grep -qF -- "$expect" "$T/$name.log" || ok=0
	n=$((n + 1))
	if [ "$ok" = 1 ]; then
		echo "PASS tooth $name: gate exit $rc, said \"$expect\""
	else
		echo "FAIL tooth $name: wanted $want with \"$expect\", gate exit $rc; its output:"
		sed 's/^/    /' "$T/$name.log"
		failures=$((failures + 1))
	fi
}

tooth clean pass "gate-feed: all 5 checks passed" "$REAL"
tooth resigned pass "gate-feed: all 5 checks passed" "$T/throwaway.pem"
tooth wrongkey fail "FAIL check_signed_by_the_feed_key: aarch64_cortex-a53/$PKG" "$REAL"
tooth unsignedidx fail "FAIL check_signed_by_the_feed_key: x86_64/packages.adb" "$REAL"
tooth tampered fail "FAIL check_signed_by_the_feed_key: x86_64/$PKG" "$REAL"
tooth wrongarch fail "FAIL check_arch_matches_its_directory: x86_64/$PKG declares arch 'aarch64_cortex-a53'" "$REAL"
tooth wrongindex fail "FAIL check_index_lists_the_packages: x86_64/packages.adb lists packages of another arch" "$REAL"
tooth nobody fail "FAIL check_entries_owned_by_root: x86_64/$PKG" "$T/throwaway.pem"
tooth staleindex fail "FAIL check_index_lists_the_packages: x86_64/packages.adb lists 'llm-gateway-" "$T/throwaway.pem"
tooth renamed fail "FAIL check_index_lists_the_packages: aarch64_cortex-a53/llm-gateway-0.0.1-r9.apk is llm-gateway $(echo "$PKG" | sed 's/^llm-gateway-//; s/\.apk$//'), so apk asks for $PKG" "$REAL"
tooth dsstore fail "FAIL check_only_feed_files: a .DS_Store" "$REAL"
tooth missing fail "measured nothing" "$REAL"

[ "$failures" -eq 0 ] || { echo "teeth-feed: $failures of $n teeth failed"; exit 1; }
echo "teeth-feed: all $n teeth hold"
