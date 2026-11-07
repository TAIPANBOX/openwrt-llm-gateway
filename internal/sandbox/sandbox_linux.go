//go:build linux && (amd64 || arm64)

package sandbox

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// The kernel's values, from linux/prctl.h, linux/seccomp.h and linux/filter.h: the standard
// library has neither these nor seccomp's number on every architecture.
const (
	prSetNoNewPrivs        = 38
	seccompSetModeFilter   = 1
	seccompFilterFlagTSYNC = 1
	bpfMaxInsns            = 4096
)

// sockFprog is struct sock_fprog. Insn has the layout of struct sock_filter.
type sockFprog struct {
	Len    uint16
	Filter *Insn
}

// Confine installs the seccomp filter of this architecture on every thread of the process and
// returns the number of system calls it allows. From then on any other call, and any call made
// with another architecture's numbering, ends the whole process with SIGSYS. It sets
// no_new_privs first, which an unprivileged process needs for a filter; a filter, once installed,
// stays for the life of the process.
//
// The daemon calls it once, first thing. procd cannot do it for the daemon: it applies a profile
// through LD_PRELOAD=/lib/libpreload-seccomp.so, which a statically linked binary never loads.
//
// This file is built only where a real run of the daemon stands behind the list: amd64 and arm64.
// Elsewhere Confine is sandbox_off.go's, and the filter stays off, because a list that is wrong
// on a rare path is a daemon that dies on a router.
func Confine() (int, error) {
	names, err := Allowed(runtime.GOARCH)
	if err != nil {
		return 0, err
	}
	nrs, err := Numbers(names, sysnums)
	if err != nil {
		return 0, err
	}
	prog, err := Build(auditArch, nrs)
	if err != nil {
		return 0, err
	}
	if err := install(prog); err != nil {
		return 0, err
	}
	return len(nrs), nil
}

// install sets no_new_privs and installs prog with SECCOMP_FILTER_FLAG_TSYNC, so the kernel puts
// the filter, and no_new_privs, on every thread of the process at once: the Go runtime runs
// several threads before main starts, and a thread left out would run unfiltered.
func install(prog []Insn) error {
	if len(prog) == 0 || len(prog) > bpfMaxInsns {
		return fmt.Errorf("seccomp: a program of %d instructions", len(prog))
	}
	fprog := sockFprog{Len: uint16(len(prog)), Filter: &prog[0]}

	// no_new_privs is per thread and the kernel checks it on the thread that installs the
	// filter, so both calls are made on one thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("no_new_privs: %w", errno)
	}
	r, _, errno := syscall.RawSyscall(uintptr(sysnums["seccomp"]), seccompSetModeFilter,
		seccompFilterFlagTSYNC, uintptr(unsafe.Pointer(&fprog)))
	runtime.KeepAlive(prog)
	return seccompResult(r, errno)
}

// seccompResult reads what seccomp(SECCOMP_SET_MODE_FILTER, SECCOMP_FILTER_FLAG_TSYNC) returned:
// 0 when every thread has the filter, an errno when the kernel refused it, and otherwise the ID
// of a thread it could not synchronise. That thread would run unfiltered, so it fails too.
func seccompResult(r uintptr, errno syscall.Errno) error {
	switch {
	case errno != 0:
		return fmt.Errorf("seccomp: %w", errno)
	case r != 0:
		return fmt.Errorf("seccomp: thread %d could not be given the filter, so it would run unfiltered", r)
	}
	return nil
}
