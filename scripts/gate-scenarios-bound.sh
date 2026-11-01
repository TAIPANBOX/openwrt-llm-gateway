#!/bin/sh
# gate-scenarios-bound.sh -- every scenario names its proof, and every named proof exists and passes.
#
# features/*.feature is what a reader reads instead of the code, which only works while it still
# describes what is tested. Two ways for that to rot quietly: a scenario stays after its test is
# gone, so the file promises a guarantee nobody enforces; or a binding names a test that no longer
# exists or no longer passes. This asserts both, and runs the tests.
#
# A scenario is preceded by one or more binding lines:
#   # bound to: TestName[, TestName...]            Go tests in *_test.go
#   # bound to vlab: check_name[, check_name...]   checks scripts/vlab-e2e.sh --selftest lists
# BOUND_SKIP_RUN=1 skips running the Go tests (the teeth script uses it for speed where running
# is not what it plants).
set -eu
REPO=${BOUND_REPO:-$(cd "$(dirname "$0")/.." && pwd)}
cd "$REPO"
FEATURES=$(ls features/*.feature 2>/dev/null || true)
[ -n "$FEATURES" ] || { echo "FAIL: measured nothing: no features/*.feature"; exit 1; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
# One line per scenario: file|line|go bindings|vlab bindings. Not tabs: read collapses runs of
# whitespace separators, so an empty go field would shift the vlab names into it.
awk '
	FNR == 1 { go = ""; vl = "" }
	/^[[:space:]]*# bound to vlab:/ { sub(/^[[:space:]]*# bound to vlab:[[:space:]]*/, ""); vl = vl (vl == "" ? "" : ",") $0; next }
	/^[[:space:]]*# bound to:/      { sub(/^[[:space:]]*# bound to:[[:space:]]*/, "");      go = go (go == "" ? "" : ",") $0; next }
	/^[[:space:]]*Scenario( Outline)?:/ { gsub(/[[:space:]]/, "", go); gsub(/[[:space:]]/, "", vl)
		print FILENAME "|" FNR "|" go "|" vl; go = ""; vl = ""; next }
	/^[[:space:]]*(Feature|Background):/ { go = ""; vl = "" }
' $FEATURES > "$TMP/scenarios"
[ -s "$TMP/scenarios" ] || { echo "FAIL: measured nothing: no scenarios"; exit 1; }

fail=0
while IFS='|' read -r file line go vl; do
	if [ -z "$go" ] && [ -z "$vl" ]; then
		echo "FAIL: $file:$line: scenario with no binding"; fail=1
	fi
	for t in $(echo "$go" | tr ',' ' '); do
		if ! grep -q "^func $t(t \*testing.T)" ./*_test.go 2>/dev/null; then
			echo "FAIL: $file:$line: bound to $t, which is no test"; fail=1
		fi
		echo "$t" >> "$TMP/tests"
	done
	for c in $(echo "$vl" | tr ',' ' '); do
		if ! sh scripts/vlab-e2e.sh --selftest | grep -qx "$c"; then
			echo "FAIL: $file:$line: bound to vlab check $c, which scripts/vlab-e2e.sh does not run"; fail=1
		fi
	done
done < "$TMP/scenarios"
[ "$fail" = 0 ] || exit 1

n=$(wc -l < "$TMP/scenarios" | tr -d ' ')
if [ "${BOUND_SKIP_RUN:-0}" != 1 ] && [ -s "$TMP/tests" ]; then
	sort -u "$TMP/tests" > "$TMP/uniq"
	re=$(paste -sd'|' "$TMP/uniq")
	go test -count=1 -v -run "^($re)\$" . > "$TMP/out" 2>&1 || true
	while read -r t; do
		if ! grep -q "^--- PASS: $t " "$TMP/out"; then
			echo "FAIL: bound test $t did not pass:"; grep -A5 -- "--- FAIL: $t " "$TMP/out" || tail -20 "$TMP/out"; fail=1
		fi
	done < "$TMP/uniq"
	[ "$fail" = 0 ] || exit 1
	echo "OK: $n scenarios bound; $(wc -l < "$TMP/uniq" | tr -d ' ') bound tests ran and passed"
else
	echo "OK: $n scenarios bound (tests not run)"
fi
