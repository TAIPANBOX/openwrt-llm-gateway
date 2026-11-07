#!/bin/sh
# vlab-upgrade.sh -- this tree's package on a real OpenWrt 25.12 router, from an upgrade on: an apk
# replaces a running gateway the way it will on an owner's router, with a CA a device trusts, a
# client's key, a domain that is not lan and a backend with a key of its own, and then everything the
# daemon does runs in its jail, as its own user, under its own filter. It is what no test can show:
# that apk runs post-upgrade, what ujail really builds, what the kernel says about the daemon, and
# that a device's trust and key survive. The checks are in e2e/upgrade-checks.sh; the device is curl
# on forge, with the CA saved before the upgrade, because the router's own wget cannot be the device
# here.
#
#   OLD_APK=<older x86_64 package> NEW_APK=<CI's x86_64 package> scripts/vlab-upgrade.sh
#   scripts/vlab-upgrade.sh --selftest    the names of the checks, for gate-scenarios-bound.sh
#
# The router is a QEMU VM of its own on forge, from a fresh copy of vlab's image, with user-mode
# networking: it needs nothing of vlab but the image, so it runs while a lab of another session is up,
# and touches nothing on forge but its directory. Its host, 192.168.1.2 from inside, is the backend.
set -u
CHECKS='upgrade_ran_post_upgrade upgrade_made_the_account upgrade_restarted_the_daemon upgrade_kept_the_ca upgrade_kept_the_configuration prepare_left_the_snapshot snapshot_directory_is_roots daemon_runs_without_root every_thread_is_confined ca_key_is_not_in_the_jail only_the_journal_is_writable request_reaches_the_backend gateway_answers_after_the_upgrade journal_moved_to_var_run stream_and_journal_work_in_the_jail reload_keeps_the_daemon_confined respawn_comes_back_confined root_is_refused_by_hand root_is_refused_when_procd_keeps_it no_call_was_refused low_port_is_one_line reboot_brings_it_back ntp_hook_renews_the_certificate removal_takes_the_keys_copy_and_keeps_the_account fresh_install_makes_the_account'
if [ "${1:-}" = "--selftest" ]; then for c in $CHECKS; do echo "$c"; done; exit 0; fi

REPO=$(cd "$(dirname "$0")/.." && pwd)
FORGE=${FORGE:-yurii@192.168.1.10}
RD=/var/tmp/llmgw-router
IMAGE=/home/yurii/vlab/images/openwrt-25.12.5-x86-64-generic-ext4-combined-efi.img
OLD_APK=${OLD_APK:-}
NEW_APK=${NEW_APK:-}
NAME=llm.home.arpa
SSH_PORT=22922 HTTPS_PORT=28760 BACKEND_PORT=18080 BACKEND_IP=192.168.1.2
ROUTER="ssh -p $SSH_PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o LogLevel=ERROR -o ConnectTimeout=8 root@127.0.0.1"
f() { ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 "$FORGE" "$@"; }
say() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*"; }

[ -f "$OLD_APK" ] && [ -f "$NEW_APK" ] || { echo "FAIL: measured nothing: OLD_APK and NEW_APK must name the two packages"; exit 1; }

out=$(mktemp)
cleanup() {
	rm -f "$out"
	say "take the router down"
	f "[ -f $RD/vm.pid ] && kill \$(cat $RD/vm.pid); [ -f $RD/standin.pid ] && kill \$(cat $RD/standin.pid); sleep 1; rm -rf $RD"
}
trap cleanup EXIT

f "[ ! -e $RD ]" || { trap - EXIT; say "FAIL: $RD is there: a run is under way, or one left it"; exit 1; }
say "router: a fresh copy of the image, and the backend"
f "mkdir -p $RD && cp $IMAGE $RD/r.img" || exit 1
f "cat > $RD/standin.py" < "$REPO/e2e/standin.py" || exit 1
f "cd $RD && (nohup python3 standin.py 127.0.0.1 $BACKEND_PORT lab-upstream-key >/dev/null 2>&1 & echo \$! > standin.pid)"
f "cd $RD && qemu-system-x86_64 -name llmgw-router -accel tcg,thread=multi -machine q35 -cpu max -smp 2 -m 384 -display none -bios /usr/share/ovmf/OVMF.fd -drive file=r.img,format=raw,if=virtio -nic user,model=virtio-net-pci,net=192.168.1.0/24,host=$BACKEND_IP,hostfwd=tcp:127.0.0.1:$SSH_PORT-192.168.1.1:22,hostfwd=tcp:127.0.0.1:$HTTPS_PORT-192.168.1.1:8760 -serial file:console.log -pidfile vm.pid -daemonize" || exit 1
n=0; until f "timeout 12 $ROUTER true" 2>/dev/null; do n=$((n+1)); [ "$n" -lt 60 ] || { say "FAIL: the router did not come up"; exit 1; }; sleep 5; done

r() { f "$ROUTER $*"; }
part() { f "$ROUTER 'BACKEND_IP=$BACKEND_IP BACKEND_PORT=$BACKEND_PORT sh -s $1'" < "$REPO/e2e/upgrade-checks.sh"; }

say "router: $(r 'cat /etc/openwrt_release' | sed -n "s/^DISTRIB_DESCRIPTION='\(.*\)'$/\1/p")"
say "router: the two packages into /root/upg"
r 'mkdir -p /root/upg' || exit 1
f "$ROUTER 'cat > /root/upg/old.apk'" < "$OLD_APK" || exit 1
f "$ROUTER 'cat > /root/upg/new.apk'" < "$NEW_APK" || exit 1

say "--- before: the older package, as an owner has it"
part before | tee -a "$out"
KEY=$(r 'cat /root/upg/key')
r 'cat /root/upg/ca.crt' | f "cat > $RD/ca.crt"
curlv="curl -s -m 25 --cacert $RD/ca.crt --resolve $NAME:$HTTPS_PORT:127.0.0.1"
# code <key or nothing>: what a device that trusts the saved CA gets for /v1/models, by the name
code() { f "$curlv -o /dev/null -w '%{http_code}' ${1:+-H 'Authorization: Bearer $1'} https://$NAME:$HTTPS_PORT/v1/models"; }
# chat <key> [stream]: a chat through to the backend
chat() { f "$curlv -N -H 'Authorization: Bearer $1' -H 'Content-Type: application/json' -d '{\"model\":\"stand-in\",\"stream\":${2:-false},\"messages\":[{\"role\":\"user\",\"content\":\"PROMPT-MARKER\"}]}' https://$NAME:$HTTPS_PORT/v1/chat/completions"; }
say "  a device that trusted the old CA, with its key: $(code "$KEY"), without: $(code "")"
say "  through to the backend: $(chat "$KEY" | cut -c1-120)"

say "--- upgrade: apk replaces it"
part upgrade | tee -a "$out"
withkey=$(code "$KEY"); nokey=$(code "")
say "  the same device, the same key: $withkey, without it: $nokey"
if [ "$withkey" = 200 ] && [ "$nokey" = 401 ]; then echo "PASS gateway_answers_after_the_upgrade" | tee -a "$out"; else echo "FAIL gateway_answers_after_the_upgrade" | tee -a "$out"; fi

say "--- jail: what the daemon is and what it can see"
part jail | tee -a "$out"

say "--- a request through the jail to the backend, by a name dnsmasq answers, with the backend's key"
ans=$(chat "$KEY")
echo "  chat: $(echo "$ans" | cut -c1-150)"
case "$ans" in *"stand-in answer"*) echo "PASS request_reaches_the_backend" | tee -a "$out" ;; *) echo "FAIL request_reaches_the_backend" | tee -a "$out" ;; esac
part journal | tee -a "$out"

say "--- a stream, and the journal past its limit"
part prefill | tee -a "$out"
sse=$(chat "$KEY" true)
echo "  stream: $(echo "$sse" | grep -c '^data:') events, last: $(echo "$sse" | grep '^data:' | tail -1)"
for i in 1 2 3 4 5 6; do chat "$KEY" >/dev/null; done
case "$sse" in *"stand-in answer"*"[DONE]"*) part rotated | tee -a "$out" ;; *) echo "FAIL stream_and_journal_work_in_the_jail" | tee -a "$out" ;; esac

say "--- reload"
part reload | tee -a "$out"
say "  the old key: $(code "$KEY"), the new key: $(code "$(r 'cat /root/upg/key2')")"

say "--- respawn: the daemon killed"
part respawn | tee -a "$out"
say "  after the respawn: $(code "$KEY")"

say "--- root"
part root | tee -a "$out"
say "  after the account's daemon is back: $(code "$KEY")"

say "--- was any call refused?"
part refused | tee -a "$out"

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
