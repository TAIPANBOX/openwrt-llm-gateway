#!/bin/sh
# gate-feed.sh -- what a router is asked to trust is signed by this feed's key, and is what it says.
#
# Invariant: every package and every index of the feed verifies against keys/llm-gateway.pem and
# no other key; each package is llm-gateway, declares its directory's architecture and the
# version its file name says, and gives every entry to root; each index lists exactly the packages
# beside it, at their size; and the feed holds nothing else (gate-feed-tree.sh, run first).
#
# A feed whose packages verify against any key at all is not a signed feed: it is an unsigned one
# with a key file next to it, and an install would not tell the two apart. So apk-tools v3 checks
# every file here with this feed's public key as its only trusted key, in the alpine image mkapk.sh
# pins, on this Mac's Docker or with FEED_SIGN_ON=forge on forge (in_pinned_alpine in
# feed-common.sh). The feed goes in on standard input; nothing is written to the disk there. That a router refuses what fails here is shown on a real OpenWrt
# router by scripts/vlab-feed.sh.
#
#   ./scripts/gate-feed.sh                       feed-out, against keys/llm-gateway.pem
#   FEED=<dir> PUB_KEY=<pem> ./scripts/gate-feed.sh
#   ./scripts/gate-feed.sh --selftest            the check names
#
# Not in CI: the feed exists only where it was signed, on the Mac that holds the key.
# scripts/teeth-feed.sh plants this gate's faults; gates-have-teeth.sh plants gate-feed-tree.sh's.
set -eu

CHECKS='check_tree check_signed_by_the_feed_key check_arch_matches_its_directory check_entries_owned_by_root check_index_lists_the_packages'
if [ "${1:-}" = "--selftest" ]; then for c in $CHECKS; do echo "$c"; done; exit 0; fi

ROOT=$(cd "$(dirname "$0")/.." && pwd)
. "$ROOT/scripts/feed-common.sh"
FEED=${FEED:-$ROOT/feed-out}
PUB_KEY=${PUB_KEY:-$ROOT/keys/llm-gateway.pem}
export FEED PUB_KEY

# ---- check_tree
if ! out=$(sh "$ROOT/scripts/gate-feed-tree.sh" 2>&1); then
	echo "$out"
	case "$out" in *"measured nothing"*) exit 1 ;; esac
	echo "FAIL check_tree: gate-feed-tree.sh failed"
	exit 1
fi
echo "PASS check_tree ($(echo "$out" | tail -n 1))"

T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
{
	echo "RELEASE='$RELEASE'"
	cat <<'CONTAINER'
set -eu
fail() { echo "FAIL $1: $2"; exit 1; }
mkdir -p /w/in /w/trust
cd /w/in && tar -xf -
mv /w/in/trusted.pem /w/trust/llm-gateway.pem
dirs=$(ls -d /w/in/$RELEASE/*/)
s=0 a=0 o=0 i=0
for dir in $dirs; do
	arch=$(basename "$dir")
	cd "$dir"
	# ---- check_signed_by_the_feed_key
	for f in ./*.apk packages.adb; do
		f=${f#./}
		apk --keys-dir /w/trust verify "$f" > /w/verify.log 2>&1 ||
			fail check_signed_by_the_feed_key "$arch/$f: $(tr '\n' ' ' < /w/verify.log)"
		s=$((s + 1))
	done
	for f in ./*.apk; do
		f=${f#./}
		apk adbdump "$f" > /w/info
		name=$(sed -n 's/^  name: //p' /w/info | head -n 1)
		version=$(sed -n 's/^  version: //p' /w/info | head -n 1)
		got=$(sed -n 's/^  arch: //p' /w/info | head -n 1)
		# ---- check_arch_matches_its_directory: apk offers a package only where its arch is the router's
		[ "$got" = "$arch" ] || fail check_arch_matches_its_directory "$arch/$f declares arch '$got'"
		a=$((a + 1))
		# ---- check_entries_owned_by_root
		acls=$(grep -cE '^[[:space:]]*(- )?acl:' /w/info || true)
		users=$(grep -cE '^[[:space:]]*user: root$' /w/info || true)
		groups=$(grep -cE '^[[:space:]]*group: root$' /w/info || true)
		[ "$acls" -gt 0 ] || fail check_entries_owned_by_root "measured nothing: $arch/$f lists no entry"
		[ "$users" = "$acls" ] && [ "$groups" = "$acls" ] ||
			fail check_entries_owned_by_root "$arch/$f: $acls entries, $users owned by user root, $groups by group root: $(grep -E '^[[:space:]]*(user|group):' /w/info | grep -v ': root$' | sort -u | tr -s ' \n' ' ')"
		o=$((o + acls))
		# the name apk asks a repository for is <name>-<version>.apk
		[ "$f" = "$name-$version.apk" ] ||
			fail check_index_lists_the_packages "$arch/$f is $name $version, so apk asks for $name-$version.apk"
		echo "$f $(wc -c < "$f" | tr -d ' ')" >> /w/present.$arch
	done
	# ---- check_index_lists_the_packages: each entry's file, arch and size, and no file left out
	apk adbdump packages.adb | awk -v want="$arch" '
		function out() { if (n != "") print n "-" v ".apk " s; if (n != "" && ar != want) bad = bad " " n "-" v "(" ar ")" }
		/^  - name: / { out(); n = $3; v = ""; ar = ""; s = "" }
		/^    version: / { v = $2 }
		/^    arch: / { ar = $2 }
		/^    file-size: / { s = $2 }
		END { out(); if (bad != "") { print "ARCH" bad; exit 1 } }' > /w/listed.$arch ||
		fail check_index_lists_the_packages "$arch/packages.adb lists packages of another arch:$(sed -n 's/^ARCH//p' /w/listed.$arch)"
	sort /w/listed.$arch > /w/l
	sort /w/present.$arch > /w/p
	cmp -s /w/l /w/p || fail check_index_lists_the_packages "$arch/packages.adb lists '$(tr '\n' ' ' < /w/l)', the directory holds '$(tr '\n' ' ' < /w/p)'"
	i=$((i + 1))
done
echo "PASS check_signed_by_the_feed_key ($s files, llm-gateway.pem the only trusted key)"
echo "PASS check_arch_matches_its_directory ($a packages)"
echo "PASS check_entries_owned_by_root ($o entries, all root:root)"
echo "PASS check_index_lists_the_packages ($i indexes)"
CONTAINER
} > "$T/gate.sh"

mkdir -p "$T/in"
cp -R "$FEED/$RELEASE" "$T/in/"
cp "$PUB_KEY" "$T/in/trusted.pem"
out=$(feed_tar "$T/in" "$RELEASE" trusted.pem | in_pinned_alpine "$T/gate.sh") || { echo "$out"; exit 1; }
echo "$out"
# A container that ran nothing would exit 0 too, so each check must have said so.
for c in check_signed_by_the_feed_key check_arch_matches_its_directory check_entries_owned_by_root check_index_lists_the_packages; do
	echo "$out" | grep -q "^PASS $c (" || { echo "FAIL $c: measured nothing: the container printed no verdict"; exit 1; }
done
echo "gate-feed: all 5 checks passed ($FEED, against $(shasum -a 256 "$PUB_KEY" | cut -c1-16)...)"
