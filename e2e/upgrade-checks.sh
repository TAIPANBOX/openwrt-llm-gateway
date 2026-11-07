#!/bin/sh
# Runs on the router, in parts, over what scripts/vlab-upgrade.sh copied to /root/upg (on the
# overlay, because the reboot takes /tmp). Each part prints PASS or FAIL per check.
#
#   before    the released package, as an owner has it: a domain that is not lan, a client, a journal
#   upgrade   apk replaces it with this tree's package
#   journal   where a request went, once a device has made one
#   lowport   port 443 in the configuration
#   afterboot what a reboot brings back
#   ntp       the certificate nears its end on a gateway nobody restarted
#   removal   the package removed, then installed on a router that never had the account
U=/root/upg
J=/var/run/llm-gateway
INIT=/etc/init.d/llm-gateway
res() { if [ "$2" = 0 ]; then echo "PASS $1"; else echo "FAIL $1"; fi; }
fingerprint() { llm-gateway cert | sed -n 's/^SHA-256 fingerprint: //p'; }
sum() { sha256sum "$1" | cut -d' ' -f1; }
owner() { ls -ldn "$1" | awk '{print $1, $3, $4}'; }
# as_account <command>: run a command as the account, the way a daemon without root would be
as_account() { start-stop-daemon -S -c llm-gateway -x /bin/sh -- -c "$1"; }
# wait_for <seconds> <command>: the VM has no KVM on some hosts, and a start can take a while
wait_for() { n=$1; shift; while [ "$n" -gt 0 ]; do "$@" >/dev/null 2>&1 && return 0; sleep 1; n=$((n-1)); done; return 1; }
listening() { netstat -ltn | grep -q ":$1 "; }

part_before() {
	apk add --no-network --allow-untrusted "$U/old.apk" | tail -2
	uci set dhcp.@dnsmasq[0].domain='home.arpa'
	uci set dhcp.@dnsmasq[0].local='/home.arpa/'
	uci commit dhcp
	/etc/init.d/dnsmasq restart >/dev/null 2>&1
	uci -q batch <<-'UCI'
		set llm-gateway.main.enabled='1'
		set llm-gateway.standin=backend
		set llm-gateway.standin.url='http://127.0.0.1:9/v1'
		add_list llm-gateway.standin.model='stand-in'
		commit llm-gateway
	UCI
	llm-gateway client add laptop -model stand-in > "$U/client.txt"
	sed -n 's/^  key *\(llmgw_[A-Za-z0-9_-]*\)$/\1/p' "$U/client.txt" > "$U/key"
	rm "$U/client.txt"
	$INIT restart
	wait_for 40 listening 8760 || echo "the old gateway did not start"
	echo "old: $(apk info -vv 2>/dev/null | grep llm-gateway)"
	echo "old: pid $(pidof llm-gateway), $(grep ^Uid /proc/$(pidof llm-gateway)/status | tr '\t' ' ')"
	echo "old: CA $(fingerprint)"
	echo "old: account in /etc/passwd: $(grep -c '^llm-gateway:' /etc/passwd)"
	echo "old: journal directory: $(owner /tmp/llm-gateway)"
	fingerprint > "$U/ca.fp"
	sum /etc/config/llm-gateway > "$U/conf.sum"
	sum /etc/llm-gateway/ca.key > "$U/cakey.sum"
	pidof llm-gateway > "$U/old.pid"
	cp /etc/llm-gateway/ca.crt "$U/ca.crt"
}

part_upgrade() {
	apk add --no-network --allow-untrusted "$U/new.apk" > "$U/apk.out" 2>&1
	rc=$?
	sed 's/^/apk: /' "$U/apk.out"
	wait_for 40 listening 8760
	pid=$(pidof llm-gateway)
	echo "new: $(apk info -vv 2>/dev/null | grep llm-gateway)"
	echo "new: pid $pid, was $(cat "$U/old.pid"), $(grep ^Uid /proc/$pid/status | tr '\t' ' ')"
	echo "new: exe $(ls -l /proc/$pid/exe | sed 's/.*-> //')"
	echo "new: CA $(fingerprint)"
	echo "new: account $(grep '^llm-gateway:' /etc/passwd)"
	echo "new: group $(grep '^llm-gateway:' /etc/group)"
	for p in $J $J/snapshot.json $J/backend.keys $J/journal /etc/llm-gateway /etc/llm-gateway/ca.key /etc/llm-gateway/tls.key; do echo "new: $p: $(owner "$p")"; done
	echo "new: snapshot $(cat $J/snapshot.json)"
	logread -e llm-gateway | tail -8 | sed 's/^.*daemon\./log: /'

	r=1; [ "$rc" = 0 ] && grep -q 'post-upgrade' "$U/apk.out" && [ ! -e /tmp/llm-gateway ] && r=0
	res upgrade_ran_post_upgrade $r
	r=1; grep -q '^llm-gateway:x:[0-9]*:[0-9]*:.*:/var/empty:/bin/false$' /etc/passwd && grep -q '^llm-gateway:' /etc/group && r=0
	res upgrade_made_the_account $r
	# Still root in this step, the new binary and not the old one from memory.
	r=1; [ -n "$pid" ] && [ "$pid" != "$(cat "$U/old.pid")" ] && ! ls -l /proc/$pid/exe | grep -q deleted && grep -q '^Uid:[[:space:]]*0[[:space:]]' /proc/$pid/status && r=0
	res upgrade_restarted_the_daemon $r
	r=1; [ "$(fingerprint)" = "$(cat "$U/ca.fp")" ] && [ "$(sum /etc/llm-gateway/ca.key)" = "$(cat "$U/cakey.sum")" ] && r=0
	res upgrade_kept_the_ca $r
	r=1; [ "$(sum /etc/config/llm-gateway)" = "$(cat "$U/conf.sum")" ] && r=0
	res upgrade_kept_the_configuration $r
	r=1; jsonfilter -i $J/snapshot.json -e '@.name' | grep -qx 'llm.home.arpa' && [ "$(jsonfilter -i $J/snapshot.json -e '@.config_sha256')" = "$(cat "$U/conf.sum")" ] && r=0
	res prepare_left_the_snapshot $r

	# What a daemon without root will be able to do, tried as the account itself.
	gid=$(grep '^llm-gateway:' /etc/group | cut -d: -f3)
	reads=$(as_account "cat $J/snapshot.json" 2>/dev/null)
	as_account "echo planted >> $J/snapshot.json" 2>/dev/null
	as_account "echo planted > $J/planted" 2>/dev/null
	as_account "mv $J/snapshot.json $J/moved" 2>/dev/null
	as_account "cat /etc/llm-gateway/ca.key" 2>/dev/null | grep -q PRIVATE && stolen=yes
	as_account "echo written > $J/journal/probe" 2>/dev/null
	echo "as the account: read the snapshot: $([ -n "$reads" ] && echo yes || echo no); read ca.key: ${stolen:-no}; wrote in the journal's directory: $([ -e $J/journal/probe ] && echo yes || echo no)"
	r=1
	[ "$(owner $J)" = "drwxr-x--- 0 $gid" ] && [ "$(owner $J/snapshot.json)" = "-rw-r----- 0 $gid" ] &&
		[ -n "$reads" ] && [ -z "${stolen:-}" ] && [ ! -e $J/planted ] && [ ! -e $J/moved ] &&
		! grep -q planted $J/snapshot.json && [ -e $J/journal/probe ] && r=0
	rm -f $J/journal/probe
	res snapshot_directory_is_roots $r
}

part_journal() {
	echo "journal: $(owner $J/journal/journal.log), $(wc -l < $J/journal/journal.log) lines"
	r=1; [ ! -e /tmp/llm-gateway ] && [ -s $J/journal/journal.log ] && ! grep -qF "$(cat "$U/key")" $J/journal/journal.log && r=0
	res journal_moved_to_var_run $r
}

part_lowport() {
	uci set llm-gateway.main.port='443'
	uci commit llm-gateway
	seen=$(logread | grep -c 'below 1024')
	$INIT restart
	sleep 8
	now=$(logread | grep -c 'below 1024')
	logread | grep 'below 1024' | tail -1 | sed 's/^.*daemon\./log: /'
	echo "lines: $((now-seen)); daemon: ${pid:-$(pidof llm-gateway)}; firewall rule: $(uci -q get firewall.llm_gateway_lan.dest_port)"
	r=1; [ "$((now-seen))" = 1 ] && [ -z "$(pidof llm-gateway)" ] && [ "$(uci -q get firewall.llm_gateway_lan.dest_port)" != 443 ] && r=0
	res low_port_is_one_line $r
	uci set llm-gateway.main.port='8760'
	uci commit llm-gateway
	$INIT restart
	wait_for 40 listening 8760
}

part_afterboot() {
	wait_for 90 listening 8760
	echo "after boot: pid $(pidof llm-gateway), CA $(fingerprint)"
	echo "after boot: $(owner $J) $(owner $J/snapshot.json)"
	r=1; [ -n "$(pidof llm-gateway)" ] && [ "$(fingerprint)" = "$(cat "$U/ca.fp")" ] && [ -s $J/snapshot.json ] && r=0
	res reboot_brings_it_back $r
}

part_ntp() {
	/etc/init.d/sysntpd stop 2>/dev/null
	leaf=$(sum /etc/llm-gateway/tls.crt)
	ACTION=periodic stratum=3 /sbin/hotplug-call ntp
	sleep 4
	quiet=$(sum /etc/llm-gateway/tls.crt)
	real=$(date -u '+%Y-%m-%d %H:%M:%S')
	end=$(llm-gateway check 2>/dev/null | sed -n 's/^certificate until //p')
	echo "certificate until $end; a day past the start of the renewal window is $(date -u -d "$end 00:00:00" +%s 2>/dev/null)"
	# 29 days before the end: inside the window
	date -u -s "@$(( $(date -u -d "$end 00:00:00" +%s) - 29*86400 ))" >/dev/null
	echo "clock moved to $(date -u)"
	ACTION=periodic stratum=3 /sbin/hotplug-call ntp
	changed() { [ "$(sum /etc/llm-gateway/tls.crt)" != "$leaf" ]; }
	wait_for 40 changed
	wait_for 40 listening 8760
	renewed=$(sum /etc/llm-gateway/tls.crt)
	echo "new end: $(llm-gateway check 2>/dev/null | sed -n 's/^certificate until //p'); CA $(fingerprint)"
	logread | grep 'near its end' | tail -1 | sed 's/^.*daemon\./log: /'
	r=1; [ "$quiet" = "$leaf" ] && [ "$renewed" != "$leaf" ] && [ "$(fingerprint)" = "$(cat "$U/ca.fp")" ] && r=0
	res ntp_hook_renews_the_certificate $r
	# Back to the real clock: the next prepare reissues the certificate for it.
	date -u -s "$real" >/dev/null
	$INIT restart
	wait_for 40 listening 8760
}

part_removal() {
	apk del --no-network llm-gateway 2>&1 | tail -2
	echo "removed: $J $([ -e $J ] && echo stays || echo gone); account $(grep -c '^llm-gateway:' /etc/passwd); rules $(uci -q show firewall | grep -c llm_gateway)"
	r=1; [ ! -e $J ] && grep -q '^llm-gateway:' /etc/passwd && [ "$(uci -q show firewall | grep -c llm_gateway)" = 0 ] && r=0
	res removal_takes_the_keys_copy_and_keeps_the_account $r
	# A router that never had the account: only post-install is there to make it.
	sed -i '/^llm-gateway:/d' /etc/passwd /etc/shadow /etc/group
	apk add --no-network --allow-untrusted "$U/new.apk" 2>&1 | tail -3
	uci set llm-gateway.main.enabled='1'
	uci commit llm-gateway
	$INIT restart
	wait_for 40 listening 8760
	echo "installed: account $(grep '^llm-gateway:' /etc/passwd); pid $(pidof llm-gateway); $(owner $J); enabled at boot: $($INIT enabled && echo yes || echo no)"
	r=1; grep -q '^llm-gateway:' /etc/passwd && [ -n "$(pidof llm-gateway)" ] && $INIT enabled && [ "$(fingerprint)" = "$(cat "$U/ca.fp")" ] && r=0
	res fresh_install_makes_the_account $r
}

part_"${1:?part}"
