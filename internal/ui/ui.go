// Package ui is ptrbox's output voice.
//
// Everything informational goes to stderr so command output - a log tail, the
// allowlist - stays pipeable. Errors are returned rather than printed here;
// only main decides that a run is over.
package ui

import (
	"fmt"
	"io"
)

// Printer writes ptrbox's progress notes.
type Printer struct{ W io.Writer }

// Say reports something that happened.
func (p Printer) Say(format string, a ...any) {
	fmt.Fprintf(p.W, "ptrbox: %s\n", fmt.Sprintf(format, a...))
}

// Warn reports something worth knowing that is not fatal.
func (p Printer) Warn(format string, a ...any) {
	fmt.Fprintf(p.W, "ptrbox: warning: %s\n", fmt.Sprintf(format, a...))
}

// Raw writes text through unchanged - for output that came from somewhere
// else, like squid's complaint about a config it refused.
func (p Printer) Raw(s string) { fmt.Fprintln(p.W, s) }
