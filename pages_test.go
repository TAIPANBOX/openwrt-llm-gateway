package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pagesRun is one call of pages_on_gh_pages from scripts/feed-common.sh under set -eu, as
// publish-feed.sh runs it, with gh replaced by a stub that answers the way gh does: the body on
// standard output, "gh: <message> (HTTP <code>)" on standard error and exit status 1 for an HTTP
// error. PAGES_GET lists the answers to successive reads of the Pages site, separated by ";": a
// status code, or the source as the --jq filter prints it ("gh-pages /"). The last one repeats.
type pagesRun struct {
	stdout, stderr string
	posts, puts    int
	err            error
}

func runPages(t *testing.T, env ...string) pagesRun {
	t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stub := `printf 'gh %s\n' "$*" >> "$LOG"
case " $* " in
*" -X POST "*) code=$PAGES_POST ;;
*" -X PUT "*) code=$PAGES_PUT ;;
*)
	n=$(( $(cat "$GETS") + 1 )); echo "$n" > "$GETS"
	code=$(printf '%s\n' "$PAGES_GET" | tr ';' '\n' | sed -n "${n}p")
	[ -n "$code" ] || code=$(printf '%s\n' "$PAGES_GET" | tr ';' '\n' | tail -1)
	;;
esac
case "$code" in
2[0-9][0-9]) exit 0 ;;
[0-9][0-9][0-9])
	case "$code" in
	404) msg="Not Found" ;;
	409) msg="GitHub Pages is already enabled." ;;
	*) msg="Something went wrong" ;;
	esac
	printf '{"message":"%s","status":"%s"}' "$msg" "$code"
	printf 'gh: %s (HTTP %s)\n' "$msg" "$code" >&2
	exit 1 ;;
*) printf '%s\n' "$code" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+stub), 0o755); err != nil {
		t.Fatal(err)
	}
	logFile, gets := filepath.Join(dir, "calls.log"), filepath.Join(dir, "gets")
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gets, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `set -eu; . "$ROOT/scripts/feed-common.sh"; pages_on_gh_pages`)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "REPO=") && !strings.HasPrefix(kv, "PAGES_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+dir+":"+os.Getenv("PATH"), "ROOT="+root, "LOG="+logFile, "GETS="+gets,
		"PAGES_SLEEP=0", "PAGES_TRIES=4", "PAGES_PUT=204")
	cmd.Env = append(cmd.Env, env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	r := pagesRun{err: cmd.Run(), stdout: out.String(), stderr: errOut.String()}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, " -X POST ") {
			r.posts++
		}
		if strings.Contains(l, " -X PUT ") {
			r.puts++
		}
	}
	return r
}

// TestPagesStepTakesAlreadyEnabledAsOn requires the Pages step of publish-feed.sh to leave GitHub
// Pages serving gh-pages from its root and to read that back before the script goes on to wait for
// the feed. The first publication, on 2026-11-01, stopped after its push: the push to gh-pages had
// turned Pages on by itself, so the read said 404 while the site was being created and turning it on
// said 409, already enabled. A 409 is that state, not a failure; any other error stops the step.
func TestPagesStepTakesAlreadyEnabledAsOn(t *testing.T) {
	for _, tc := range []struct {
		name        string
		env         []string
		ok          bool
		posts, puts int
		stdout      string
		stderr      string
	}{
		{name: "already on", env: []string{"PAGES_GET=gh-pages /"}, ok: true, stdout: "serves gh-pages /"},
		{name: "the push turned it on", env: []string{"PAGES_GET=404;gh-pages /", "PAGES_POST=409"},
			ok: true, posts: 1, stdout: "already on"},
		{name: "still being created", env: []string{"PAGES_GET=404;404;gh-pages /", "PAGES_POST=409"},
			ok: true, posts: 2, stdout: "serves gh-pages /"},
		{name: "turned on", env: []string{"PAGES_GET=404;gh-pages /", "PAGES_POST=201"},
			ok: true, posts: 1, stdout: "turned on"},
		{name: "moved", env: []string{"PAGES_GET=main /docs;gh-pages /"}, ok: true, puts: 1, stdout: "moved from 'main /docs'"},
		{name: "turning on refused", env: []string{"PAGES_GET=404", "PAGES_POST=422"}, posts: 1, stderr: "HTTP 422"},
		{name: "never serves gh-pages", env: []string{"PAGES_GET=404", "PAGES_POST=409"}, posts: 4, stderr: "does not serve gh-pages /"},
		{name: "the read fails", env: []string{"PAGES_GET=500", "PAGES_POST=201"}, stderr: "HTTP 500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runPages(t, tc.env...)
			if (r.err == nil) != tc.ok {
				t.Fatalf("succeeded=%v, want %v\nstdout: %s\nstderr: %s", r.err == nil, tc.ok, r.stdout, r.stderr)
			}
			if r.posts != tc.posts || r.puts != tc.puts {
				t.Errorf("%d POST and %d PUT, want %d and %d", r.posts, r.puts, tc.posts, tc.puts)
			}
			if !strings.Contains(r.stdout, tc.stdout) {
				t.Errorf("stdout does not say %q: %s", tc.stdout, r.stdout)
			}
			if !strings.Contains(r.stderr, tc.stderr) {
				t.Errorf("stderr does not say %q: %s", tc.stderr, r.stderr)
			}
		})
	}
}
