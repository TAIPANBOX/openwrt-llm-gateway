# feed-common.sh -- what the feed scripts share. Sourced, never run; ROOT is set by the caller.
#
# The feed is signed and checked by apk-tools v3, which exists here only in the alpine image
# mkapk.sh pins. The Mac does not run Docker, so that image runs on forge, where it is already
# loaded; --pull never makes sure nothing is fetched for it.

FORGE=${FORGE:-yurii@192.168.1.10}
REPO=${REPO:-TAIPANBOX/openwrt-llm-gateway}
RELEASE=${RELEASE:-25.12}
BASE_URL=${BASE_URL:-https://taipanbox.github.io/openwrt-llm-gateway}
ALPINE=${ALPINE:-$(sed -n 's/^ALPINE=\${ALPINE:-\(.*\)}$/\1/p' "$ROOT/mkapk.sh")}
# The architectures CI builds, read from its matrix so the feed cannot drift from it.
ARCHES=${ARCHES:-$(sed -n 's/.* apk: \([a-z0-9_-]*\) }.*/\1/p' "$ROOT/.github/workflows/ci.yml" | LC_ALL=C sort | tr '\n' ' ')}

# The private key: one generic password in the login keychain, the PEM in base64 (feed-keygen.sh).
FEED_KEY_SERVICE=llm-gateway-feed-signing-key
FEED_KEY_ACCOUNT=TAIPANBOX/openwrt-llm-gateway
FEED_KEYCHAIN=${FEED_KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}

# feed_key_present: the keychain item exists. Reads its attributes only, never the secret.
feed_key_present() {
	security find-generic-password -s "$FEED_KEY_SERVICE" -a "$FEED_KEY_ACCOUNT" "$FEED_KEYCHAIN" >/dev/null 2>&1
}

# feed_private_key: the PEM on standard output, for a pipe and nothing else.
feed_private_key() {
	security find-generic-password -s "$FEED_KEY_SERVICE" -a "$FEED_KEY_ACCOUNT" -w "$FEED_KEYCHAIN" | base64 -D
}

# in_forge_alpine <script>: runs a script of this machine in the pinned alpine image on forge, with
# no network and its work space /w in the container's memory, standard input and output passed
# through. The script travels as an argument, in base64 so no quoting of ssh or sh can change it.
# Whatever it reads, the signing key included, comes on standard input and is never written to
# forge's disk; the container is gone when the script ends.
in_forge_alpine() {
	b64=$(base64 < "$1" | tr -d '\n')
	ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 "$FORGE" \
		"sudo docker run --rm -i --pull never --network none --tmpfs /w:rw,size=160m,mode=0700 $ALPINE sh -c 'eval \"\$(echo $b64 | base64 -d)\"'"
}

# feed_tar <dir> <path...>: a tar of paths under dir on standard output, with no macOS metadata.
feed_tar() {
	d=$1
	shift
	COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -C "$d" -cf - "$@"
}

# require_main_tree: the working tree is clean and HEAD is origin/main, so what is signed and
# published comes from main's own history. FEED_TRIAL=1 lifts the second half for a trial build of
# a pull request, which is never published: publish-feed.sh builds again without it.
require_main_tree() {
	dirty=$(git -C "$ROOT" status --porcelain)
	[ -z "$dirty" ] || {
		echo "$1: the working tree is not clean; commit first:" >&2
		echo "$dirty" >&2
		exit 1
	}
	git -C "$ROOT" fetch -q origin main
	head=$(git -C "$ROOT" rev-parse HEAD)
	main=$(git -C "$ROOT" rev-parse origin/main)
	if [ "$head" != "$main" ]; then
		if [ "${FEED_TRIAL:-0}" = 1 ]; then
			echo "==> trial: HEAD $head is not origin/main $main; this build is never published"
		else
			echo "$1: HEAD ($head) is not origin/main ($main); refusing" >&2
			exit 1
		fi
	fi
}
