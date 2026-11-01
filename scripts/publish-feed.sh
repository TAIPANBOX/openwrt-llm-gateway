#!/bin/sh
# publish-feed.sh -- build, sign, gate and publish the apk feed from the Mac that holds the key.
#
#   FROM_CI=<run id of ci.yml pushed to main> ./scripts/publish-feed.sh
#
# Signing does not happen in CI, on purpose.
#
# A signing key held as a repository secret is readable by anyone who can push a workflow to the
# default branch: GitHub masks secrets in logs, and masking is easy to defeat on purpose. For this
# key that trade is wrong, because there is no revocation: a router that has trusted the public
# half keeps trusting anything signed with the private half until a person deletes the file from
# /etc/apk/keys. So CI builds every architecture and runs every gate, and the signature is applied
# here, where the key lives (the login keychain) and from where it travels only into the signing
# container's memory.
#
# What this does
#
#   1. refuses unless the working tree is clean and HEAD is origin/main; FEED_TRIAL is cleared
#   2. build-feed.sh: the run's packages, required to be built from HEAD's tree, signed on this Mac
#   3. gate-feed.sh and teeth-feed.sh against the result, before anything is pushed
#   4. replaces the gh-pages branch with one fresh commit holding only the feed, and pushes it
#   5. turns GitHub Pages on for that branch if it is not (a 409 means the push already did), reads
#      the source back until it says gh-pages /, and waits until the published key and every
#      published index are byte-identical to the ones just built
#
# The packages are not built from this working tree: they are the CI run's, tied to it by the
# tree check. What this tree contributes is the scripts and the landing page, which is why it must
# be clean and on main, and why its commit is named in the published commit.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
. "$ROOT/scripts/feed-common.sh"
FEED_TRIAL=0
export FEED_TRIAL
SITE=$ROOT/feed-out

require_main_tree publish-feed.sh
PUBLISH_SHA=$(git rev-parse HEAD)
PUBLISH_SHORT=$(git rev-parse --short HEAD)
case "${FROM_CI:-}" in ''|*[!0-9]*) echo "usage: FROM_CI=<ci.yml run id> ./scripts/publish-feed.sh" >&2; exit 1 ;; esac
echo "==> publishing the packages of CI run $FROM_CI with the scripts of commit $PUBLISH_SHA"

./scripts/build-feed.sh

echo "==> gating what is about to be published"
# Before the push rather than after. A feed is the one artefact where "we will notice if it is
# broken" is wrong: the router that notices is someone else's.
./scripts/gate-feed.sh
./scripts/teeth-feed.sh

echo "==> publishing to gh-pages"
# A worktree rather than a branch switch, so the working tree is not disturbed by publishing.
WT=$(mktemp -d)
cleanup() {
	git worktree remove --force "$WT" >/dev/null 2>&1 || true
	rm -rf "$WT"
	git branch -qD _feed_publish >/dev/null 2>&1 || true
}
trap cleanup EXIT

# A fresh orphan commit every time, force-pushed, so the branch is always one commit deep and
# holds nothing but the feed. Committing on top of the previous feed would keep every version of
# every package in the repository forever; a router installs whatever the feed serves now, and the
# source that produced it has its own history on main.
git worktree add -q --detach "$WT"
git -C "$WT" checkout -q --orphan _feed_publish
git -C "$WT" rm -rq --cached . >/dev/null 2>&1 || true
find "$WT" -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
cp -R "$SITE/." "$WT/"
# Finder writes .DS_Store into any folder it shows, at any moment; the gate passed a minute ago,
# so this looks again at what is about to be committed.
if find "$WT" -name .DS_Store ! -path "$WT/.git/*" -print | grep -q .; then
	echo "publish-feed.sh: a .DS_Store appeared in the copy to publish; refusing" >&2
	exit 1
fi
[ -f "$WT/.nojekyll" ] || { echo "publish-feed.sh: no .nojekyll in the copy to publish; refusing" >&2; exit 1; }

git -C "$WT" add -A
git -C "$WT" commit -q -m "feed: llm-gateway from CI run $FROM_CI, signed on a workstation, scripts at $PUBLISH_SHORT"
secretscan -C "$WT" tree HEAD
# A pre-push hook that scans old..new needs the old gh-pages commit locally.
git fetch -q origin gh-pages 2>/dev/null || true
# Force, because the branch is replaced rather than extended. This is the one place a force push
# is the correct operation and not a way out of a mistake.
git -C "$WT" push -qf origin HEAD:gh-pages
echo "==> pushed gh-pages"

echo "==> GitHub Pages"
pages_on_gh_pages

echo "==> waiting for $BASE_URL to serve this build"
LIVE=$ROOT/build/feed/live
mkdir -p "$LIVE"
paths="llm-gateway.pem"
for a in $ARCHES; do paths="$paths $RELEASE/$a/packages.adb"; done
tries=0
while :; do
	same=0
	for p in $paths; do
		code=$(curl -s -o "$LIVE/file" -w '%{http_code}' "$BASE_URL/$p" || echo 000)
		[ "$code" = 200 ] && cmp -s "$LIVE/file" "$SITE/$p" && same=$((same + 1))
	done
	[ "$same" = "$(echo $paths | wc -w | tr -d ' ')" ] && break
	tries=$((tries + 1))
	[ "$tries" -lt 60 ] || { echo "publish-feed.sh: $BASE_URL does not serve this build after 15 minutes" >&2; exit 1; }
	sleep 15
done
rm -rf "$LIVE"
echo "==> published: $BASE_URL/ (the key and $(echo $ARCHES | wc -w | tr -d ' ') indexes byte-identical to this build)"
