# feed-common.sh -- what the feed scripts share. Sourced, never run; ROOT is set by the caller.
#
# The feed is signed and checked by apk-tools v3, which exists here only in the alpine image
# mkapk.sh pins. That image runs on this Mac's own Docker, so the signing key never leaves the
# machine that holds it. FEED_SIGN_ON=forge runs the same image on forge instead, where it is
# loaded too: a fallback for a Mac whose Docker is off, and one that sends the key to another
# machine, so it is never the default and the log says so. --pull never makes sure nothing is
# fetched for the image on either.

FEED_SIGN_ON=${FEED_SIGN_ON:-local}
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

# The container's options on either machine: no network, its work space /w in memory.
FEED_DOCKER_OPTS="--rm -i --pull never --network none --tmpfs /w:rw,size=160m,mode=0700"

# in_pinned_alpine <script>: runs a script of this machine in the pinned alpine image, standard
# input and output passed through. Whatever it reads, the signing key included, comes on standard
# input and is never written to disk or put on a command line; the container is gone when the
# script ends. Where it runs is FEED_SIGN_ON: local (the default) or forge.
in_pinned_alpine() {
	case "$FEED_SIGN_ON" in
		local)
			docker ps >/dev/null 2>&1 || {
				echo "feed: Docker on this Mac does not answer (docker ps). Start Docker Desktop, or set" >&2
				echo "feed: FEED_SIGN_ON=forge to run the container on forge, which sends the key there." >&2
				return 1
			}
			docker image inspect "$ALPINE" >/dev/null 2>&1 || {
				echo "feed: this Mac's Docker has no $ALPINE; fetch it once with: docker pull $ALPINE" >&2
				return 1
			}
			# shellcheck disable=SC2086 # the options, split on purpose
			docker run $FEED_DOCKER_OPTS "$ALPINE" sh -c "$(cat "$1")"
			;;
		forge)
			echo "feed: FEED_SIGN_ON=forge: the container runs on $FORGE; what it reads on standard input goes there" >&2
			# The script travels as an argument, in base64 so no quoting of ssh or sh can change it.
			b64=$(base64 < "$1" | tr -d '\n')
			ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 "$FORGE" \
				"sudo docker run $FEED_DOCKER_OPTS $ALPINE sh -c 'eval \"\$(echo $b64 | base64 -d)\"'"
			;;
		*)
			echo "feed: FEED_SIGN_ON is '$FEED_SIGN_ON'; it is local (the default) or forge" >&2
			return 1
			;;
	esac
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
