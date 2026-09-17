package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// What can be said about Terminal without a terminal, which is the only way a
// test suite ever runs: everything that is not one must be refused, on every
// platform - a pipe and a file are neither a tty nor a Windows console. The
// yes path needs a real console and belongs to a person looking at one.

func TestAPipeIsNotATerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if Terminal(w) {
		t.Error("a pipe was taken for a terminal: escapes would reach whatever reads it")
	}
}

func TestAFileIsNotATerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if Terminal(f) {
		t.Error("a file was taken for a terminal: escapes would be written into a log")
	}
}
