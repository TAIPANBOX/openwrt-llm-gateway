package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// rollcallFilter is the SHA-256 of rollcall's internal/sandbox/filter.go at 6cbface, below its
// package line. Changing it means the copy was changed on purpose.
const rollcallFilter = "98452a44e53bc85ed6b2687953ca9927644e1f13a5cfced85997dfc564da2613"

// sandboxDir is where these gates read the sources: this directory, or a tree the teeth planted.
func sandboxDir() string {
	if d := os.Getenv("SANDBOX_SRC"); d != "" {
		return d
	}
	return "."
}

func TestFilterIsRollcallsCopy(t *testing.T) {
	raw, err := os.ReadFile(sandboxDir() + "/filter.go")
	if err != nil {
		t.Fatalf("measured nothing: %v", err)
	}
	_, body, ok := strings.Cut(string(raw), "\npackage sandbox\n")
	if !ok {
		t.Fatal("measured nothing: filter.go has no package line")
	}
	sum := sha256.Sum256([]byte(body))
	if got := hex.EncodeToString(sum[:]); got != rollcallFilter {
		t.Fatalf("filter.go below its package line has SHA-256 %s, want rollcall's %s: it is no longer the copy it says it is", got, rollcallFilter)
	}
}

// families are the lists, each with the table its numbers come from.
var families = map[string]string{"amd64": "amd64", "arm64": "arm64", "arm": "arm", "mipsle": "mipsle"}

// tableNames reads the names of a generated table as text, so one test covers every architecture
// whatever this one is.
func tableNames(t *testing.T, goarch string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(sandboxDir() + "/zsysnum_linux_" + goarch + ".go")
	if err != nil {
		t.Fatalf("measured nothing: %v", err)
	}
	names := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*"([a-z0-9_]+)":\s*[0-9]+,$`).FindAllSubmatch(raw, -1) {
		names[string(m[1])] = true
	}
	if len(names) < 200 {
		t.Fatalf("measured nothing: %d names in the %s table", len(names), goarch)
	}
	return names
}

func listFor(t *testing.T, goarch string) []string {
	t.Helper()
	lists := os.DirFS(sandboxDir() + "/seccomp")
	names, err := allowedFrom(lists, goarch)
	if err != nil {
		t.Fatalf("measured nothing: %s: %v", goarch, err)
	}
	if len(names) == 0 {
		t.Fatalf("measured nothing: an empty list for %s", goarch)
	}
	return names
}

// TestListNamesExistPerFamily: a name the architecture does not have protects nothing, and the call
// the daemon really makes would be missing. Numbers refuses it at start, on a router; this refuses
// it here.
func TestListNamesExistPerFamily(t *testing.T) {
	for goarch := range families {
		table := tableNames(t, goarch)
		for _, name := range listFor(t, goarch) {
			if !table[name] {
				t.Errorf("%s: %s is no system call of this architecture", goarch, name)
			}
		}
	}
}

// TestListNeverAllowsAProgram: the filter is what stands behind "the daemon cannot start a program".
// ptrace and the mount and namespace calls would each be a way round the jail.
func TestListNeverAllowsAProgram(t *testing.T) {
	for goarch := range families {
		for _, name := range listFor(t, goarch) {
			switch name {
			case "execve", "execveat", "fork", "vfork", "ptrace", "process_vm_readv", "process_vm_writev",
				"mount", "umount2", "pivot_root", "chroot", "unshare", "setns", "bpf", "seccomp",
				"setuid", "setgid", "setresuid", "setresgid", "setgroups", "capset", "init_module", "finit_module", "kexec_load":
				t.Errorf("%s: the list allows %s", goarch, name)
			}
		}
	}
}

// TestListAllowsTheCopyPaths: net/http reaches splice and sendfile through io.Copy when a response
// body is a socket or a file. No path here gets there today, by chance rather than by design, and
// SIGSYS is a dear way to find out the day one does.
func TestListAllowsTheCopyPaths(t *testing.T) {
	for goarch, want := range map[string][]string{
		"amd64": {"splice", "sendfile"}, "arm64": {"splice", "sendfile"},
		"arm": {"splice", "sendfile", "sendfile64"}, "mipsle": {"splice", "sendfile", "sendfile64"},
	} {
		list := listFor(t, goarch)
		for _, name := range want {
			if !slices.Contains(list, name) {
				t.Errorf("%s: the list lacks %s", goarch, name)
			}
		}
	}
}

// needs are the calls no run may go without, each with what makes it. The real proof is
// TestDaemonRunsUnderItsFilter; this names them where a reader looks.
var needs = []struct{ name, why string }{
	{"futex", "the runtime's locks"},
	{"mmap", "the runtime's heap"},
	{"munmap", "the runtime's heap"},
	{"madvise", "the scavenger giving memory back"},
	{"clone", "a new thread"},
	{"rt_sigaction", "os/signal and the runtime"},
	{"rt_sigprocmask", "a new thread"},
	{"rt_sigreturn", "return from a signal: goroutine preemption"},
	{"sigaltstack", "a new thread"},
	{"tgkill", "goroutine preemption"},
	{"epoll_pwait", "the network poller"},
	{"epoll_ctl", "the network poller"},
	{"epoll_create1", "the network poller"},
	{"eventfd2", "the network poller, at the first file opened"},
	{"accept4", "a connection"},
	{"getrandom", "crypto/rand"},
	{"openat", "the snapshot, the journal"},
	{"renameat", "the journal's rotation"},
	{"exit_group", "the end"},
}

func TestListHoldsWhatEveryRunNeeds(t *testing.T) {
	for _, goarch := range []string{"amd64", "arm64"} {
		list := listFor(t, goarch)
		for _, n := range needs {
			if !slices.Contains(list, n.name) {
				t.Errorf("%s: the list lacks %s, which %s needs", goarch, n.name, n.why)
			}
		}
	}
}

// TestFilterOnlyWhereARealRunStandsBehindIt: the build lines decide where the filter goes on. A list
// nobody ran is a daemon that dies on a rare path, so adding an architecture here is a statement
// that it was run.
func TestFilterOnlyWhereARealRunStandsBehindIt(t *testing.T) {
	const on = "//go:build linux && (amd64 || arm64)"
	const off = "//go:build !(linux && (amd64 || arm64))"
	for file, want := range map[string]string{"sandbox_linux.go": on, "sandbox_off.go": off} {
		raw, err := os.ReadFile(sandboxDir() + "/" + file)
		if err != nil {
			t.Fatalf("measured nothing: %v", err)
		}
		if first, _, _ := strings.Cut(string(raw), "\n"); first != want {
			t.Errorf("%s is built under %q, want %q", file, first, want)
		}
	}
}

func TestListsAreReadWithCare(t *testing.T) {
	good := fstest.MapFS{"common.txt": {Data: []byte("# comment\n\nread  \nwrite\nread\n")}, "amd64.txt": {Data: []byte("mmap\n")}}
	got, err := allowedFrom(good, "amd64")
	if err != nil || !slices.Equal(got, []string{"mmap", "read", "write"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := allowedFrom(good, "riscv64"); err == nil {
		t.Error("an architecture with no family was read")
	}
	if _, err := allowedFrom(good, "arm64"); err == nil {
		t.Error("a family with no file was read")
	}
	bad := fstest.MapFS{"common.txt": {Data: []byte("read write\n")}, "amd64.txt": {Data: []byte("")}}
	if _, err := allowedFrom(bad, "amd64"); err == nil || !strings.Contains(err.Error(), "common.txt:1") {
		t.Errorf("two names on a line: %v", err)
	}
	if isName("") || isName("Read") || !isName("_llseek") {
		t.Error("isName")
	}
	for goarch, want := range map[string]string{"amd64": "amd64", "arm64": "aarch64", "arm": "arm", "mips": "mips", "mipsle": "mips", "riscv64": ""} {
		if got := family(goarch); got != want {
			t.Errorf("family(%s) = %q, want %q", goarch, got, want)
		}
	}
	if names, err := Allowed("amd64"); err != nil || len(names) < 40 {
		t.Errorf("the embedded list: %d names, %v", len(names), err)
	}
}

func TestCheckStatus(t *testing.T) {
	for name, c := range map[string]struct {
		status string
		ok     bool
	}{
		"in force":        {"Name:\tx\nNoNewPrivs:\t1\nSeccomp:\t2\nSeccomp_filters:\t1\n", true},
		"no filter":       {"NoNewPrivs:\t1\nSeccomp:\t0\n", false},
		"strict mode":     {"NoNewPrivs:\t1\nSeccomp:\t1\n", false},
		"no_new_privs 0":  {"NoNewPrivs:\t0\nSeccomp:\t2\n", false},
		"says neither":    {"Name:\tx\n", false},
		"empty":           {"", false},
		"another's field": {"Seccomp_filters:\t2\nNoNewPrivs:\t1\n", false},
	} {
		if err := CheckStatus([]byte(c.status)); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}
