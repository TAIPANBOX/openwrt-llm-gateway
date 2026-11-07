//go:build !(linux && (amd64 || arm64))

package sandbox

// Confine installs nothing here and says so with 0: no real run of the daemon stands behind a list
// for this architecture yet, and a wrong list is a daemon that dies on a rare path. The caller says
// it in one line. The lists for arm and mips are there for the day one does.
func Confine() (int, error) { return 0, nil }
