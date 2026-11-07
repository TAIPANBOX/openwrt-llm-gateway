//go:build linux

package sandbox

import (
	"syscall"
	"testing"
)

// TestFilterConstants holds the values filter.go spells out to the standard library's, where it
// has them. filter.go is a copy, and these are the numbers it would be silently wrong with.
func TestFilterConstants(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"BPF_LD", bpfLD, syscall.BPF_LD}, {"BPF_W", bpfW, syscall.BPF_W}, {"BPF_ABS", bpfABS, syscall.BPF_ABS},
		{"BPF_JMP", bpfJMP, syscall.BPF_JMP}, {"BPF_JEQ", bpfJEQ, syscall.BPF_JEQ}, {"BPF_K", bpfK, syscall.BPF_K},
		{"BPF_RET", bpfRET, syscall.BPF_RET},
	} {
		if c.got != c.want {
			t.Errorf("%s is %#x here and %#x in syscall", c.name, c.got, c.want)
		}
	}
}
