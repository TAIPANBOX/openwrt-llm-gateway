#!/bin/sh
# Runs on the router, in parts, over what scripts/vlab-upgrade.sh copied to /root/upg (on the
# overlay, because the reboot takes /tmp). Each part prints PASS or FAIL per check.
#
#   before    the older package, as an owner has it: a domain that is not lan, a client, a journal
#   upgrade   apk replaces it with this tree's package
#   jail      what the daemon is, and what it can see, by the kernel's word
#   journal   where a request went, once a device has made one
#   prefill   the journal just short of its limit
#   rotated   the requests since have rotated it
#   reload    a new client and a reload
#   respawn   the daemon killed
#   root      serve as root, by hand, and from a procd that was told of a user that is not there
#   controls  the same daemon without the capabilities file, and without ujail
#   refused   no call of the daemon's was ever refused
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
# as_account <command>: run a command as the account, the way the daemon runs
as_account() { start-stop-daemon -S -c llm-gateway -x /bin/sh -- -c "$1"; }
# wait_for <seconds> <command>: the VM has no KVM on some hosts, and a start can take a while
wait_for() { n=$1; shift; while [ "$n" -gt 0 ]; do "$@" >/dev/null 2>&1 && return 0; sleep 1; n=$((n-1)); done; return 1; }
listening() { netstat -ltn | grep -q ":$1 "; }
# pidof names ujail too, under the name it was given. The daemon is the one that is not root; an
# older package's daemon, which is root, is the only one there is.
daemon_pid() {
	for p in $(pidof llm-gateway); do
		grep -q '^Uid:[[:space:]]*0[[:space:]]' "/proc/$p/status" 2>/dev/null || { echo "$p"; return; }
	done
	pidof llm-gateway | cut -d' ' -f1
}
confined() { # pid: the kernel's word on one process or thread
	acct=$(grep '^llm-gateway:' /etc/passwd | cut -d: -f3)
	s=/proc/$1/status
	grep -q "^Uid:[[:space:]]*$acct[[:space:]]$acct[[:space:]]$acct[[:space:]]$acct\$" "$s" || return 1
	for c in CapInh CapPrm CapEff CapBnd CapAmb; do grep -q "^$c:[[:space:]]*0000000000000000\$" "$s" || return 1; done
	grep -q '^NoNewPrivs:[[:space:]]*1$' "$s" && grep -q '^Seccomp:[[:space:]]*2$' "$s"
}

part_before() {
	apk add --no-network --allow-untrusted "$U/old.apk" 2>&1 | grep -v 'opening from cache' | tail -2
	uci set dhcp.@dnsmasq[0].domain='home.arpa'
	uci set dhcp.@dnsmasq[0].local='/home.arpa/'
	uci add_list dhcp.@dnsmasq[0].address='/standin.home.arpa/'"$BACKEND_IP"
	uci commit dhcp
	/etc/init.d/dnsmasq restart >/dev/null 2>&1
	echo 'lab-upstream-key' > /etc/llm-gateway-upstream.key
	chmod 0600 /etc/llm-gateway-upstream.key
	uci -q batch <<-UCI
		set llm-gateway.main.enabled='1'
		set llm-gateway.standin=backend
		set llm-gateway.standin.url='http://standin.home.arpa:$BACKEND_PORT/v1'
		set llm-gateway.standin.key_file='/etc/llm-gateway-upstream.key'
		add_list llm-gateway.standin.model='stand-in'
		commit llm-gateway
	UCI
	llm-gateway client add laptop -model stand-in > "$U/client.txt"
	sed -n 's/^  key *\(llmgw_[A-Za-z0-9_-]*\)$/\1/p' "$U/client.txt" > "$U/key"
	rm "$U/client.txt"
	$INIT restart
	wait_for 60 listening 8760 || echo "the old gateway did not start"
	pid=$(daemon_pid)
	echo "old: $(apk info -vv 2>/dev/null | grep llm-gateway | cut -d' ' -f1)"
	echo "old: pid $pid, $(grep ^Uid /proc/$pid/status | tr '\t' ' ')"
	echo "old: CA $(fingerprint)"
	echo "old: account in /etc/passwd: $(grep -c '^llm-gateway:' /etc/passwd)"
	fingerprint > "$U/ca.fp"
	sum /etc/config/llm-gateway > "$U/conf.sum"
	sum /etc/llm-gateway/ca.key > "$U/cakey.sum"
	echo "$pid" > "$U/old.pid"
	cp /etc/llm-gateway/ca.crt "$U/ca.crt"
}

part_upgrade() {
	apk add --no-network --allow-untrusted "$U/new.apk" 2>&1 | grep -v 'opening from cache' > "$U/apk.out"
	sed 's/^/apk: /' "$U/apk.out"
	wait_for 60 listening 8760
	pid=$(daemon_pid)
	echo "new: $(apk info -vv 2>/dev/null | grep llm-gateway | cut -d' ' -f1)"
	echo "new: pid $pid, was $(cat "$U/old.pid"), $(grep ^Uid /proc/$pid/status | tr '\t' ' ')"
	echo "new: exe $(ls -l /proc/$pid/exe | sed 's/.*-> //')"
	echo "new: CA $(fingerprint)"
	echo "new: account $(grep '^llm-gateway:' /etc/passwd)"
	echo "new: group $(grep '^llm-gateway:' /etc/group)"
	for p in $J $J/snapshot.json $J/backend.keys $J/journal /etc/llm-gateway /etc/llm-gateway/ca.key /etc/llm-gateway/tls.key; do echo "new: $p: $(owner "$p")"; done
	echo "new: snapshot $(cat $J/snapshot.json)"
	logread -e llm-gateway | tail -5 | sed 's/^.*daemon\./log: /' | cut -c1-150

	r=1; grep -q 'post-upgrade' "$U/apk.out" && grep -q 'OK:' "$U/apk.out" && [ ! -e /tmp/llm-gateway ] && r=0
	res upgrade_ran_post_upgrade $r
	r=1; grep -q '^llm-gateway:x:[0-9]*:[0-9]*:.*:/var/empty:/bin/false$' /etc/passwd && grep -q '^llm-gateway:' /etc/group && r=0
	res upgrade_made_the_account $r
	# The new binary and not the old one from memory, and no longer root.
	r=1; [ -n "$pid" ] && [ "$pid" != "$(cat "$U/old.pid")" ] && ! ls -l /proc/$pid/exe | grep -q deleted && ! grep -q '^Uid:[[:space:]]*0[[:space:]]' /proc/$pid/status && r=0
	res upgrade_restarted_the_daemon $r
	r=1; [ "$(fingerprint)" = "$(cat "$U/ca.fp")" ] && [ "$(sum /etc/llm-gateway/ca.key)" = "$(cat "$U/cakey.sum")" ] && r=0
	res upgrade_kept_the_ca $r
	r=1; [ "$(sum /etc/config/llm-gateway)" = "$(cat "$U/conf.sum")" ] && r=0
	res upgrade_kept_the_configuration $r
	r=1; jsonfilter -i $J/snapshot.json -e '@.name' | grep -qx 'llm.home.arpa' && [ "$(jsonfilter -i $J/snapshot.json -e '@.config_sha256')" = "$(cat "$U/conf.sum")" ] && r=0
	res prepare_left_the_snapshot $r

	# What the daemon can do, tried as the account itself.
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

part_jail() {
	pid=$(daemon_pid)
	echo "procd: $(apk info -vv 2>/dev/null | grep '^procd-[0-9]' | cut -d' ' -f1)"
	grep -E '^(Name|Uid|Gid|Groups|CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs|Seccomp|Seccomp_filters):' /proc/$pid/status | sed 's/^/status: /' | tr '\t' ' '
	r=1; confined "$pid" && r=0
	res daemon_runs_without_root $r
	threads=0; bad=0
	for t in /proc/$pid/task/*; do
		threads=$((threads+1))
		confined "$pid/task/${t##*/}" || bad=$((bad+1))
	done
	echo "threads: $threads, of which not confined: $bad"
	r=1; [ "$threads" -gt 1 ] && [ "$bad" = 0 ] && r=0
	res every_thread_is_confined $r

	root=/proc/$pid/root
	(cd "$root" && find etc var tmp usr -type f -o -type l 2>/dev/null) | sort | sed 's/^/jail holds: /'
	r=1
	[ -e "$root/etc/llm-gateway/tls.key" ] && [ ! -e "$root/etc/llm-gateway/ca.key" ] && [ ! -e "$root/etc/config/dhcp" ] &&
		[ ! -e "$root/etc/config/firewall" ] && [ ! -e "$root/etc/config/network" ] &&
		[ -z "$(cd "$root" && find etc var tmp usr -name 'ca.key' 2>/dev/null)" ] && r=0
	res ca_key_is_not_in_the_jail $r

	awk '{print "mount: " $2, $3, $4}' /proc/$pid/mounts | sort -u
	r=1
	[ "$(awk '$4 ~ /^rw/ && $2 !~ /^\/(proc|dev)(\/|$)/ {print $2}' /proc/$pid/mounts)" = "$J/journal" ] && r=0
	res only_the_journal_is_writable $r
}

part_journal() {
	echo "journal: $(owner $J/journal/journal.log), $(wc -l < $J/journal/journal.log) lines"
	r=1; [ ! -e /tmp/llm-gateway ] && [ -s $J/journal/journal.log ] && ! grep -qF "$(cat "$U/key")" $J/journal/journal.log && r=0
	res journal_moved_to_var_run $r
}

part_prefill() {
	$INIT stop
	head -c 1048300 /dev/zero | tr '\0' 'x' > $J/journal/journal.log
	$INIT start
	wait_for 60 listening 8761
	echo "journal filled: $(owner $J/journal/journal.log), $(wc -c < $J/journal/journal.log) bytes"
}

part_rotated() {
	echo "journal: $(ls -ln $J/journal | awk 'NF > 8 {print $1, $3, $5, $9}' | tr '\n' ';')"
	r=1; [ -s $J/journal/journal.log.1 ] && [ "$(wc -c < $J/journal/journal.log)" -lt 100000 ] && grep -q '"client":"laptop"' $J/journal/journal.log && confined "$(daemon_pid)" && r=0
	res stream_and_journal_work_in_the_jail $r
}

part_reload() {
	before=$(daemon_pid)
	llm-gateway client add phone -model stand-in > "$U/client2.txt"
	sed -n 's/^  key *\(llmgw_[A-Za-z0-9_-]*\)$/\1/p' "$U/client2.txt" > "$U/key2"
	rm "$U/client2.txt"
	$INIT reload
	sleep 2
	wait_for 60 listening 8761
	pid=$(daemon_pid)
	echo "reload: pid $before -> $pid"
	r=1; [ -n "$pid" ] && [ "$pid" != "$before" ] && confined "$pid" && r=0
	res reload_keeps_the_daemon_confined $r
}

part_respawn() {
	before=$(daemon_pid)
	kill -9 "$before"
	gone() { ! grep -q llm-gateway "/proc/$before/comm" 2>/dev/null; }
	wait_for 10 gone
	back() { p=$(daemon_pid); [ -n "$p" ] && [ "$p" != "$before" ] && listening 8761; }
	wait_for 40 back
	pid=$(daemon_pid)
	echo "respawn: pid $before -> $pid"
	r=1; [ -n "$pid" ] && [ "$pid" != "$before" ] && confined "$pid" && r=0
	res respawn_comes_back_confined $r
}

part_root() {
	out=$(/usr/bin/llm-gateway serve 2>&1); rc=$?
	echo "by hand: exit $rc: $out"
	r=1; [ "$rc" = 1 ] && [ "$(echo "$out" | wc -l)" = 1 ] && echo "$out" | grep -q 'refusing to run as root' && r=0
	res root_is_refused_by_hand $r

	# procd, told of a user that does not exist, says nothing and starts the daemon as root.
	cp $INIT "$U/init.saved"
	sed -i 's/procd_set_param user llm-gateway/procd_set_param user no-such-user/; s/procd_set_param group llm-gateway/procd_set_param group no-such-user/' $INIT
	seen=$(logread | grep -c 'refusing to run as root')
	$INIT restart
	refused() { [ "$(logread | grep -c 'refusing to run as root')" -gt "$seen" ]; }
	wait_for 30 refused
	logread | grep 'refusing to run as root' | tail -1 | sed 's/^.*daemon\./log: /'
	r=1; refused && ! listening 8761 && ! listening 8760 && r=0
	cp "$U/init.saved" $INIT
	res root_is_refused_when_procd_keeps_it $r
	$INIT restart
	wait_for 60 listening 8761
}

# A zero means something only beside what it is without the thing that makes it zero, so these take
# each away in turn and read the kernel again.
part_controls() {
	cp $INIT "$U/init.saved"
	cp /etc/capabilities/llm-gateway.json "$U/caps.saved"
	bnd() { sed -n 's/^CapBnd:[[:space:]]*//p' "/proc/$(daemon_pid)/status"; }
	restart_up() { $INIT restart; sleep 1; wait_for 40 listening 8761; }
	asked=$(bnd)
	sed -i '/procd_set_param capabilities/d' $INIT
	restart_up
	nofile=$(bnd)
	cp "$U/init.saved" $INIT
	sed -i '/"bounding"/d' /etc/capabilities/llm-gateway.json
	restart_up
	noset=$(bnd)
	cp "$U/caps.saved" /etc/capabilities/llm-gateway.json
	restart_up
	back=$(bnd)
	echo "CapBnd: as shipped $asked; with no capabilities file $nofile; with a file that leaves bounding out $noset; as shipped again $back"
	r=1; [ "$asked" = 0000000000000000 ] && [ "$back" = 0000000000000000 ] && [ "$nofile" != 0000000000000000 ] && [ "$noset" != 0000000000000000 ] && r=0
	res capabilities_file_empties_the_bounding_set $r

	mv /sbin/ujail /sbin/ujail.saved
	$INIT restart
	sleep 6
	up=no; listening 8761 && up=yes
	running=$(pidof llm-gateway)
	line=$(dmesg | grep -i 'llm-gateway' | tail -1)
	mv /sbin/ujail.saved /sbin/ujail
	echo "without ujail: listening: $up; processes: [$running]; kernel log: $line"
	r=1; [ "$up" = no ] && [ -z "$running" ] && r=0
	res no_ujail_no_start $r
	$INIT restart
	wait_for 60 listening 8761
}

part_refused() {
	killed=$(logread | grep -c 'exited with signal: 31')
	audit=$(dmesg | grep -c 'type=1326')
	echo "killed by SIGSYS: $killed; kernel seccomp records: $audit; daemon $(daemon_pid)"
	r=1; [ "$killed" = 0 ] && [ "$audit" = 0 ] && confined "$(daemon_pid)" && r=0
	res no_call_was_refused $r
}

part_lowport() {
	uci set llm-gateway.main.port='443'
	uci commit llm-gateway
	seen=$(logread | grep -c 'below 1024')
	$INIT restart
	sleep 8
	now=$(logread | grep -c 'below 1024')
	logread | grep 'below 1024' | tail -1 | sed 's/^.*daemon\./log: /'
	echo "lines: $((now-seen)); daemon: $(pidof llm-gateway); firewall rule: $(uci -q get firewall.llm_gateway_lan.dest_port)"
	r=1; [ "$((now-seen))" = 1 ] && [ -z "$(pidof llm-gateway)" ] && [ "$(uci -q get firewall.llm_gateway_lan.dest_port)" != 443 ] && r=0
	res low_port_is_one_line $r
	uci set llm-gateway.main.port='8760'
	uci commit llm-gateway
	$INIT restart
	wait_for 60 listening 8760
}

part_afterboot() {
	wait_for 120 listening 8760
	pid=$(daemon_pid)
	echo "after boot: pid $pid, CA $(fingerprint)"
	echo "after boot: $(owner $J) $(owner $J/snapshot.json)"
	r=1; [ -n "$pid" ] && confined "$pid" && [ "$(fingerprint)" = "$(cat "$U/ca.fp")" ] && [ -s $J/snapshot.json ] && r=0
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
	# 29 days before the end: inside the window
	date -u -s "@$(( $(date -u -d "$end 00:00:00" +%s) - 29*86400 ))" >/dev/null
	echo "certificate until $end; clock moved to $(date -u)"
	ACTION=periodic stratum=3 /sbin/hotplug-call ntp
	changed() { [ "$(sum /etc/llm-gateway/tls.crt)" != "$leaf" ]; }
	wait_for 60 changed
	wait_for 60 listening 8760
	renewed=$(sum /etc/llm-gateway/tls.crt)
	echo "new end: $(llm-gateway check 2>/dev/null | sed -n 's/^certificate until //p'); CA $(fingerprint)"
	logread | grep 'near its end' | tail -1 | sed 's/^.*daemon\./log: /'
	r=1; [ "$quiet" = "$leaf" ] && [ "$renewed" != "$leaf" ] && [ "$(fingerprint)" = "$(cat "$U/ca.fp")" ] && confined "$(daemon_pid)" && r=0
	res ntp_hook_renews_the_certificate $r
	# Back to the real clock: the next prepare reissues the certificate for it.
	date -u -s "$real" >/dev/null
	$INIT restart
	wait_for 60 listening 8760
}

part_removal() {
	apk del --no-network llm-gateway 2>&1 | grep -v 'opening from cache' | tail -2
	echo "removed: $J $([ -e $J ] && echo stays || echo gone); account $(grep -c '^llm-gateway:' /etc/passwd); rules $(uci -q show firewall | grep -c llm_gateway)"
	r=1; [ ! -e $J ] && grep -q '^llm-gateway:' /etc/passwd && [ "$(uci -q show firewall | grep -c llm_gateway)" = 0 ] && r=0
	res removal_takes_the_keys_copy_and_keeps_the_account $r
	# A router that never had the account: only post-install is there to make it.
	sed -i '/^llm-gateway:/d' /etc/passwd /etc/shadow /etc/group
	apk add --no-network --allow-untrusted "$U/new.apk" 2>&1 | grep -v 'opening from cache' | tail -3
	uci set llm-gateway.main.enabled='1'
	uci commit llm-gateway
	$INIT restart
	wait_for 60 listening 8760
	pid=$(daemon_pid)
	echo "installed: account $(grep '^llm-gateway:' /etc/passwd); pid $pid; $(owner $J); enabled at boot: $($INIT enabled && echo yes || echo no)"
	r=1; grep -q '^llm-gateway:' /etc/passwd && [ -n "$pid" ] && confined "$pid" && $INIT enabled && [ "$(fingerprint)" = "$(cat "$U/ca.fp")" ] && r=0
	res fresh_install_makes_the_account $r
}

part_"${1:?part}"
