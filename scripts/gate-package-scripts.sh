#!/bin/sh
# gate-package-scripts.sh <adbdump.txt> -- the package carries the scripts apk runs, read back from
# what `apk adbdump` printed for the built package, not from the source that should have put them
# there. mkapk.sh runs it on every package; the teeth run it on fixtures.
#
# apk runs post-upgrade, and not post-install, when it replaces a version. A package without it
# leaves the old binary running from memory and makes no account: nothing fails, the new version
# simply does not start until the next reboot.
set -eu
DUMP=${1:?usage: gate-package-scripts.sh <adbdump.txt>}
[ -s "$DUMP" ] || { echo "FAIL: measured nothing: $DUMP is empty or not there"; exit 1; }
grep -qE '^[[:space:]]*(- )?scripts:' "$DUMP" || { echo "FAIL: measured nothing: no scripts section in $DUMP"; exit 1; }

# script <name>: the text of one script, by the indentation adbdump gives its block
script() {
	awk -v want="$1:" '
		{ line = $0; sub(/^[[:space:]]+/, "", line); ind = length($0) - length(line) }
		on && $0 != "" && ind <= base { on = 0 }
		on { print line; next }
		index(line, want) == 1 { on = 1; base = ind }
	' "$DUMP"
}
fail=0
for s in post-install post-upgrade pre-deinstall; do
	if [ -z "$(script "$s")" ]; then echo "FAIL: the package has no $s script"; fail=1; fi
done
up=$(script post-upgrade)
for need in '/etc/init.d/llm-gateway account' '/etc/init.d/llm-gateway restart' 'rm -rf /tmp/llm-gateway'; do
	case "$up" in *"$need"*) ;; *) echo "FAIL: post-upgrade does not run: $need"; fail=1 ;; esac
done
if echo "$up" | grep -qE 'llm-gateway enable$'; then echo "FAIL: post-upgrade enables the service, which the owner may have switched off"; fail=1; fi
[ "$fail" = 0 ] || exit 1
echo "OK: post-install, post-upgrade and pre-deinstall are in the package; post-upgrade makes the account and restarts"
