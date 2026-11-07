#!/bin/sh
# vlab-upgrade.sh -- the upgrade from the released package to this tree's, on a real OpenWrt 25.12
# router: an apk replaces a running gateway the way it will on an owner's router, with a CA a device
# trusts, a client's key and a domain that is not lan, and then the router reboots. It is what no
# test can show: that apk runs post-upgrade, that the old binary is gone from memory, that the
# device's trust and key survive. The checks are in e2e/upgrade-checks.sh; the trust is checked from
# outside the router by curl with the CA saved before the upgrade, because the router's own wget
# cannot be the device here.
#
#   OLD_APK=<released x86_64 package> NEW_APK=<CI's x86_64 package> scripts/vlab-upgrade.sh
#   scripts/vlab-upgrade.sh --selftest    the names of the checks, for gate-scenarios-bound.sh
#
# In vlab on forge by default (e2e/feed.json), the lab taken down afterwards. ROUTER=<ssh command>
# and HTTPS_AT=<host:port> run the same checks on a router that is already up.
set -u
CHECKS='upgrade_ran_post_upgrade upgrade_made_the_account upgrade_restarted_the_daemon upgrade_kept_the_ca upgrade_kept_the_configuration prepare_left_the_snapshot snapshot_directory_is_roots gateway_answers_after_the_upgrade journal_moved_to_var_run low_port_is_one_line reboot_brings_it_back ntp_hook_renews_the_certificate removal_takes_the_keys_copy_and_keeps_the_account fresh_install_makes_the_account'
if [ "${1:-}" = "--selftest" ]; then for c in $CHECKS; do echo "$c"; done; exit 0; fi

REPO=$(cd "$(dirname "$0")/.." && pwd)
FORGE=${FORGE:-yurii@192.168.1.10}
RD=/var/tmp/llmgw-upgrade
VL="sudo /home/yurii/vlab/bin/vlab"
ROUTER=${ROUTER:-}
HTTPS_AT=${HTTPS_AT:-}
OLD_APK=${OLD_APK:-}
NEW_APK=${NEW_APK:-}
NAME=llm.home.arpa
f() { ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 "$FORGE" "$@"; }
say() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*"; }

[ -f "$OLD_APK" ] && [ -f "$NEW_APK" ] || { echo "FAIL: measured nothing: OLD_APK and NEW_APK must name the two packages"; exit 1; }

out=$(mktemp)
lab=no
cleanup() {
	rm -f "$out"
	[ "$lab" = yes ] || return 0
	say "take the lab down"
	f "$VL down >/dev/null 2>&1; rm -rf $RD; if $VL check clean >/dev/null 2>&1; then echo \"  vlab check clean: clean\"; else echo \"  vlab check clean: NOT clean\"; fi"
}
trap cleanup EXIT

if [ -z "$ROUTER" ]; then
	if f "$VL status" >/dev/null 2>&1; then say "FAIL: a lab is already up on forge; not touching it"; exit 1; fi
	f "rm -rf $RD && mkdir -p $RD/e2e" || exit 1
	COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -C "$REPO" -cf - e2e/feed.json | f "tar -xf - -C $RD" || exit 1
	lab=yes
	say "vlab up"
	f "cd /home/yurii/vlab && $VL up $RD/e2e/feed.json --ttl 45m" | tail -3 || exit 1
	ROUTER="$VL ssh r1 --"
fi
r() { f "$ROUTER $*"; }
part() { f "$ROUTER sh -s $1" < "$REPO/e2e/upgrade-checks.sh"; }

say "router: $(r 'cat /etc/openwrt_release' | sed -n "s/^DISTRIB_DESCRIPTION='\(.*\)'$/\1/p")"
say "router: the two packages into /root/upg"
r 'mkdir -p /root/upg' || exit 1
f "$ROUTER 'cat > /root/upg/old.apk'" < "$OLD_APK" || exit 1
f "$ROUTER 'cat > /root/upg/new.apk'" < "$NEW_APK" || exit 1

say "--- before: the released package, as an owner has it"
part before | tee -a "$out"
KEY=$(r 'cat /root/upg/key')
r 'cat /root/upg/ca.crt' | f "cat > /var/tmp/llmgw-upgrade-ca.crt"
[ -n "$HTTPS_AT" ] || HTTPS_AT="$(r "ubus call network.interface.lan status" | sed -n 's/.*"address": "\([0-9.]*\)".*/\1/p' | head -1):8760"
HOST=${HTTPS_AT%:*} PORT=${HTTPS_AT##*:}
# code <key or nothing>: what a device that trusts the saved CA gets for /v1/models, by the name
code() { f "curl -s -o /dev/null -w '%{http_code}' -m 15 --cacert /var/tmp/llmgw-upgrade-ca.crt --resolve $NAME:$PORT:$HOST ${1:+-H 'Authorization: Bearer $1'} https://$NAME:$PORT/v1/models"; }
say "  a device that trusted the old CA, with its key: $(code "$KEY"), without: $(code "")"

say "--- upgrade: apk replaces it"
part upgrade | tee -a "$out"
withkey=$(code "$KEY"); nokey=$(code "")
say "  the same device, the same key: $withkey, without it: $nokey"
if [ "$withkey" = 200 ] && [ "$nokey" = 401 ]; then echo "PASS gateway_answers_after_the_upgrade" | tee -a "$out"; else echo "FAIL gateway_answers_after_the_upgrade" | tee -a "$out"; fi
part journal | tee -a "$out"

say "--- lowport"
part lowport | tee -a "$out"

say "--- reboot"
r reboot
sleep 15
n=0; until r true 2>/dev/null; do n=$((n+1)); [ "$n" -lt 60 ] || break; sleep 5; done
part afterboot | tee -a "$out"
say "  after the reboot, the same device, the same key: $(code "$KEY")"

say "--- ntp: the certificate nears its end on a gateway nobody restarted"
part ntp | tee -a "$out"
say "  after the renewal, the same device, the same key: $(code "$KEY")"

say "--- removal, then a fresh install"
part removal | tee -a "$out"
say "  after the fresh install, the same device, the same key: $(code "$KEY")"

passed=0; failed=0
for c in $CHECKS; do
	if grep -qx "PASS $c" "$out"; then passed=$((passed+1)); say "PASS $c"
	else failed=$((failed+1)); say "FAIL $c"; fi
done
say "checks: $passed passed, $failed failed"
[ $((passed+failed)) -gt 0 ] || { echo "FAIL: measured nothing"; exit 1; }
[ "$failed" = 0 ]
