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
PKGREL=${PKGREL:-1}
PKG=llm-gateway
DESCRIPTION="The house model servers behind one name on the router: a key per client, HTTPS under a name-constrained house CA, zone rules and a journal without content."
OUT="${PKG}-${VERSION}-r${PKGREL}.apk"
ALPINE=${ALPINE:-alpine@sha256:020dfcbaaf4cc1078bf2d9c7ba31a8466e334061dcd2f248001d68f79e52c000}

SRC=$(cd "$(dirname "$0")" && pwd)
BUILD=$(mktemp -d)
trap 'rm -rf "$BUILD"' EXIT

DATA="$BUILD/root"
mkdir -p "$DATA/usr/bin" "$DATA/etc/init.d" "$DATA/etc/config" "$DATA/lib/upgrade/keep.d"
install -m 0755 "$BIN"                                "$DATA/usr/bin/$PKG"
install -m 0755 "$SRC/files/etc/init.d/$PKG"          "$DATA/etc/init.d/$PKG"
install -m 0644 "$SRC/files/etc/config/$PKG"          "$DATA/etc/config/$PKG"
install -m 0644 "$SRC/files/lib/upgrade/keep.d/$PKG"  "$DATA/lib/upgrade/keep.d/$PKG"

# post-install covers a fresh install and an upgrade. The service stays off until the owner
# enables it, so enabling the init script starts nothing yet.
cat > "$BUILD/post-install" <<EOT
#!/bin/sh
/etc/init.d/$PKG enable
/etc/init.d/$PKG restart
exit 0
EOT
# Removal takes back what the init script added to other packages' configuration.
cat > "$BUILD/pre-deinstall" <<'EOT'
#!/bin/sh
/etc/init.d/llm-gateway stop
/etc/init.d/llm-gateway disable
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
chmod 0755 "$BUILD/post-install" "$BUILD/pre-deinstall"

# MKAPK_STAGE=<dir> stages the tree and the scripts there with a run-mkpkg.sh that holds the one
# command, and stops: the vlab run builds the package inside the OpenWrt router itself, whose apk
# is v3, so no other tooling is needed anywhere.
if [ -n "${MKAPK_STAGE:-}" ]; then
	mkdir -p "$MKAPK_STAGE"
	cp -R "$DATA" "$MKAPK_STAGE/root"
	cp "$BUILD/post-install" "$BUILD/pre-deinstall" "$MKAPK_STAGE/"
	{
		echo '#!/bin/sh'
		echo 'cd "$(dirname "$0")" && apk mkpkg \\'
		echo "	--info 'name:$PKG' --info 'version:$VERSION-r$PKGREL' --info 'arch:$ARCH' \\"
		echo "	--info 'license:MIT' --info 'origin:$PKG' --info 'url:https://github.com/TAIPANBOX/openwrt-llm-gateway' \\"
		echo "	--info 'maintainer:TAIPANBOX' --info 'depends:libc ubus jsonfilter' \\"
		echo "	--info 'description:$DESCRIPTION' \\"
		echo "	--script post-install:post-install --script pre-deinstall:pre-deinstall \\"
		echo "	--files root --output $OUT && echo $OUT"
	} > "$MKAPK_STAGE/run-mkpkg.sh"
	chmod 0755 "$MKAPK_STAGE/run-mkpkg.sh"
	echo "staged in $MKAPK_STAGE"
	exit 0
fi

run_mkpkg() {
	if command -v apk >/dev/null 2>&1 && apk mkpkg --help >/dev/null 2>&1; then
		apk mkpkg "$@"
	elif command -v docker >/dev/null 2>&1; then
		docker run --rm -v "$BUILD:$BUILD" -w "$BUILD" "$ALPINE" apk mkpkg "$@"
	else
		echo "mkapk.sh: need apk-tools v3 on PATH or docker to run $ALPINE" >&2
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
	--script "pre-deinstall:$BUILD/pre-deinstall" \
	--files "$DATA" \
	--output "$BUILD/$OUT"

rm -f "$SRC/$OUT"
cp "$BUILD/$OUT" "$SRC/$OUT"
echo "$OUT"
