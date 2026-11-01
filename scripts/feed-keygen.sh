#!/bin/sh
# feed-keygen.sh -- make the signing pair of the apk feed. Run once, ever, on the Mac that signs.
#
# apk-tools v3 signs with an elliptic-curve key on prime256v1, the shape OpenWrt uses for its own
# release keys: /etc/apk/keys/openwrt-25.12.pem on any 25.12 device is a PEM public key of exactly
# this kind. The usign (Ed25519) keys of opkg do not apply to apk.
#
#   public   keys/llm-gateway.pem. Committed, published with the feed and copied to /etc/apk/keys
#            on every router that installs from it. Nothing about it is secret.
#   private  the login keychain of this Mac, and nowhere else: a generic password with the service
#            llm-gateway-feed-signing-key and the account TAIPANBOX/openwrt-llm-gateway, holding
#            the PEM in base64. It is never a file: openssl writes it into a pipe, the public half
#            is derived from the pipe, and `security -i` stores it from its standard input, so it
#            is never a program argument either. build-feed.sh reads it back the same way and
#            hands it to the signing container on standard input. It is never printed, never
#            committed (.gitignore, and TestNoPrivateKeyCommittable fails if git would commit any
#            private key), never uploaded and never a CI secret: see publish-feed.sh for why.
#
# There is no revocation. If the private key leaks, a new public key does NOT undo it: every
# router keeps the old one in /etc/apk/keys and trusts whatever it signs until a person deletes
# the file. So it is made once, kept in one place, and if it is ever in doubt that is said
# publicly rather than rotated quietly.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
. "$ROOT/scripts/feed-common.sh"
PUB=${PUB:-$ROOT/keys/llm-gateway.pem}
OPENSSL=${OPENSSL:-openssl}

[ "$(uname -s)" = Darwin ] || { echo "feed-keygen.sh: the key lives in a macOS keychain; run this on the Mac that signs" >&2; exit 1; }
if [ -e "$PUB" ] || feed_key_present; then
	echo "feed-keygen.sh: $PUB or the keychain item $FEED_KEY_SERVICE / $FEED_KEY_ACCOUNT already exists." >&2
	echo "feed-keygen.sh: a second key does not replace the first on any router that already" >&2
	echo "feed-keygen.sh: trusts it. Delete it deliberately if you mean to." >&2
	exit 1
fi
mkdir -p "$(dirname "$PUB")"
umask 077

# The private key exists only in this variable, in base64, until the keychain has it. printf is a
# shell builtin, so the variable reaches the pipes below without becoming anyone's argument.
priv=$("$OPENSSL" ecparam -name prime256v1 -genkey -noout | base64 | tr -d '\n')
case "$priv" in LS0tLS1CRUdJTi*) ;; *) echo "feed-keygen.sh: openssl did not produce a PEM key" >&2; exit 1 ;; esac
printf '%s' "$priv" | base64 -D | "$OPENSSL" ec -pubout -out "$PUB.tmp" 2>/dev/null
chmod 0644 "$PUB.tmp"

printf 'add-generic-password -s "%s" -a "%s" -l "%s" -j "%s" -w "%s" "%s"\n' \
	"$FEED_KEY_SERVICE" "$FEED_KEY_ACCOUNT" "llm-gateway apk feed signing key" \
	"base64 of a PEM EC private key on prime256v1; made by scripts/feed-keygen.sh, read by scripts/build-feed.sh" \
	"$priv" "$FEED_KEYCHAIN" | security -i >/dev/null
unset priv

# security -i exits 0 even when the command it read failed, so the key is read back and its public
# half compared before the public half is kept.
if ! feed_private_key | "$OPENSSL" ec -pubout 2>/dev/null | cmp -s - "$PUB.tmp"; then
	rm -f "$PUB.tmp"
	echo "feed-keygen.sh: the keychain does not hold the key just made; nothing was kept." >&2
	echo "feed-keygen.sh: if an item was stored, remove it with" >&2
	echo "  security delete-generic-password -s $FEED_KEY_SERVICE -a $FEED_KEY_ACCOUNT" >&2
	exit 1
fi
mv "$PUB.tmp" "$PUB"

cat <<EOF

Public key   $PUB   commit this, it is published with the feed
             SHA-256 $(shasum -a 256 "$PUB" | cut -d' ' -f1)
Private key  keychain $FEED_KEYCHAIN
             service $FEED_KEY_SERVICE, account $FEED_KEY_ACCOUNT

The private key is nowhere else. Keep a second copy where you would keep a password: there is no
way to recover it, and no way to revoke it from a router that already trusts the public half.
EOF
