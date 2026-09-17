//go:build !windows

package ui

import "os"

func terminal(f *os.File) bool {
	// An unset TERM is the same answer as "dumb": something is running ptrbox
	// that never said it could render anything.
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
