#!/bin/sh
# gate-no-wildcard.sh -- the product code never names a wildcard listen address or a wildcard
# CORS origin (invariants 3 and 4). Tests are exempt: they plant both on purpose.
#
# Why a text gate on top of the tests: a new listener or a new response path written next year
# is not covered by today's tests, and these two strings are how every model server surveyed on
# 2026-11-01 ended up reachable from the whole network and every web page.
set -eu
REPO=${NOWILD_REPO:-$(cd "$(dirname "$0")/.." && pwd)}
cd "$REPO"
SRC=$(ls ./*.go 2>/dev/null | grep -v '_test\.go$' || true)
[ -n "$SRC" ] || { echo "FAIL: measured nothing: no Go source"; exit 1; }
fail=0
check() { # pattern, what it means
	hits=$(grep -nE "$1" $SRC || true)
	if [ -n "$hits" ]; then echo "FAIL: $2:"; echo "$hits"; fail=1; fi
}
check '(^|[^0-9.])0\.0\.0\.0'                             'a wildcard IPv4 address'
check '"\[?::\]?(:[0-9]*)?"'                             'a wildcard IPv6 address'
check '(Listen|ListenAndServe(TLS)?)\([^)]*":[0-9]'      'a listen on every address (":port")'
check 'Addr:[[:space:]]*":[0-9]'                         'a server Addr on every address'
check 'Allow-Origin"?[^)]*"\*"'                          'a wildcard CORS origin'
check 'AllowAllOrigins|anyHost\(\)|AllowedOrigins.*\*'   'a CORS library told to allow every origin'
[ "$fail" = 0 ] || exit 1
echo "OK: no wildcard listen address or CORS origin in $(echo "$SRC" | wc -w | tr -d ' ') source files"
