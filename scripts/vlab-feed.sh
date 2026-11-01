#!/bin/sh
# vlab-feed.sh -- the signed feed on a real OpenWrt 25.12 router, in vlab on forge, before it is published.
#
# Brings up e2e/feed.json (one router), copies the x86_64 part of the feed build-feed.sh wrote into
# it as a local repository, and runs e2e/feed-checks.sh there with OpenWrt's own apk. Every refusal
# has its positive control: the same feed installs with the feed's key. The lab is always taken
# down, pass or fail.
#
#   refused_without_the_key                no key in /etc/apk/keys: apk update calls the feed
#                                          UNTRUSTED and apk add finds no such package
#   installs_from_the_signed_index         the key in /etc/apk/keys: apk add llm-gateway with no
#                                          --allow-untrusted, apk policy names the feed, it runs
#   installed_files_belong_to_root         every installed file is 0:0, and so are the directories
#                                          the package shares with the system
#   refused_with_another_key               another key in its place: untrusted again, nothing installed
#   refused_when_the_package_is_corrupted  the feed's key and signed index, one byte of the package
#                                          changed: apk add refuses and installs nothing
#
#   scripts/vlab-feed.sh              run it (from the Mac; forge over ssh)
#   scripts/vlab-feed.sh --selftest   the names of the checks, for gate-scenarios-bound.sh
#   OLD_APK=<file> scripts/vlab-feed.sh   also install that package afterwards and show its owners
set -u
CHECKS='refused_without_the_key installs_from_the_signed_index installed_files_belong_to_root refused_with_another_key refused_when_the_package_is_corrupted'
if [ "${1:-}" = "--selftest" ]; then for c in $CHECKS; do echo "$c"; done; exit 0; fi

REPO=$(cd "$(dirname "$0")/.." && pwd)
FORGE=${FORGE:-yurii@192.168.1.10}
RD=/home/yurii/llmgw-feed
VL="sudo /home/yurii/vlab/bin/vlab"
FEED=${FEED:-$REPO/feed-out}
OLD_APK=${OLD_APK:-}
X=25.12/x86_64
f() { ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 "$FORGE" "$@"; }
say() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*"; }

[ -f "$FEED/$X/packages.adb" ] || { echo "FAIL: measured nothing: no $FEED/$X/packages.adb; run build-feed.sh first"; exit 1; }
PKG=$(cd "$FEED/$X" && ls -1 llm-gateway-*.apk)

say "bundle: the x86_64 feed, a copy with one byte of the package changed, the key and another"
B=$REPO/build/vlab-feed
rm -rf "$B"
mkdir -p "$B/bundle/good/$X" "$B/bundle/bad/$X" "$B/bundle/keys"
cp "$FEED/$X/packages.adb" "$FEED/$X/$PKG" "$B/bundle/good/$X/"
cp "$FEED/$X/packages.adb" "$FEED/$X/$PKG" "$B/bundle/bad/$X/"
size=$(wc -c < "$FEED/$X/$PKG" | tr -d ' ')
printf 'Z' | dd of="$B/bundle/bad/$X/$PKG" bs=1 seek=$((size / 2)) conv=notrunc 2>/dev/null
cmp -s "$B/bundle/bad/$X/$PKG" "$FEED/$X/$PKG" && { say "FAIL: the corruption did not change the package"; exit 1; }
cp "$FEED/llm-gateway.pem" "$B/bundle/keys/llm-gateway.pem"
openssl ecparam -name prime256v1 -genkey -noout | openssl ec -pubout -out "$B/bundle/keys/other.pem" 2>/dev/null
[ -z "$OLD_APK" ] || cp "$OLD_APK" "$B/bundle/old.apk"
say "  $PKG sha256 $(shasum -a 256 "$FEED/$X/$PKG" | cut -d' ' -f1), corrupted at byte $((size / 2)) of $size"

if f "$VL status" >/dev/null 2>&1; then say "FAIL: a lab is already up on forge; not touching it"; exit 1; fi
say "ship to forge"
f "rm -rf $RD && mkdir -p $RD/e2e" || exit 1
COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -C "$REPO" -cf - e2e/feed.json | f "tar -xf - -C $RD" || exit 1
COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -C "$B/bundle" -cf - . | f "cat > $RD/bundle.tar" || exit 1
trap 'say "take the lab down"; f "$VL down >/dev/null 2>&1; rm -rf $RD; if $VL check clean >/dev/null 2>&1; then echo \"  vlab check clean: clean\"; else echo \"  vlab check clean: NOT clean\"; fi"' EXIT

say "vlab up"
f "cd /home/yurii/vlab && $VL up $RD/e2e/feed.json --ttl 30m" | tail -3 || exit 1

say "router: the bundle into /tmp/feedtest"
f "cat $RD/bundle.tar | $VL ssh r1 -- 'rm -rf /tmp/feedtest && mkdir -p /tmp/feedtest && tar -xf - -C /tmp/feedtest'" || exit 1

say "router: the checks, with OpenWrt's own apk"
out=$(f "$VL ssh r1 -- sh -s" < "$REPO/e2e/feed-checks.sh")
echo "$out"

passed=0; failed=0
for c in $CHECKS; do
	if echo "$out" | grep -qx "PASS $c"; then passed=$((passed+1)); say "PASS $c"
	else failed=$((failed+1)); say "FAIL $c"; fi
done
say "checks: $passed passed, $failed failed"
[ $((passed+failed)) -gt 0 ] || { echo "FAIL: measured nothing"; exit 1; }
[ "$failed" = 0 ]
