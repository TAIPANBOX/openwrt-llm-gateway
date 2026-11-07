//go:build linux && !race && (amd64 || arm64)

// The race detector's runtime makes system calls of its own, which no list here allows, so this runs
// without it: in `go test ./...` and in CI's seccomp job, on amd64 and on arm64.

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/TAIPANBOX/openwrt-llm-gateway/internal/sandbox"
)

// daemonChildEnv holds the plan of a re-executed test binary, which runs the real serve, with the real
// filter, instead of the tests. It is an environment variable and not a file on purpose: the first
// file a process opens starts the runtime's poller, and a child that opened one before serve would
// make eventfd2 and epoll_create1 before its filter, where the daemon makes them after.
const daemonChildEnv = "LLMGW_DAEMON_CHILD"

type childPlan struct {
	ConfigPath string
	RunDir     string
	Listen     map[string]string // the address serve asks for, and the one that stands for it
}

func TestMain(m *testing.M) {
	if plan := os.Getenv(daemonChildEnv); plan != "" {
		os.Exit(daemonChild(plan))
	}
	os.Exit(m.Run())
}

// probingResolver sends one real DNS query before each lookup: a name that cannot resolve, so the
// resolver's whole way out (resolv.conf, a socket, the answer) runs under the filter, on any machine.
type probingResolver struct{}

func (probingResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	net.DefaultResolver.LookupIPAddr(probe, "nothing-here.invalid")
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

func daemonChild(rawPlan string) int {
	var plan childPlan
	if err := json.Unmarshal([]byte(rawPlan), &plan); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return serve(ctx, &daemonEnv{
		configPath: plan.ConfigPath,
		runDir:     plan.RunDir,
		resolver:   probingResolver{},
		now:        time.Now,
		listen:     func(network, addr string) (net.Listener, error) { return net.Listen(network, plan.Listen[addr]) },
		euid:       os.Geteuid,
		confine:    sandbox.Confine,
		procStatus: func() ([]byte, error) { return os.ReadFile("/proc/self/status") },
	}, os.Stderr)
}

// staticChild builds this test binary as the daemon is built, with CGO_ENABLED=0. It has to be: a test
// binary linked with cgo starts its threads through glibc, whose clone3 and rseq the daemon never
// makes, so a run of it would prove nothing about the list, in either direction.
func staticChild(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "daemon.test")
	cmd := exec.Command("go", "test", "-c", "-o", out, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the static child: %v\n%s", err, msg)
	}
	return out
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// filterBackend is the model server: a plain answer, a stream, and a stream that goes on until the
// client leaves. It records what it was sent.
type filterBackend struct {
	srv      *httptest.Server
	mu       sync.Mutex
	sawUp    []string
	requests atomic.Int64
}

func newFilterBackend(t *testing.T) *filterBackend {
	t.Helper()
	b := &filterBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.requests.Add(1)
		io.Copy(io.Discard, r.Body)
		b.mu.Lock()
		if v := r.Header.Get("Upgrade"); v != "" {
			b.sawUp = append(b.sawUp, v)
		}
		b.mu.Unlock()
		switch r.Header.Get("X-Test-Kind") {
		case "stream", "endless":
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; r.Header.Get("X-Test-Kind") == "endless" || i < 5; i++ {
				if _, err := fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chunk %d\"}}]}\n\n", i); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(5 * time.Millisecond)
			}
			io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// TestDaemonRunsUnderItsFilter runs the real serve in a process of its own, with the real filter on
// every thread and nothing stood in for but the two addresses, through everything a daemon does:
// TLS, a request to a backend by name, a stream, a client that leaves mid-stream, a backend that is
// down, many at once, a body of a megabyte, the journal's rotation, and SIGTERM. A call the list
// lacks ends it with SIGSYS, and that is this test failing.
func TestDaemonRunsUnderItsFilter(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("serve refuses root, as it must")
	}
	b := newFilterBackend(t)
	_, port, err := net.SplitHostPort(b.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	down := freePort(t)
	_, downPort, _ := net.SplitHostPort(down)
	// An https backend under a CA nobody here trusts: the answer is a 502, and on the way the daemon
	// reads the system's roots, which is getdents64 and readlinkat, and a TLS client's whole path.
	strange := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(strange.Close)
	_, strangePort, _ := net.SplitHostPort(strange.Listener.Addr().String())
	conf := "config backend 'alive'\n\toption url 'http://localhost:" + port + "/v1'\n\tlist model 'm1'\n" +
		"config backend 'gone'\n\toption url 'http://localhost:" + downPort + "/v1'\n\tlist model 'm2'\n" +
		"config backend 'strange'\n\toption url 'https://localhost:" + strangePort + "/v1'\n\tlist model 'm3'\n" +
		"config client 'laptop'\n\toption key_hash '" + HashKey(testKey) + "'\n\tlist model '*'\n"
	e, _, certDir := prepareEnv(t, conf)
	runPrepare(t, e)

	// A journal just short of its limit, so a few requests rotate it.
	journal := filepath.Join(e.runDir, "journal", "journal.log")
	if err := os.WriteFile(journal, bytes.Repeat([]byte("{}\n"), (1<<20)/3-40), 0o600); err != nil {
		t.Fatal(err)
	}
	lan, loop := freePort(t), freePort(t)
	plan, _ := json.Marshal(childPlan{ConfigPath: e.configPath, RunDir: e.runDir,
		Listen: map[string]string{"192.168.8.1:8760": lan, "127.0.0.1:8761": loop}})
	var log bytes.Buffer
	cmd := exec.Command(staticChild(t), "-test.run=^$")
	// The collector often, so the heap grows and shrinks: mmap, munmap and madvise run too.
	cmd.Env = append(os.Environ(), daemonChildEnv+"="+string(plan), "GOGC=5")
	// One writer for both: two would race on the buffer, and the log would come back empty.
	out := &syncWriter{w: &log}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	fail := func(format string, a ...any) {
		t.Helper()
		cmd.Process.Kill()
		err := <-exited
		t.Fatalf(format+"\nthe daemon: %v\nits log:\n%s", append(a, err, log.String())...)
	}

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(readAll(t, filepath.Join(certDir, "ca.crt")))
	plain := &http.Client{Timeout: 20 * time.Second}
	viaTLS := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "llm.lan"},
		DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, n, lan)
		},
	}}
	defer viaTLS.CloseIdleConnections()
	defer plain.CloseIdleConnections()

	deadline := time.Now().Add(15 * time.Second)
	for {
		if resp, err := plain.Get("http://" + loop + "/health"); err == nil {
			resp.Body.Close()
			break
		}
		select {
		case err := <-exited:
			exited <- err
			fail("the daemon stopped before it answered")
		default:
		}
		if time.Now().After(deadline) {
			fail("the daemon did not answer within 15 s")
		}
		time.Sleep(20 * time.Millisecond)
	}

	ask := func(c *http.Client, url, kind, model string, body []byte) (int, string) {
		t.Helper()
		if body == nil {
			body = []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hello"}]}`)
		}
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "llm.lan"
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Content-Type", "application/json")
		if kind != "" {
			req.Header.Set("X-Test-Kind", kind)
		}
		resp, err := c.Do(req)
		if err != nil {
			fail("%s: %v", url, err)
		}
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(got)
	}
	chat := "https://llm.lan/v1/chat/completions"
	if code, body := ask(viaTLS, chat, "", "m1", nil); code != 200 || !strings.Contains(body, "hi") {
		fail("over TLS, a chat to a backend by name: %d %s", code, body)
	}
	if code, body := ask(viaTLS, chat, "stream", "m1", nil); code != 200 || !strings.Contains(body, "chunk 4") || !strings.Contains(body, "[DONE]") {
		fail("a stream: %d %s", code, body)
	}
	if code, _ := ask(plain, "http://"+loop+"/v1/chat/completions", "", "m2", nil); code != http.StatusBadGateway {
		fail("a backend that is down: %d", code)
	}
	if code, _ := ask(plain, "http://"+loop+"/v1/chat/completions", "", "m3", nil); code != http.StatusBadGateway {
		fail("an https backend nobody trusts: %d", code)
	}
	big := append(append([]byte(`{"model":"m1","messages":[{"role":"user","content":"`), bytes.Repeat([]byte("x"), 1<<20)...), []byte(`"}]}`)...)
	if code, _ := ask(viaTLS, chat, "", "m1", big); code != 200 {
		fail("a body of a megabyte: %d", code)
	}
	// A client that asks to switch protocol: the backend must not hear of it.
	up, err := http.NewRequest(http.MethodPost, "http://"+loop+"/v1/chat/completions", strings.NewReader(`{"model":"m1"}`))
	if err != nil {
		t.Fatal(err)
	}
	up.Host = "llm.lan"
	up.Header.Set("Authorization", "Bearer "+testKey)
	up.Header.Set("Connection", "Upgrade")
	up.Header.Set("Upgrade", "websocket")
	if resp, err := plain.Do(up); err != nil {
		fail("a request to switch protocol: %v", err)
	} else {
		resp.Body.Close()
	}
	// A client that leaves in the middle of a stream.
	ctx, cancel := context.WithCancel(context.Background())
	mid, _ := http.NewRequestWithContext(ctx, http.MethodPost, chat, strings.NewReader(`{"model":"m1","stream":true}`))
	mid.Host = "llm.lan"
	mid.Header.Set("Authorization", "Bearer "+testKey)
	mid.Header.Set("X-Test-Kind", "endless")
	resp, err := viaTLS.Do(mid)
	if err != nil {
		cancel()
		fail("the endless stream: %v", err)
	}
	io.ReadFull(resp.Body, make([]byte, 64))
	cancel()
	resp.Body.Close()
	// Many at once, on connections of their own: threads, the poller, the collector.
	var wg sync.WaitGroup
	var bad atomic.Int64
	for i := range 24 {
		wg.Go(func() {
			c := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DisableKeepAlives: true,
				TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "llm.lan"},
				DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, n, lan)
				}}}
			for range 6 {
				req, _ := http.NewRequest(http.MethodPost, chat, strings.NewReader(`{"model":"m1"}`))
				req.Host = "llm.lan"
				req.Header.Set("Authorization", "Bearer "+testKey)
				if i%3 == 0 {
					req.Header.Set("X-Test-Kind", "stream")
				}
				r, err := c.Do(req)
				if err != nil {
					bad.Add(1)
					return
				}
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
				if r.StatusCode != 200 {
					bad.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if n := bad.Load(); n != 0 {
		fail("%d of the requests at once failed", n)
	}
	// Long enough for the background of the runtime to run: sysmon, the scavenger, preemption.
	time.Sleep(1500 * time.Millisecond)

	// The kernel's word, on every thread.
	pid := cmd.Process.Pid
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil || len(tasks) < 2 {
		fail("the daemon's threads: %v, %d", err, len(tasks))
	}
	for _, task := range tasks {
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/status", pid, task.Name()))
		if err != nil {
			continue // a thread that has just ended
		}
		if err := sandbox.CheckStatus(status); err != nil {
			fail("thread %s: %v", task.Name(), err)
		}
	}
	b.mu.Lock()
	saw := b.sawUp
	b.mu.Unlock()
	if len(saw) != 0 {
		fail("a backend was asked to switch protocol: %v", saw)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		fail("SIGTERM: %v", err)
	}
	select {
	case err := <-exited:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				t.Fatalf("the daemon was killed by %v: a call its list lacks\nits log:\n%s", ws.Signal(), log.String())
			}
		}
		if err != nil {
			t.Fatalf("the daemon ended with %v, want a clean stop on SIGTERM\nits log:\n%s", err, log.String())
		}
	case <-time.After(10 * time.Second):
		fail("the daemon did not stop within 10 s of SIGTERM")
	}
	if !strings.Contains(log.String(), "seccomp filter on every thread") {
		t.Fatalf("the daemon does not say its filter is on:\n%s", log.String())
	}
	st, err := os.Stat(journal + ".1")
	if err != nil || st.Size() < 1<<19 {
		t.Fatalf("the journal did not rotate: %v", err)
	}
	if got := readAll(t, journal); !bytes.Contains(got, []byte(`"client":"laptop"`)) || bytes.Contains(got, []byte(testKey)) {
		t.Fatalf("the journal after its rotation: %d bytes", len(got))
	}
}
