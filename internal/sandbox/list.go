package sandbox

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"unicode"
)

//go:embed seccomp/*.txt
var embedded embed.FS

// family is the list of seccomp/ that a GOARCH takes beside common.txt.
func family(goarch string) string {
	switch goarch {
	case "amd64":
		return "amd64"
	case "arm64":
		return "aarch64"
	case "arm":
		return "arm"
	case "mips", "mipsle":
		return "mips"
	}
	return ""
}

// Allowed returns the system calls the filter for goarch allows: common.txt plus the family's list.
func Allowed(goarch string) ([]string, error) {
	lists, err := fs.Sub(embedded, "seccomp")
	if err != nil {
		return nil, err
	}
	return allowedFrom(lists, goarch)
}

// allowedFrom reads the lists: a line starting with # and a blank line drop out, trailing white
// space goes, and every other line must be one name of [a-z0-9_]; the result is sorted, once each.
func allowedFrom(lists fs.FS, goarch string) ([]string, error) {
	fam := family(goarch)
	if fam == "" {
		return nil, fmt.Errorf("no system call list for linux/%s", goarch)
	}
	var names []string
	for _, file := range []string{"common.txt", fam + ".txt"} {
		data, err := fs.ReadFile(lists, file)
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			line = strings.TrimRightFunc(line, unicode.IsSpace)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !isName(line) {
				return nil, fmt.Errorf("%s:%d: %q is not a system call name", file, i+1, line)
			}
			names = append(names, line)
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// isName reports whether s is made of [a-z0-9_] only.
func isName(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return s != ""
}

// CheckStatus reads the text of /proc/self/status and requires what the kernel must show once the
// filter is on: no_new_privs set and seccomp in filter mode. Anything else is an error, a status
// that says neither included: a daemon that cannot show its filter does not listen.
func CheckStatus(status []byte) error {
	var noNewPrivs, seccomp string
	for line := range strings.Lines(string(status)) {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch k {
		case "NoNewPrivs":
			noNewPrivs = strings.TrimSpace(v)
		case "Seccomp":
			seccomp = strings.TrimSpace(v)
		}
	}
	switch {
	case noNewPrivs != "1":
		return fmt.Errorf("no_new_privs is not set (NoNewPrivs: %q)", noNewPrivs)
	case seccomp != "2":
		return fmt.Errorf("seccomp is not in filter mode (Seccomp: %q)", seccomp)
	}
	return nil
}
