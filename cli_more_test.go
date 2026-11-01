package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestClientListRemoveCertCheckAndVersion(t *testing.T) {
	certDir := t.TempDir()
	conf := "config gateway 'main'\n\tlist zone 'lan'\n\toption cert_dir '" + certDir + "'\n" +
		"config backend 'desktop'\n\toption url 'http://10.0.0.5:11434/v1'\n\tlist model 'm1'\n" +
		"config client 'laptop'\n\toption key_hash '" + HashKey(testKey) + "'\n\tlist model 'm1'\n" +
		"\tlist origin 'http://chat.lan:3000'\n\toption rate '30'\n"
	e, u := testEnv(t, conf)
	var out, errOut bytes.Buffer
	if code := run([]string{"client", "list"}, &out, &errOut, e); code != 0 {
		t.Fatalf("list: %d %s", code, errOut.String())
	}
	if l := out.String(); !strings.Contains(l, "laptop\tmodels m1\torigins http://chat.lan:3000\trate 30") || strings.Contains(l, "sha256:") {
		t.Fatalf("list printed %q", l)
	}
	out.Reset()
	if code := run([]string{"client", "remove", "desktop"}, &out, &errOut, e); code == 0 {
		t.Fatal("remove deleted a backend as if it were a client")
	}
	if code := run([]string{"client", "remove", "laptop"}, &out, &errOut, e); code != 0 {
		t.Fatalf("remove: %d %s", code, errOut.String())
	}
	if len(u.calls) != 1 || !strings.Contains(u.calls[0], "delete llm-gateway.laptop\ncommit llm-gateway") {
		t.Fatalf("uci calls %q", u.calls)
	}
	out.Reset()
	if code := run([]string{"cert"}, &out, &errOut, e); code == 0 {
		t.Fatal("cert printed something before a CA exists")
	}
	c, err := ensureCerts(certDir, []string{"llm.lan"}, houseIPs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"cert"}, &out, &errOut, e); code != 0 || !strings.Contains(out.String(), c.Fingerprint) ||
		!strings.Contains(out.String(), "BEGIN CERTIFICATE") || strings.Contains(out.String(), "PRIVATE KEY") {
		t.Fatalf("cert printed %q", out.String())
	}
	// client add now shows the CA a device has to trust.
	out.Reset()
	if code := run([]string{"client", "add", "phone", "-model", "m1"}, &out, &errOut, e); code != 0 || !strings.Contains(out.String(), c.Fingerprint) {
		t.Fatalf("client add after the CA exists: %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"check"}, &out, &errOut, e); code != 0 || !strings.Contains(out.String(), "1 backend(s), 1 client(s)") {
		t.Fatalf("check: %d %q %q", code, out.String(), errOut.String())
	}
	os.WriteFile(e.configPath, []byte(conf+"config backend 'pub'\n\toption url 'http://8.8.8.8/v1'\n\tlist model 'x'\n"), 0o644)
	errOut.Reset()
	if code := run([]string{"check"}, &out, &errOut, e); code == 0 || !strings.Contains(errOut.String(), "pub") {
		t.Fatalf("check accepted a public backend: %q", errOut.String())
	}
	out.Reset()
	if code := run([]string{"version"}, &out, &errOut, e); code != 0 || strings.TrimSpace(out.String()) == "" {
		t.Fatal("version")
	}
	for _, args := range [][]string{nil, {"nope"}, {"client"}, {"client", "add"}, {"client", "remove"}} {
		if code := run(args, io.Discard, io.Discard, e); code != 2 {
			t.Fatalf("%v exited %d, want 2", args, code)
		}
	}
	if _, err := os.Stat(filepath.Join(certDir, "ca.key")); err != nil {
		t.Fatal(err)
	}
}

func TestUsageIsFoundWhereverTheStreamSplits(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\r\n\r\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5}}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"
	// One byte per read: every split point a network can produce.
	s := newUsageSniffer(io.NopCloser(iotest.OneByteReader(strings.NewReader(stream))), true)
	got, _ := io.ReadAll(s)
	s.Close()
	if string(got) != stream {
		t.Fatal("the stream was changed on the way through")
	}
	if s.got.Prompt != 11 || s.got.Completion != 5 {
		t.Fatalf("usage %+v", s.got)
	}
	// A line longer than the sniffer keeps is passed through and ignored, not buffered.
	long := "data: " + strings.Repeat("a", maxStreamLine+10) + "\n" +
		"data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n"
	s = newUsageSniffer(io.NopCloser(strings.NewReader(long)), true)
	got, _ = io.ReadAll(s)
	if len(got) != len(long) || s.got.Completion != 2 {
		t.Fatalf("long line: %d bytes, usage %+v", len(got), s.got)
	}
	// A JSON body too large to keep is passed through and its usage is not guessed at.
	big := `{"pad":"` + strings.Repeat("b", maxJSON) + `","usage":{"prompt_tokens":9,"completion_tokens":9}}`
	s = newUsageSniffer(io.NopCloser(strings.NewReader(big)), false)
	got, _ = io.ReadAll(s)
	if len(got) != len(big) || s.got.Prompt != 0 {
		t.Fatalf("oversized JSON: %d bytes, usage %+v", len(got), s.got)
	}
}
