// Package backend is what ptrbox needs from whatever builds its VMs, said
// without naming the tool that does it.
//
// It imports nothing from config, lima or cli: those depend on it, never the
// reverse. This file is the bottom of that - one invocation of a backend's
// binary, the thing that executes it, and the error it comes back with. It
// was lifted out of internal/lima unchanged in meaning, and lima keeps the
// names as aliases, so the fake and every caller compile as they did.
//
// Commands are built as argument vectors and executed directly - never
// through a shell - so a repo path or a domain with a space in it cannot
// become a second command.
package backend

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Cmd is one invocation of a backend's binary.
type Cmd struct {
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Interactive says a person is on the other end of these streams, which
	// makes the invocation unbounded in time: it ends when they end it. A
	// runner that imposes a deadline (multipass's, against a wedging daemon)
	// must not impose it here - killing a session someone is working in is
	// the failure, not the protection, and the person at the keyboard is the
	// timeout. Only `ptrbox shell` sets it.
	Interactive bool
}

// Runner executes invocations. It exists so the test suite can simulate a
// machine's worth of VMs without one: a fake answers the same calls from
// canned state.
type Runner interface {
	Run(Cmd) error
}

// ExecRunner is the real runner: it executes Binary, found on PATH.
type ExecRunner struct {
	Binary string
}

func (r ExecRunner) Run(c Cmd) error {
	cmd := exec.Command(r.Binary, c.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	return cmd.Run()
}

// Available reports whether Binary is on PATH.
func (r ExecRunner) Available() bool {
	_, err := exec.LookPath(r.Binary)
	return err == nil
}

// Narrator is an output writer that wants to know where one invocation begins
// and ends: enough to translate the stream into ptrbox's voice, and to hand
// the raw bytes back when the invocation fails.
//
// Declared here rather than imported so that a backend keeps knowing nothing
// about how output is presented. A plain io.Writer is still a perfectly good
// Stdout.
type Narrator interface {
	io.Writer
	Begin(args []string)
	End(err error)
}

// Error carries what the binary said, which is usually more useful than the
// exit status on its own.
type Error struct {
	Binary string
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s %s: %v", e.Binary, strings.Join(e.Args, " "), e.Err)
	if trimmed := strings.TrimSpace(e.Stderr); trimmed != "" {
		msg += ": " + trimmed
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }
