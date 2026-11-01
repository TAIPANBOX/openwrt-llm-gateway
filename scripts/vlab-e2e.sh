#!/bin/sh
# vlab-e2e.sh -- the end-to-end run on a real OpenWrt 25.12 firewall, in vlab on forge.
#
# Builds the gateway for x86_64, stages the package, brings up e2e/llmgw.json (r1 with a home
# LAN, a guest network and a models zone; a laptop, a guest and a model box as clients), builds
# and installs the package inside r1 with OpenWrt's own apk, and runs the checks below. Every
# negative check has a positive control first, so a refusal counts only when the same path is
# shown to work for whoever should have it. The lab is always taken down, pass or fail.
#
#   scripts/vlab-e2e.sh              run it (from the Mac; forge over ssh)
#   scripts/vlab-e2e.sh --selftest   the names of the checks, for gate-scenarios-bound.sh
set -u
CHECKS='guest_cannot_reach_the_port lan_cannot_reach_the_model_box lan_reaches_the_model_through_the_gateway models_zone_has_no_internet'
if [ "${1:-}" = "--selftest" ]; then for c in $CHECKS; do echo "$c"; done; exit 0; fi

FORGE=${FORGE:-yurii@192.168.1.10}
RD=/home/yurii/llmgw-e2e
VL="sudo /home/yurii/vlab/bin/vlab"
REPO=$(cd "$(dirname "$0")/.." && pwd)
VERSION=${VERSION:-0.1.0}
f() { ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 "$FORGE" "$@"; }
say() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*"; }

passed=0; failed=0
check() { # name, outcome (0 = holds)
	if [ "$2" = 0 ]; then passed=$((passed+1)); say "PASS $1"; else failed=$((failed+1)); say "FAIL $1"; fi
}

say "build x86_64 and stage the package"
cd "$REPO"
rm -rf build/stage
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o build/llm-gateway.amd64 . || exit 1
MKAPK_STAGE=build/stage ./mkapk.sh build/llm-gateway.amd64 "$VERSION" x86_64 >/dev/null || exit 1

say "ship to forge"
f "rm -rf $RD && mkdir -p $RD" || exit 1
COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -C "$REPO" -cf - e2e build/stage | f "tar -xf - -C $RD" || exit 1

# OpenWrt's own apk has no mkpkg (only the SDK's host build does), so the package is built on
# forge by the pinned alpine image mkapk.sh names, already in forge's Docker; --pull never makes
# sure nothing is downloaded for it.
say "build the apk on forge"
ALPINE=$(sed -n 's/^ALPINE=\${ALPINE:-\(.*\)}$/\1/p' "$REPO/mkapk.sh")
f "sudo docker run --rm --pull never -v $RD/build/stage:/s -w /s $ALPINE sh run-mkpkg.sh" | tail -1 || exit 1
f "ls $RD/build/stage/llm-gateway-*.apk" >/dev/null || { say "FAIL: no apk was built"; exit 1; }

if f "$VL status" >/dev/null 2>&1; then say "FAIL: a lab is already up on forge; not touching it"; exit 1; fi
trap 'say "take the lab down"; f "$VL down >/dev/null 2>&1; $VL check clean" | tail -1' EXIT

say "vlab up"
f "cd /home/yurii/vlab && $VL up $RD/e2e/llmgw.json --ttl 45m" | tail -3 || exit 1

say "router: networks, zones, the package"
f "cat $RD/build/stage/llm-gateway-*.apk | $VL ssh r1 -- 'rm -rf /tmp/s && mkdir -p /tmp/s && cat > /tmp/s/llm-gateway.apk'" || exit 1
f "$VL ssh r1 -- sh -s" < e2e/router-setup.sh | tail -4
KEY=$(f "$VL ssh r1 -- cat /tmp/client.txt" | sed -n 's/^  key *\(llmgw_[A-Za-z0-9_-]*\)$/\1/p')
[ -n "$KEY" ] || { say "FAIL: no client key from the router"; exit 1; }
f "$VL ssh r1 -- cat /etc/llm-gateway/ca.crt" > "$REPO/build/ca.crt"
f "cat > $RD/ca.crt" < "$REPO/build/ca.crt"

say "clients: model box (static), laptop and guest (DHCP from r1)"
f "$VL exec modelbox -- sh -c 'ip addr add 10.60.0.10/24 dev eth0 && ip link set eth0 up && ip route add default via 10.60.0.1'"
f "$VL exec modelbox -- sh -c 'python3 $RD/e2e/standin.py >/dev/null 2>&1 &'"
f "$VL dhcp laptop" | tail -1
f "$VL dhcp guest" | tail -1
sleep 2

cx() { f "$VL exec $1 --timeout 20s -- $2"; }  # client, command
curlcode() { cx "$1" "curl -s -o /dev/null -w %{http_code} -m 6 $2" 2>/dev/null; }

say "--- evidence"
f "$VL ssh r1 -- netstat -ltn" | grep -E ':876[01] ' | sed 's/^/  r1 listens: /'
f "$VL ssh r1 -- 'llm-gateway version; apk info -vv 2>/dev/null | grep llm-gateway'" | sed 's/^/  r1: /'

say "--- lan_reaches_the_model_through_the_gateway"
body='{"model":"stand-in","messages":[{"role":"user","content":"E2E-PROMPT-MARKER"}]}'
ans=$(cx laptop "curl -sS -m 10 --cacert $RD/ca.crt -H 'Authorization: Bearer $KEY' -H 'Content-Type: application/json' -d '$body' https://llm.lan:8760/v1/chat/completions")
echo "  laptop -> https://llm.lan:8760 with its key: $ans"
nokey=$(curlcode laptop "--cacert $RD/ca.crt https://llm.lan:8760/v1/models")
echo "  laptop -> https://llm.lan:8760 with no key: $nokey"
case "$ans" in *"stand-in answer"*) [ "$nokey" = 401 ]; check lan_reaches_the_model_through_the_gateway $? ;; *) check lan_reaches_the_model_through_the_gateway 1 ;; esac

say "--- lan_cannot_reach_the_model_box"
ctl=$(f "$VL ssh r1 -- wget -q -T 5 -O - http://10.60.0.10:8080/health")
echo "  control, r1 -> model box: $ctl"
direct=$(curlcode laptop "http://10.60.0.10:8080/health")
echo "  laptop -> model box directly: ${direct:-no answer}"
case "$ctl" in *ok*) [ "$direct" != 200 ]; check lan_cannot_reach_the_model_box $? ;; *) check lan_cannot_reach_the_model_box 1 ;; esac

say "--- guest_cannot_reach_the_port"
gnet=$(curlcode guest "http://1.1.1.1/")
echo "  control, guest -> internet: ${gnet:-no answer}"
g1=$(curlcode guest "-k https://192.168.71.1:8760/health")
g2=$(curlcode guest "-k https://192.168.70.1:8760/health")
echo "  guest -> its router address :8760: ${g1:-no answer}; -> the home address :8760: ${g2:-no answer}"
if [ -n "$gnet" ] && [ "$gnet" != 000 ] && [ "$g1" != 200 ] && [ "$g2" != 200 ]; then check guest_cannot_reach_the_port 0; else check guest_cannot_reach_the_port 1; fi

say "--- models_zone_has_no_internet"
lnet=$(curlcode laptop "http://1.1.1.1/")
mnet=$(curlcode modelbox "http://1.1.1.1/")
echo "  control, laptop -> internet: ${lnet:-no answer}; model box -> internet: ${mnet:-no answer}"
if [ -n "$lnet" ] && [ "$lnet" != 000 ] && { [ -z "$mnet" ] || [ "$mnet" = 000 ]; }; then check models_zone_has_no_internet 0; else check models_zone_has_no_internet 1; fi

say "--- journal on r1"
j=$(f "$VL ssh r1 -- cat /tmp/llm-gateway/journal.log")
echo "$j" | sed 's/^/  /'
case "$j" in *E2E-PROMPT-MARKER*|*"stand-in answer"*|*"$KEY"*) say "FAIL journal holds content or a key"; failed=$((failed+1)) ;; esac

say "checks: $passed passed, $failed failed"
[ $((passed+failed)) -gt 0 ] || { echo "FAIL: measured nothing"; exit 1; }
[ "$failed" = 0 ]
