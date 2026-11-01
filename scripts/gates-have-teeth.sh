#!/bin/sh
# gates-have-teeth.sh -- each gate goes red on its own planted fault, stays green on the clean
# tree, and says "measured nothing" when its subject is gone. A gate that cannot fail is ceremony.
set -eu
REPO=$(cd "$(dirname "$0")/.." && pwd)
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
fresh() { rm -rf "$TMP/r"; mkdir -p "$TMP/r"; (cd "$REPO" && tar cf - --exclude .git --exclude docs . ) | (cd "$TMP/r" && tar xf -); }
pass=0; bad=0
expect() { # want(red|green|nothing) name command...
	want=$1 name=$2; shift 2
	out=$("$@" 2>&1) && rc=0 || rc=$?
	case $want in
	green)   [ $rc = 0 ] && ok=1 || ok=0 ;;
	red)     [ $rc != 0 ] && ! echo "$out" | grep -q 'measured nothing' && ok=1 || ok=0 ;;
	nothing) [ $rc != 0 ] && echo "$out" | grep -q 'measured nothing' && ok=1 || ok=0 ;;
	esac
	if [ $ok = 1 ]; then pass=$((pass+1)); echo "ok   $name"; else bad=$((bad+1)); echo "BAD  $name (rc=$rc)"; echo "$out" | tail -5; fi
}
B="sh scripts/gate-scenarios-bound.sh"
W="sh scripts/gate-no-wildcard.sh"

fresh; expect green "bound: clean tree" sh -c "cd $TMP/r && $B"
fresh; perl -i -ne 'print unless /^\s*# bound to: TestWrongKeyIsRefused$/' "$TMP/r/features/access.feature"
grep -q 'TestWrongKeyIsRefused' "$TMP/r/features/access.feature" && { echo "BAD  the unbinding plant did not apply"; exit 1; }
expect red "bound: a scenario loses its binding" sh -c "cd $TMP/r && BOUND_SKIP_RUN=1 $B"
fresh; sed -i.bak 's/# bound to: TestWrongKeyIsRefused/# bound to: TestThatDoesNotExist/' "$TMP/r/features/access.feature"
expect red "bound: a binding names no test" sh -c "cd $TMP/r && BOUND_SKIP_RUN=1 $B"
fresh; sed -i.bak 's/# bound to vlab: models_zone_has_no_internet/# bound to vlab: no_such_check/' "$TMP/r/features/reach.feature"
expect red "bound: a vlab binding names no check" sh -c "cd $TMP/r && BOUND_SKIP_RUN=1 $B"
fresh; perl -i -pe 's/^(func TestWrongKeyIsRefused\(t \*testing.T\) \{)$/$1\n\tt.Fatal("planted")/' "$TMP/r/access_test.go"
grep -q 't.Fatal("planted")' "$TMP/r/access_test.go" || { echo "BAD  the failing-test plant did not apply"; exit 1; }
expect red "bound: a bound test fails" sh -c "cd $TMP/r && $B"
fresh; rm -f "$TMP/r"/features/*.feature
expect nothing "bound: no feature files" sh -c "cd $TMP/r && $B"

fresh; expect green "wildcard: clean tree" sh -c "cd $TMP/r && $W"
for plant in 'var _ = "0.0.0.0:8760"' 'var _ = "[::]:8760"' 'var _ = func() { http.ListenAndServe(":8760", nil) }' \
	'var _ = &http.Server{Addr: ":8760"}' 'var _ = func(w http.ResponseWriter) { w.Header().Set("Access-Control-Allow-Origin", "*") }'; do
	fresh; printf 'package main\n\nimport "net/http"\n\nvar _ = http.StatusOK\n%s\n' "$plant" > "$TMP/r/planted.go"
	expect red "wildcard: plant $plant" sh -c "cd $TMP/r && $W"
done
fresh; printf 'package main\n\nvar _ = "0.0.0.0"\n' > "$TMP/r/planted_test.go"
expect green "wildcard: a test file may name it" sh -c "cd $TMP/r && $W"
fresh; rm -f "$TMP/r"/*.go
expect nothing "wildcard: no source" sh -c "cd $TMP/r && $W"

echo "teeth: $pass ok, $bad bad"
[ $bad = 0 ]
