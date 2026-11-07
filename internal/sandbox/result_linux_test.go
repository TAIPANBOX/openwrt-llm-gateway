//go:build linux && (amd64 || arm64)

package sandbox

import (
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// TestSeccompResult is what TSYNC can answer: a thread ID that is not zero is a thread the kernel
// could not give the filter, and that thread would run unfiltered.
func TestSeccompResult(t *testing.T) {
	if err := seccompResult(0, 0); err != nil {
		t.Fatalf("a clean install: %v", err)
	}
	if err := seccompResult(4242, 0); err == nil || !strings.Contains(err.Error(), "4242") {
		t.Fatalf("a thread left out: %v", err)
	}
	if err := seccompResult(0, syscall.EINVAL); err == nil {
		t.Fatal("an errno passed for success")
	}
}

// TestInsnIsSockFilter: the program goes to the kernel as it stands, so Insn has to be laid out as
// struct sock_filter, 8 bytes.
func TestInsnIsSockFilter(t *testing.T) {
	var in Insn
	if unsafe.Sizeof(in) != 8 || unsafe.Offsetof(in.Jt) != 2 || unsafe.Offsetof(in.Jf) != 3 || unsafe.Offsetof(in.K) != 4 {
		t.Fatalf("Insn: size %d, Jt at %d, Jf at %d, K at %d", unsafe.Sizeof(in), unsafe.Offsetof(in.Jt), unsafe.Offsetof(in.Jf), unsafe.Offsetof(in.K))
	}
}

func TestInstallRefusesAProgramTheKernelWouldNot(t *testing.T) {
	if err := install(nil); err == nil {
		t.Error("install took no program")
	}
	if err := install(make([]Insn, bpfMaxInsns+1)); err == nil {
		t.Error("install took a program past the kernel's limit")
	}
}
