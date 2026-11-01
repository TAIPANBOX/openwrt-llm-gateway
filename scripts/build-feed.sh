#!/bin/sh
# build-feed.sh -- lay out and sign the apk feed for OpenWrt 25.12 from the packages a CI run built.
#
#   FROM_CI=<ci.yml run id> ./scripts/build-feed.sh
#
# Where the packages come from
#
# Nothing is built here. The packages are the ones ci.yml built after every gate passed, downloaded
# with gh from the run's llm-gateway-<arch> artifacts into build/feed/download, never into the
# repository. The run must have succeeded, be ci.yml pushed to main, and be for HEAD; HEAD must be
# origin/main with a clean tree; and every artifact carries the git tree it was built from
# (source-tree), which must be HEAD's. So a signature never lands on a package built from other
# source than the scripts and the page published with it.
#
# FEED_TRIAL=1 accepts a pull request's run, for a build tried on the virtual lab before the merge.
# It is never published: publish-feed.sh builds again without it. The tree check still holds, and
# a pull request's run builds the merge commit, whose tree is HEAD's only while main has not moved.
#
# Where it is signed
#
# The private key is read from the login keychain (feed-keygen.sh) and reaches the pinned alpine
# container only on its standard input; the container keeps it in memory and is gone when the
# signing ends (in_pinned_alpine in feed-common.sh). The container runs on this Mac's Docker, so
# the key does not leave it; FEED_SIGN_ON=forge runs it on forge instead, and says so. Inside,
# before anything is signed, each package must be llm-gateway, declare its directory's
# architecture and the version its file name says, and give every entry to root (the r1 packages
# did not; see mkapk.sh). Then:
#
#   the packages first and the index second: `apk mkndx` refuses a package it cannot verify, with
#   "UNTRUSTED signature", which reads like a problem with the key rather than with the order;
#   --allow-untrusted on adbsign describes its input, which has no signature yet: without it
#   adbsign fails after truncating the file it rewrites in place, so it only ever sees copies;
#   --reset-signatures, so this key's signature is the only one a package carries;
#   `apk verify` of every file with this feed's public key as the only trusted key.
#
# Layout:
#
#   feed-out/.nojekyll                               Pages serves the files as they are
#   feed-out/index.html                              how a person adds the feed
#   feed-out/llm-gateway.pem                         the public key, for /etc/apk/keys
#   feed-out/25.12/<arch>/packages.adb               the signed index of that architecture
#   feed-out/25.12/<arch>/llm-gateway-<version>.apk  the package, signed
#
# CI names its packages <name>-<version>.apk already, the name apk asks a repository for; each
# architecture has its own directory, so the names do not collide.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
. "$ROOT/scripts/feed-common.sh"
OUT=${OUT:-$ROOT/feed-out}
WORK=$ROOT/build/feed
PUB_KEY=${PUB_KEY:-$ROOT/keys/llm-gateway.pem}
FROM_CI=${FROM_CI:-}
fail() { echo "build-feed.sh: $*" >&2; exit 1; }

case "$FROM_CI" in ''|*[!0-9]*) fail "usage: FROM_CI=<ci.yml run id> ./scripts/build-feed.sh" ;; esac
[ -f "$PUB_KEY" ] || fail "no public key at $PUB_KEY"
feed_key_present || fail "no signing key in the keychain ($FEED_KEY_SERVICE / $FEED_KEY_ACCOUNT); make the pair once with ./scripts/feed-keygen.sh"
require_main_tree build-feed.sh
HEAD_SHA=$(git rev-parse HEAD)
TREE=$(git rev-parse 'HEAD^{tree}')

# ---- 1. the run
# shellcheck disable=SC2046 # six words, split on purpose
set -- $(gh run view "$FROM_CI" -R "$REPO" --json status,conclusion,event,headBranch,headSha,workflowDatabaseId \
	--jq '"\(.status) \(.conclusion) \(.event) \(.headBranch) \(.headSha) \(.workflowDatabaseId)"')
[ $# = 6 ] || fail "could not read run $FROM_CI of $REPO"
status=$1 concl=$2 event=$3 branch=$4 sha=$5 wf=$6
wfpath=$(gh api "repos/$REPO/actions/workflows/$wf" --jq .path)
[ "$wfpath" = .github/workflows/ci.yml ] || fail "run $FROM_CI is $wfpath, not .github/workflows/ci.yml"
[ "$status $concl" = "completed success" ] || fail "run $FROM_CI is $status/$concl, not completed/success"
[ "$sha" = "$HEAD_SHA" ] || fail "run $FROM_CI is for $sha, HEAD is $HEAD_SHA"
if [ "${FEED_TRIAL:-0}" = 1 ]; then
	case "$event" in push|pull_request) ;; *) fail "run $FROM_CI is a $event run" ;; esac
else
	[ "$event $branch" = "push main" ] || fail "run $FROM_CI is $event on $branch, not a push to main"
fi
echo "==> run $FROM_CI: ci.yml, $event on $branch, $sha, success"

# ---- 2. its packages, and the tree they were built from
DL=$WORK/download/run-$FROM_CI
rm -rf "$DL"
mkdir -p "$DL"
gh run download "$FROM_CI" -R "$REPO" -p 'llm-gateway-*' -D "$DL"
got=$(cd "$DL" && ls -1A | LC_ALL=C sort | tr '\n' ' ')
want=$(for a in $ARCHES; do echo "llm-gateway-$a"; done | LC_ALL=C sort | tr '\n' ' ')
[ "$got" = "$want" ] || fail "the run's artifacts are '$got', not '$want'"
PKG=""
for a in $ARCHES; do
	d=$DL/llm-gateway-$a
	apk=$(cd "$d" && ls -1 ./*.apk 2>/dev/null | sed 's|^\./||')
	printf '%s\n' "$apk" | grep -Eqx 'llm-gateway-[0-9]+\.[0-9]+\.[0-9]+-r[0-9]+\.apk' ||
		fail "llm-gateway-$a holds '$apk', not one llm-gateway-<version>.apk"
	files=$(cd "$d" && ls -1A | LC_ALL=C sort | tr '\n' ' ')
	[ "$files" = "$apk source-tree " ] || fail "llm-gateway-$a holds '$files', not the package and source-tree"
	[ -z "$PKG" ] || [ "$PKG" = "$apk" ] || fail "llm-gateway-$a holds $apk, the others $PKG"
	PKG=$apk
	built=$(cat "$d/source-tree")
	[ "$built" = "$TREE" ] || fail "llm-gateway-$a was built from tree $built, HEAD's is $TREE; refusing"
	echo "    $a: $apk sha256 $(shasum -a 256 "$d/$apk" | cut -d' ' -f1), tree $built"
done

rm -rf "$OUT"
for a in $ARCHES; do
	mkdir -p "$OUT/$RELEASE/$a"
	cp "$DL/llm-gateway-$a/$PKG" "$OUT/$RELEASE/$a/"
done

# ---- 3. sign, in the pinned image (on this Mac unless FEED_SIGN_ON=forge)
SIGN=$WORK/sign.sh
{
	echo "RELEASE='$RELEASE'"
	cat <<'CONTAINER'
set -eu
fail() { echo "sign: $*" >&2; exit 1; }
umask 077
mkdir -p /w/key /w/in /w/trust
# The private key: the first lines of standard input, through its END line. read takes one byte
# at a time from a pipe, so what follows is left for tar.
while IFS= read -r l; do
	printf '%s\n' "$l" >> /w/key/private.pem
	case "$l" in "-----END "*) break ;; esac
done
head -n 1 /w/key/private.pem | grep -q '^-----BEGIN ' && tail -n 1 /w/key/private.pem | grep -q '^-----END ' ||
	fail "no key on standard input"
cd /w/in && tar -xf -
mv /w/in/trusted.pem /w/trust/llm-gateway.pem
umask 022
for dir in /w/in/$RELEASE/*; do
	arch=$(basename "$dir")
	cd "$dir"
	for f in *.apk; do
		apk adbdump "$f" > /w/info
		name=$(sed -n 's/^  name: //p' /w/info | head -n 1)
		version=$(sed -n 's/^  version: //p' /w/info | head -n 1)
		got=$(sed -n 's/^  arch: //p' /w/info | head -n 1)
		[ "$name" = llm-gateway ] || fail "$arch/$f is package '$name'"
		[ "$got" = "$arch" ] || fail "$arch/$f declares arch '$got'"
		[ "$f" = "$name-$version.apk" ] || fail "$arch/$f holds version $version"
		acls=$(grep -cE '^[[:space:]]*(- )?acl:' /w/info || true)
		users=$(grep -cE '^[[:space:]]*user: root$' /w/info || true)
		groups=$(grep -cE '^[[:space:]]*group: root$' /w/info || true)
		[ "$acls" -gt 0 ] && [ "$users" = "$acls" ] && [ "$groups" = "$acls" ] ||
			fail "$arch/$f: $acls entries, $users owned by user root, $groups by group root; not signing it"
		apk adbsign --allow-untrusted --reset-signatures --sign-key /w/key/private.pem "$f" >&2
	done
	apk --keys-dir /w/trust mkndx --description "llm-gateway $version" --output packages.adb ./*.apk >&2
	apk adbsign --allow-untrusted --sign-key /w/key/private.pem packages.adb >&2
	apk --keys-dir /w/trust verify ./*.apk packages.adb >&2
	echo "==> $arch: $(ls ./*.apk | wc -l) package and the index signed, verified against llm-gateway.pem alone" >&2
done
rm -f /w/key/private.pem
cd /w/in && tar -cf - "$RELEASE"
CONTAINER
} > "$SIGN"

STAGE=$WORK/stage
rm -rf "$STAGE" "$WORK/signed.tar"
mkdir -p "$STAGE"
cp -R "$OUT/$RELEASE" "$STAGE/"
cp "$PUB_KEY" "$STAGE/trusted.pem"
if [ "$FEED_SIGN_ON" = forge ]; then
	echo "==> signing on $FORGE in $ALPINE: FEED_SIGN_ON=forge, so the signing key goes to forge, on the container's standard input"
else
	echo "==> signing on this Mac in $ALPINE: the signing key stays here"
fi
{ feed_private_key; feed_tar "$STAGE" "$RELEASE" trusted.pem; } | in_pinned_alpine "$SIGN" > "$WORK/signed.tar"
rm -rf "$OUT/$RELEASE" "$STAGE"
tar -xf "$WORK/signed.tar" -C "$OUT"
rm -f "$WORK/signed.tar"

# ---- 4. the key, the page, and .nojekyll: Pages would otherwise run Jekyll over the feed
cp "$PUB_KEY" "$OUT/llm-gateway.pem"
touch "$OUT/.nojekyll"
KEY_SHA=$(shasum -a 256 "$PUB_KEY" | cut -d' ' -f1)
PKGVER=${PKG#llm-gateway-}
PKGVER=${PKGVER%.apk}
SHORT=$(git rev-parse --short HEAD)
items=""
for a in $ARCHES; do
	case "$a" in
		aarch64_cortex-a53) note="has run on a GL.iNet Flint 2" ;;
		x86_64) note="has run on OpenWrt 25.12.5 in a virtual lab" ;;
		*) note="built and signed" ;;
	esac
	items="$items<li><code>$a</code>: $note</li>"
done
cat > "$OUT/index.html" <<HTML
<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>llm-gateway feed</title>
<style>
body{font:16px/1.6 system-ui,sans-serif;max-width:46rem;margin:2.5rem auto;padding:0 16px;color:#1d1d1f;background:#fff}
code,pre{background:#f2f2f2;border-radius:3px}code{padding:.1rem .3rem}
pre{padding:.8rem;overflow-x:auto;white-space:pre}h2{margin-top:2rem}
@media (prefers-color-scheme: dark){body{color:#e8e8e8;background:#161616}code,pre{background:#262626}a{color:#8ab4f8}}
</style>
<h1>llm-gateway</h1>
<p>A signed apk feed for OpenWrt 25.12 with
<a href="https://github.com/$REPO">llm-gateway</a>: the models of the house behind one name on the
router, a key per device, HTTPS under the router's own CA and a journal without content.</p>
<p>Package: llm-gateway $PKGVER, built by <a href="https://github.com/$REPO/actions/runs/$FROM_CI">CI run $FROM_CI</a>
from commit <a href="https://github.com/$REPO/commit/$HEAD_SHA">$SHORT</a>.</p>

<h2>Add the feed</h2>
<p>On the router, as root. The first line trusts the key, the third adds the feed for the router's
own architecture.</p>
<pre>wget -O /etc/apk/keys/llm-gateway.pem $BASE_URL/llm-gateway.pem
sha256sum /etc/apk/keys/llm-gateway.pem
echo "$BASE_URL/$RELEASE/\$(cat /etc/apk/arch)/packages.adb" &gt;&gt; /etc/apk/repositories.d/customfeeds.list

apk update &amp;&amp; apk add llm-gateway</pre>
<p>The key's SHA-256, which <code>sha256sum</code> must print:</p>
<pre>$KEY_SHA</pre>
<p>The index and every package are signed with that key, so nothing above needs
<code>--allow-untrusted</code>. Without the key apk drops the feed without a word and the package
appears not to exist. The service does nothing until it is configured; the
<a href="https://github.com/$REPO#install">README</a> goes on from there.</p>

<h2>Architectures</h2>
<ul>$items</ul>
</html>
HTML

echo "==> feed at $OUT ($(du -sh "$OUT" | cut -f1)): $PKG for $ARCHES"
echo "    key sha256 $KEY_SHA"
