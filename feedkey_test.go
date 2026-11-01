package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// feedKeyRel is the public half of the apk feed's signing pair (scripts/feed-keygen.sh). Every
// router that installs from the feed copies it into /etc/apk/keys.
const feedKeyRel = "keys/llm-gateway.pem"

// privateKeyPEM matches the first line of any PEM private key: EC, RSA, PKCS#8, OpenSSH,
// encrypted. It is assembled from parts so that this file, and any secret scanner reading it,
// never holds the marker itself.
var privateKeyPEM = regexp.MustCompile("-----BEGIN ([A-Z0-9]+ )*" + "PRIVATE" + " KEY-----")

// TestFeedPublicKey requires keys/llm-gateway.pem to be exactly one PEM block of type PUBLIC KEY
// holding an EC key on P-256 (prime256v1), the only kind apk-tools v3 verifies. A private key in
// its place, another curve or algorithm, or anything after the block is red; a missing file has
// measured nothing.
func TestFeedPublicKey(t *testing.T) {
	data, err := os.ReadFile(filepath.FromSlash(feedKeyRel))
	if err != nil {
		t.Fatalf("measured nothing: %v", err)
	}
	problems := feedKeyProblems(data)
	for _, p := range problems {
		t.Errorf("%s: %s", feedKeyRel, p)
	}
	if len(problems) == 0 {
		t.Logf("%s: EC public key on P-256", feedKeyRel)
	}
}

func feedKeyProblems(data []byte) []string {
	block, rest := pem.Decode(data)
	if block == nil {
		return []string{"no PEM block"}
	}
	var problems []string
	if len(bytes.TrimSpace(rest)) != 0 {
		problems = append(problems, "more than one PEM block, or text after it")
	}
	if block.Type != "PUBLIC KEY" {
		return append(problems, fmt.Sprintf("the PEM block is %q, not PUBLIC KEY", block.Type))
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return append(problems, "not a public key: "+err.Error())
	}
	ec, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return append(problems, fmt.Sprintf("%T is not an EC key", key))
	}
	if name := ec.Curve.Params().Name; name != "P-256" {
		problems = append(problems, fmt.Sprintf("curve %s, not P-256 (prime256v1)", name))
	}
	return problems
}

// TestNoPrivateKeyCommittable requires that no file git would commit holds a private key: the
// tracked files and the untracked ones .gitignore does not exclude, each read in full, and no
// path ending in .private.pem among them. The feed's private half lives only in the signing
// Mac's keychain, so this is red when a key file is force-added, when .gitignore stops excluding
// one, and when a key sits where .gitignore misses it. It reads git's own list, so a directory
// that is not the top of a git checkout has measured nothing.
func TestNoPrivateKeyCommittable(t *testing.T) {
	files, err := committableFiles(".")
	if err != nil {
		t.Fatalf("measured nothing: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("measured nothing: git lists no file")
	}
	read := 0
	for _, rel := range files {
		if strings.HasSuffix(rel, ".private.pem") {
			t.Errorf("%s: a private key file is tracked or would be committed", rel)
			continue
		}
		data, err := os.ReadFile(filepath.FromSlash(rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue // tracked but deleted in the working tree
		}
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		read++
		if privateKeyPEM.Match(data) {
			t.Errorf("%s: holds a private key", rel)
		}
	}
	t.Logf("%d of %d committable files read", read, len(files))
}

// committableFiles lists, through git, the tracked files of dir and the untracked files that
// .gitignore does not exclude, relative to dir with forward slashes.
func committableFiles(dir string) ([]string, error) {
	top := exec.Command("git", "rev-parse", "--show-toplevel")
	top.Dir = dir
	out, err := top.Output()
	if err != nil {
		return nil, fmt.Errorf("%s is not a git checkout: %v", dir, err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, err
	}
	want, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if want, err = filepath.EvalSymlinks(want); err != nil {
		return nil, err
	}
	if got != want {
		return nil, fmt.Errorf("%s is not the top of a git checkout (git found %s)", want, got)
	}
	ls := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	ls.Dir = dir
	if out, err = ls.Output(); err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %v", dir, err)
	}
	var files []string
	seen := map[string]bool{}
	for rel := range strings.SplitSeq(string(out), "\x00") {
		if rel != "" && !seen[rel] {
			seen[rel] = true
			files = append(files, rel)
		}
	}
	return files, nil
}
