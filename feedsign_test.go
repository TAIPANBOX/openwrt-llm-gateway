package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// signOnRun is one call of in_pinned_alpine from scripts/feed-common.sh, with docker and ssh
// replaced by stubs that log how they were called and pass standard input through, as the
// container passes the signing key through.
type signOnRun struct {
	stdout, stderr, log string
	err                 error
}

const signOnSecret = "feed-sign-test-secret-7d1f"

func runSignOn(t *testing.T, env ...string) signOnRun {
	t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("docker", `printf 'docker %s\n' "$*" >> "$LOG"
case "$1" in
ps) [ "${DOCKER_DOWN:-0}" != 1 ] ;;
image) [ "${IMAGE_MISSING:-0}" != 1 ] ;;
run) cat ;;
*) exit 9 ;;
esac
`)
	stub("ssh", `printf 'ssh %s\n' "$*" >> "$LOG"
cat
`)
	script := filepath.Join(dir, "container.sh")
	if err := os.WriteFile(script, []byte("cat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "calls.log")
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `. "$ROOT/scripts/feed-common.sh" && in_pinned_alpine "$SCRIPT"`)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FEED_SIGN_ON=") && !strings.HasPrefix(kv, "PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+dir+":"+os.Getenv("PATH"), "ROOT="+root, "SCRIPT="+script, "LOG="+logFile)
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(signOnSecret + "\n")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	r := signOnRun{err: cmd.Run(), stdout: out.String(), stderr: errOut.String()}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	r.log = "\n" + string(data)
	return r
}

// TestFeedSigningStaysOnTheMacUnlessForgeIsAsked requires the signing container of the feed to run
// on this Mac's own Docker by default, with no network, its work space in memory and the key on its
// standard input only; to stop with a plain message when that Docker does not answer or lacks the
// pinned image, rather than go elsewhere; and to run on forge only when FEED_SIGN_ON=forge says so,
// saying so in the log.
func TestFeedSigningStaysOnTheMacUnlessForgeIsAsked(t *testing.T) {
	local := "docker run --rm -i --pull never --network none --tmpfs /w:rw,size=160m,mode=0700 alpine@sha256:"
	for _, tc := range []struct {
		name   string
		env    []string
		ok     bool
		log    []string // each must be in the log of calls
		notLog []string // none may be; a leading newline means at the start of a call
		stderr string
	}{
		{name: "default", ok: true, log: []string{"docker ps", local}, notLog: []string{"\nssh "}},
		{name: "local", env: []string{"FEED_SIGN_ON=local"}, ok: true, log: []string{"docker ps", local}, notLog: []string{"\nssh "}},
		{name: "docker down", env: []string{"DOCKER_DOWN=1"}, notLog: []string{"\ndocker run", "\nssh "}, stderr: "FEED_SIGN_ON=forge"},
		{name: "image missing", env: []string{"IMAGE_MISSING=1"}, notLog: []string{"\ndocker run", "\nssh "}, stderr: "docker pull"},
		{name: "forge", env: []string{"FEED_SIGN_ON=forge"}, ok: true,
			log:    []string{"ssh ", "--pull never --network none --tmpfs /w:rw,size=160m,mode=0700 alpine@sha256:"},
			notLog: []string{"\ndocker "}, stderr: "forge"},
		{name: "unknown", env: []string{"FEED_SIGN_ON=cloud"}, notLog: []string{"\ndocker ", "\nssh "}, stderr: "FEED_SIGN_ON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runSignOn(t, tc.env...)
			if (r.err == nil) != tc.ok {
				t.Fatalf("succeeded=%v, want %v\nstderr: %s\ncalls: %s", r.err == nil, tc.ok, r.stderr, r.log)
			}
			if tc.ok && r.stdout != signOnSecret+"\n" {
				t.Errorf("the container's standard input did not come through: stdout %q", r.stdout)
			}
			if strings.Contains(r.log, signOnSecret) {
				t.Errorf("the key reached a command line: %s", r.log)
			}
			for _, s := range tc.log {
				if !strings.Contains(r.log, s) {
					t.Errorf("no call with %q; calls:\n%s", s, r.log)
				}
			}
			for _, s := range tc.notLog {
				if strings.Contains(r.log, s) {
					t.Errorf("a call with %q; calls:\n%s", s, r.log)
				}
			}
			if !strings.Contains(r.stderr, tc.stderr) {
				t.Errorf("stderr does not say %q: %s", tc.stderr, r.stderr)
			}
		})
	}
}
