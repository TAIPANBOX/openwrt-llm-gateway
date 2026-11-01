#!/bin/sh
# Runs on r1, with OpenWrt's own apk, over the bundle scripts/vlab-feed.sh copied to /tmp/feedtest:
#   good/25.12/x86_64/   the x86_64 part of the signed feed, as build-feed.sh wrote it
#   bad/25.12/x86_64/    the same, with one byte of the package changed; the index is the signed one
#   keys/llm-gateway.pem the feed's key; keys/other.pem a key that signed nothing here
#   old.apk              optional: a package to install afterwards, only to show its owners
# Prints PASS or FAIL per check, and the evidence under it.
set -u
F=/tmp/feedtest
GOOD=$F/good/25.12/x86_64/packages.adb
BAD=$F/bad/25.12/x86_64/packages.adb
LIST=/etc/apk/repositories.d/llm-gateway-test.list
KEY=/etc/apk/keys/llm-gateway.pem
result() { if [ "$2" = 0 ]; then echo "PASS $1"; else echo "FAIL $1"; fi; }
show() { sed 's/^/    /' "$@"; }
not_installed() { ! apk info -e llm-gateway >/dev/null 2>&1 && [ ! -e /usr/bin/llm-gateway ]; }

echo "  apk $(apk --version | cut -d' ' -f2 | tr -d ,), OpenWrt $(. /etc/openwrt_release; echo "$DISTRIB_RELEASE"), $(cat /etc/apk/arch)"
rm -f "$KEY"
mkdir -p /var/lock
echo "$GOOD" > "$LIST"

echo "--- refused_without_the_key"
apk update > /tmp/u1 2>&1
apk add llm-gateway > /tmp/a1 2>&1; rc=$?
grep -F "$GOOD" /tmp/u1 | show
show /tmp/a1
r=1
[ "$rc" != 0 ] && grep -qF "$GOOD: UNTRUSTED signature" /tmp/u1 && grep -q 'llm-gateway (no such package)' /tmp/a1 && not_installed && r=0
result refused_without_the_key $r

echo "--- installs_from_the_signed_index"
cp "$F/keys/llm-gateway.pem" "$KEY"
echo "    sha256 of $KEY: $(sha256sum "$KEY" | cut -d' ' -f1)"
apk update > /tmp/u2 2>&1
apk add llm-gateway > /tmp/a2 2>&1; rc=$?
apk policy llm-gateway > /tmp/p 2>&1
v=$(llm-gateway version 2>&1)
show /tmp/a2
show /tmp/p
echo "    llm-gateway version: $v"
r=1
[ "$rc" = 0 ] && ! grep -q UNTRUSTED /tmp/u2 && ! grep -q allow-untrusted /tmp/a2 && grep -qF "$GOOD" /tmp/p && [ -n "$v" ] && r=0
result installs_from_the_signed_index $r

echo "--- installed_files_belong_to_root"
apk info -L llm-gateway 2>/dev/null | sed '1d;/^$/d' > /tmp/files
n=0 bad=0
while IFS= read -r f; do
	o=$(ls -ldn "/$f" | awk '{ print $3 ":" $4 }')
	n=$((n + 1))
	[ "$o" = 0:0 ] || { bad=$((bad + 1)); echo "    /$f is $o"; }
done < /tmp/files
for d in / /etc /etc/config /etc/init.d /lib /lib/upgrade /lib/upgrade/keep.d /usr /usr/bin; do
	o=$(ls -ldn "$d" | awk '{ print $3 ":" $4 }')
	n=$((n + 1))
	[ "$o" = 0:0 ] || { bad=$((bad + 1)); echo "    $d is $o"; }
done
ls -ln /usr/bin/llm-gateway /etc/init.d/llm-gateway /etc/config/llm-gateway | show
echo "    $n paths, $bad not 0:0"
r=1
[ -s /tmp/files ] && [ "$bad" = 0 ] && r=0
result installed_files_belong_to_root $r

echo "--- refused_with_another_key"
apk del llm-gateway > /tmp/d 2>&1
cp "$F/keys/other.pem" "$KEY"
apk update > /tmp/u3 2>&1
apk add llm-gateway > /tmp/a3 2>&1; rc=$?
grep -F "$GOOD" /tmp/u3 | show
show /tmp/a3
r=1
[ "$rc" != 0 ] && grep -qF "$GOOD: UNTRUSTED signature" /tmp/u3 && grep -q 'llm-gateway (no such package)' /tmp/a3 && not_installed && r=0
result refused_with_another_key $r

echo "--- refused_when_the_package_is_corrupted"
cp "$F/keys/llm-gateway.pem" "$KEY"
echo "$BAD" > "$LIST"
apk update > /tmp/u4 2>&1
apk add llm-gateway > /tmp/a4 2>&1; rc=$?
show /tmp/a4
r=1
[ "$rc" != 0 ] && ! grep -q UNTRUSTED /tmp/u4 && not_installed && r=0
result refused_when_the_package_is_corrupted $r

if [ -f "$F/old.apk" ]; then
	echo "--- evidence: owners of the package given as old.apk"
	apk add --allow-untrusted "$F/old.apk" > /tmp/a5 2>&1 || show /tmp/a5
	ls -ln /usr/bin/llm-gateway /etc/init.d/llm-gateway /etc/config/llm-gateway 2>&1 | show
	ls -ldn / /etc /etc/init.d /usr /usr/bin 2>&1 | show
	apk del llm-gateway > /dev/null 2>&1
fi

rm -f "$LIST" "$KEY"
apk update > /dev/null 2>&1
