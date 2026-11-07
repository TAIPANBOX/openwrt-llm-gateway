#!/bin/sh
# Build an apk v3 package for OpenWrt 25.12 and later.
#
# An apk v3 package is a binary ADB blob, and exactly one program writes it: apk-tools v3, via
# `apk mkpkg`, which is also the last step of OpenWrt's own include/package-pack.mk. A static Go
# binary needs nothing else from the SDK, so this runs that one command, from the host's apk if
# it is v3, else from a pinned alpine:edge image (apk-tools 3). The approach and the pinned image
# are the ones openwrt-mcp's mkapk.sh uses, verified there in OpenWrt's rootfs on 2026-09-08.
#
# Usage: ./mkapk.sh <binary> <version> [apk-arch]     (apk-arch: `cat /etc/apk/arch` on the target)
set -eu

BIN=${1:?usage: mkapk.sh <binary> <version> [arch]}
VERSION=${2:?usage: mkapk.sh <binary> <version> [arch]}
ARCH=${3:-aarch64_cortex-a53}
# r2: the r1 packages named every entry nobody:nobody (see run_mkpkg); the same version with other
# owners would be a second package under one name.
# r3: `llm-gateway prepare`, the account, the journal under /var/run, post-upgrade, the ntp hook.
PKGREL=${PKGREL:-3}
PKG=llm-gateway
DESCRIPTION="The house model servers behind one name on the router: a key per client, HTTPS under a name-constrained house CA, zone rules and a journal without content."
OUT="${PKG}-${VERSION}-r${PKGREL}.apk"
ALPINE=${ALPINE:-alpine@sha256:020dfcbaaf4cc1078bf2d9c7ba31a8466e334061dcd2f248001d68f79e52c000}

SRC=$(cd "$(dirname "$0")" && pwd)
BUILD=$(mktemp -d)
trap 'rm -rf "$BUILD"' EXIT

DATA="$BUILD/root"
mkdir -p "$DATA/usr/bin" "$DATA/etc/init.d" "$DATA/etc/config" "$DATA/etc/hotplug.d/ntp" "$DATA/lib/upgrade/keep.d"
install -m 0755 "$BIN"                                     "$DATA/usr/bin/$PKG"
install -m 0755 "$SRC/files/etc/init.d/$PKG"               "$DATA/etc/init.d/$PKG"
install -m 0644 "$SRC/files/etc/config/$PKG"               "$DATA/etc/config/$PKG"
install -m 0644 "$SRC/files/etc/hotplug.d/ntp/50-$PKG"     "$DATA/etc/hotplug.d/ntp/50-$PKG"
install -m 0644 "$SRC/files/lib/upgrade/keep.d/$PKG"       "$DATA/lib/upgrade/keep.d/$PKG"

# post-install runs on a fresh install only. apk runs post-upgrade, never post-install, when it
# replaces a version (apk-tools database.c), so what an upgrade needs has to be in post-upgrade as
# well. The service stays off until the owner enables it, so enabling the init script starts
# nothing yet.
cat > "$BUILD/post-install" <<EOT
#!/bin/sh
/etc/init.d/$PKG account
/etc/init.d/$PKG enable
/etc/init.d/$PKG restart
exit 0
EOT
# post-upgrade never enables: an owner who switched the start at boot off keeps it off. It restarts
# a gateway that runs or is meant to run, or the old binary would carry on from memory until the
# next reboot, and the change would arrive then, possibly far from home. A journal of r2 or earlier
# is left in /tmp; the restart is what stops anything writing to it.
cat > "$BUILD/post-upgrade" <<EOT
#!/bin/sh
/etc/init.d/$PKG account
if /etc/init.d/$PKG enabled || /etc/init.d/$PKG running; then
	/etc/init.d/$PKG restart
fi
rm -rf /tmp/$PKG
exit 0
EOT
# Removal takes back what the init script added to other packages' configuration, and the copy of
# the backends' keys. The account stays: see llmgw_account in the init script.
cat > "$BUILD/pre-deinstall" <<'EOT'
#!/bin/sh
/etc/init.d/llm-gateway stop
/etc/init.d/llm-gateway disable
rm -rf /var/run/llm-gateway
for s in $(uci -q show firewall | sed -n 's/^firewall\.\(llm_gateway_[a-z0-9_]*\)=rule$/\1/p'); do
	uci -q delete "firewall.$s"
done
uci -q delete dhcp.llm_gateway
uci commit firewall
uci commit dhcp
/etc/init.d/firewall reload >/dev/null 2>&1
/etc/init.d/dnsmasq reload >/dev/null 2>&1
exit 0
EOT
chmod 0755 "$BUILD/post-install" "$BUILD/post-upgrade" "$BUILD/pre-deinstall"

# MKAPK_STAGE=<dir> stages the tree and the scripts there with a run-mkpkg.sh that holds the one
# command, and stops: the vlab run builds the package inside the OpenWrt router itself, whose apk
# is v3, so no other tooling is needed anywhere.
if [ -n "${MKAPK_STAGE:-}" ]; then
	mkdir -p "$MKAPK_STAGE"
	cp -R "$DATA" "$MKAPK_STAGE/root"
	cp "$BUILD/post-install" "$BUILD/post-upgrade" "$BUILD/pre-deinstall" "$MKAPK_STAGE/"
	# Run as root in the pinned image: the tree is copied and given to root first, for the reason
	# run_mkpkg below gives, and the bind-mounted original is left to its owner.
	{
		echo '#!/bin/sh'
		echo 'set -e; cd "$(dirname "$0")"; t=$(mktemp -d); cp -a root "$t/root"; chown -R 0:0 "$t/root"'
		echo 'apk mkpkg \\'
		echo "	--info 'name:$PKG' --info 'version:$VERSION-r$PKGREL' --info 'arch:$ARCH' \\"
		echo "	--info 'license:MIT' --info 'origin:$PKG' --info 'url:https://github.com/TAIPANBOX/openwrt-llm-gateway' \\"
		echo "	--info 'maintainer:TAIPANBOX' --info 'depends:libc ubus jsonfilter' \\"
		echo "	--info 'description:$DESCRIPTION' \\"
		echo "	--script post-install:post-install --script post-upgrade:post-upgrade --script pre-deinstall:pre-deinstall \\"
		echo "	--files \"\$t/root\" --output $OUT && rm -rf \"\$t\" && echo $OUT"
	} > "$MKAPK_STAGE/run-mkpkg.sh"
	chmod 0755 "$MKAPK_STAGE/run-mkpkg.sh"
	echo "staged in $MKAPK_STAGE"
	exit 0
fi

# An apk names the owner of every entry, and `apk mkpkg` has no option to set it: apk-tools 3.0.7
# takes the uid and gid of each file and directory of the tree and names them from the passwd and
# group files of the system it runs on, writing nobody for an id it cannot name. On a CI runner
# the tree belongs to the runner's user (uid 1001), which the alpine image does not know, so every
# entry of the r1 packages was nobody:nobody, /usr/bin/llm-gateway and /etc/init.d/llm-gateway
# (both run as root) included. So mkpkg reads a copy of the tree owned by root, and check_owners
# reads the result back. A host apk is used only as root, the one case where the tree is root's
# already; anything else runs in the pinned image, as root there, on a copy made inside it.
if [ "$(id -u)" = 0 ] && command -v apk >/dev/null 2>&1 && apk mkpkg --help >/dev/null 2>&1; then
	in_writer() { "$@"; }
elif command -v docker >/dev/null 2>&1; then
	in_writer() { docker run --rm -v "$BUILD:$BUILD" -w "$BUILD" "$ALPINE" "$@"; }
else
	echo "mkapk.sh: need apk-tools v3 on PATH and root, or docker to run $ALPINE" >&2
	exit 1
fi

run_mkpkg() {
	# shellcheck disable=SC2016 # expanded by the inner shell, which gets the paths as arguments
	in_writer sh -c 'set -e; src=$1; shift; t=$(mktemp -d); cp -a "$src/." "$t/"; chown -R 0:0 "$t"; apk mkpkg --files "$t" "$@"; rm -rf "$t"' \
		sh "$DATA" "$@"
}

# check_owners <package>: fails unless every entry, files and directories, is root's as apk lists it.
check_owners() {
	in_writer apk adbdump "$1" > "$BUILD/adbdump.txt"
	acls=$(grep -cE '^[[:space:]]*(- )?acl:' "$BUILD/adbdump.txt" || true)
	users=$(grep -cE '^[[:space:]]*user: root$' "$BUILD/adbdump.txt" || true)
	groups=$(grep -cE '^[[:space:]]*group: root$' "$BUILD/adbdump.txt" || true)
	if [ "$acls" -eq 0 ] || [ "$users" -ne "$acls" ] || [ "$groups" -ne "$acls" ]; then
		echo "mkapk.sh: $(basename "$1"): $acls entries, $users owned by user root, $groups by group root; every entry must be root's:" >&2
		grep -E '^[[:space:]]*(user|group):' "$BUILD/adbdump.txt" | grep -v ': root$' | sort | uniq -c >&2 || true
		exit 1
	fi
}

run_mkpkg \
	--info "name:$PKG" \
	--info "version:$VERSION-r$PKGREL" \
	--info "arch:$ARCH" \
	--info "license:MIT" \
	--info "origin:$PKG" \
	--info "url:https://github.com/TAIPANBOX/openwrt-llm-gateway" \
	--info "maintainer:TAIPANBOX" \
	--info "description:$DESCRIPTION" \
	--info "depends:libc ubus jsonfilter" \
	--script "post-install:$BUILD/post-install" \
	--script "post-upgrade:$BUILD/post-upgrade" \
	--script "pre-deinstall:$BUILD/pre-deinstall" \
	--output "$BUILD/$OUT"
check_owners "$BUILD/$OUT"
sh "$SRC/scripts/gate-package-scripts.sh" "$BUILD/adbdump.txt"

rm -f "$SRC/$OUT"
cp "$BUILD/$OUT" "$SRC/$OUT"
echo "$OUT"
