#!/bin/sh
# gate-feed-tree.sh -- the feed holds what a feed is made of, and nothing else.
#
# The half of gate-feed.sh that needs no apk-tools, so it runs anywhere, CI included
# (gates-have-teeth.sh plants its faults there). gate-feed.sh runs it first.
#
#   check_feed_present       an index for every architecture CI builds; else it measured nothing
#   check_only_feed_files    .nojekyll, index.html, llm-gateway.pem, and per architecture the index
#                            and one package. Finder writes .DS_Store into any folder it shows, and
#                            a stray file of any kind has no place on a page routers trust.
#   check_nojekyll           .nojekyll at the root, or Pages runs Jekyll over the feed
#   check_published_key      the feed's llm-gateway.pem is keys/llm-gateway.pem, byte for byte
#   check_architectures      the architecture directories are exactly CI's, each with the index
#                            and the same one package
#   check_no_private_key     no file holds a private key
#
#   FEED=<dir> PUB_KEY=<pem> ./scripts/gate-feed-tree.sh     (defaults: feed-out, keys/llm-gateway.pem)
set -eu

CHECKS='check_feed_present check_only_feed_files check_nojekyll check_published_key check_architectures check_no_private_key'
if [ "${1:-}" = "--selftest" ]; then for c in $CHECKS; do echo "$c"; done; exit 0; fi

ROOT=$(cd "$(dirname "$0")/.." && pwd)
. "$ROOT/scripts/feed-common.sh"
FEED=${FEED:-$ROOT/feed-out}
PUB_KEY=${PUB_KEY:-$ROOT/keys/llm-gateway.pem}
fail() { echo "FAIL $1: $2"; exit 1; }

# ---- check_feed_present
[ -d "$FEED/$RELEASE" ] || { echo "FAIL check_feed_present: measured nothing: no $FEED/$RELEASE"; exit 1; }
n=0
for a in $ARCHES; do [ -f "$FEED/$RELEASE/$a/packages.adb" ] && n=$((n + 1)); done
[ "$n" -gt 0 ] || { echo "FAIL check_feed_present: measured nothing: no index under $FEED/$RELEASE"; exit 1; }
echo "PASS check_feed_present ($n indexes)"

# ---- check_only_feed_files
ds=$(cd "$FEED" && find . -name .DS_Store)
[ -z "$ds" ] || fail check_only_feed_files "a .DS_Store, Finder's: $(echo $ds)"
stray=$(cd "$FEED" && find . ! -type d ! -path ./.nojekyll ! -path ./index.html ! -path ./llm-gateway.pem \
	! -path "./$RELEASE/*/packages.adb" ! -path "./$RELEASE/*/llm-gateway-*.apk")
[ -z "$stray" ] || fail check_only_feed_files "files a feed does not carry: $(echo $stray)"
links=$(cd "$FEED" && find . -type l)
[ -z "$links" ] || fail check_only_feed_files "symbolic links: $(echo $links)"
echo "PASS check_only_feed_files ($(cd "$FEED" && find . -type f | wc -l | tr -d ' ') files)"

# ---- check_nojekyll
[ -f "$FEED/.nojekyll" ] || fail check_nojekyll "no .nojekyll at the root of $FEED"
echo "PASS check_nojekyll"

# ---- check_published_key
[ -f "$PUB_KEY" ] || fail check_published_key "no public key at $PUB_KEY"
[ -f "$FEED/llm-gateway.pem" ] || fail check_published_key "the feed has no llm-gateway.pem"
cmp -s "$FEED/llm-gateway.pem" "$PUB_KEY" || fail check_published_key "the feed's llm-gateway.pem is not $PUB_KEY"
echo "PASS check_published_key (sha256 $(shasum -a 256 "$PUB_KEY" | cut -d' ' -f1))"

# ---- check_architectures
got=$(cd "$FEED/$RELEASE" && ls -1A | LC_ALL=C sort | tr '\n' ' ')
want=$(for a in $ARCHES; do echo "$a"; done | LC_ALL=C sort | tr '\n' ' ')
[ "$got" = "$want" ] || fail check_architectures "architectures '$got', CI builds '$want'"
pkg=""
for a in $ARCHES; do
	[ -f "$FEED/$RELEASE/$a/packages.adb" ] || fail check_architectures "$a has no packages.adb"
	p=$(cd "$FEED/$RELEASE/$a" && ls -1 ./llm-gateway-*.apk 2>/dev/null | sed 's|^\./||')
	[ "$(printf '%s\n' "$p" | grep -c .)" = 1 ] || fail check_architectures "$a holds '$(echo $p)', not one package"
	[ -z "$pkg" ] || [ "$pkg" = "$p" ] || fail check_architectures "$a holds $p, another architecture $pkg"
	pkg=$p
done
echo "PASS check_architectures ($pkg for $(echo $ARCHES))"

# ---- check_no_private_key
marker="PRIVATE"" KEY-----"
held=$(grep -rl -- "$marker" "$FEED" 2>/dev/null || true)
[ -z "$held" ] || fail check_no_private_key "a private key in $(echo $held)"
echo "PASS check_no_private_key"

echo "gate-feed-tree: all 6 checks passed ($FEED)"
