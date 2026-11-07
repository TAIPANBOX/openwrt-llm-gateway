#!/bin/sh
# gate-seccomp-run.sh -- the daemon under its own filter on this machine's kernel, and the proof that
# the filter is what it runs under. Run in CI on amd64 and on arm64, the two architectures the filter
# goes on for, and by the teeth.
#
# A list is only as good as the run behind it: a call it lacks ends the daemon with SIGSYS on the rare
# path that makes it. So this runs the real serve, in a static binary, through TLS, a backend by name,
# an https backend, a stream, a client leaving, the journal's rotation and SIGTERM, on this kernel;
# then, for calls each of those depends on, a copy of the tree whose list lacks that call alone must
# die with SIGSYS. That shows the path ran under the filter, and that a green run means something.
set -eu
REPO=${SECCOMP_REPO:-$(cd "$(dirname "$0")/.." && pwd)}
case "$(uname -s) $(uname -m)" in
Linux\ x86_64 | Linux\ aarch64) ;;
*) echo "FAIL: measured nothing: the filter goes on only on linux amd64 and arm64, this is $(uname -sm)"; exit 1 ;;
esac
T=TestDaemonRunsUnderItsFilter
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

run() { # the tree; prints the output, and fails with the test
	(cd "$1" && go test -count=1 -v -timeout 180s -run "^$T\$" . 2>&1)
}
if ! run "$REPO" > "$TMP/out"; then
	tail -30 "$TMP/out"; echo "FAIL: the daemon does not run under its filter"; exit 1
fi
grep -q -- "^--- PASS: $T " "$TMP/out" || { echo "FAIL: measured nothing: $T did not run"; exit 1; }
echo "ok   the daemon ran under its filter: $(grep -c '' "$REPO/internal/sandbox/seccomp/common.txt") lines in the list, $(uname -m)"
(cd "$REPO" && go test -count=1 ./internal/sandbox/ >/dev/null) || { echo "FAIL: the sandbox tests"; exit 1; }

# Each call stands for a path: the poller, a connection, the journal's rotation, an https backend's
# roots. renameat is made once, at the rotation, so its death shows the rotation ran filtered.
for call in ${SECCOMP_DROP:-eventfd2 accept4 renameat readlinkat}; do
	rm -rf "$TMP/r"; mkdir -p "$TMP/r"
	(cd "$REPO" && tar cf - --exclude .git --exclude docs --exclude ./build . ) | (cd "$TMP/r" && tar xf -)
	grep -qx "$call" "$TMP/r/internal/sandbox/seccomp/common.txt" || { echo "FAIL: measured nothing: $call is not in the list"; exit 1; }
	sed -i.bak "/^$call\$/d" "$TMP/r/internal/sandbox/seccomp/common.txt"
	if run "$TMP/r" > "$TMP/out"; then
		echo "FAIL: the daemon ran without $call: the run does not reach what needs it, or the filter is not on"; exit 1
	fi
	grep -qE 'bad system call|killed by bad system call|signal: bad system' "$TMP/out" || { tail -20 "$TMP/out"; echo "FAIL: without $call the test failed, but not by SIGSYS"; exit 1; }
	echo "ok   without $call the daemon dies by SIGSYS"
done
echo "OK: the daemon runs under its own filter on $(uname -m), and dies without what it needs"
