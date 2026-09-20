package backend

import (
	"bytes"
	"io"
)

// Invoker is one binary and the four ways ptrbox runs it: with its output
// going to the user (Passthrough), captured (Output), fed from a reader
// (Send), or streamed to a writer as it arrives (Stream). Every backend's
// client is one of these plus what its binary spells differently, so this is
// the part that is not any backend's.
//
// Stdout and Stderr are where passthrough output goes - the provisioning
// chatter of a first boot, say, which belongs on the user's terminal rather
// than in a buffer. A Stdout that is a Narrator is told where each
// invocation begins and ends.
type Invoker struct {
	Binary string
	Runner Runner
	Stdout io.Writer
	Stderr io.Writer
}

// availabler lets a Runner answer "is the binary usable here" for itself,
// which is how a fake reports yes without the binary anywhere on the machine.
type availabler interface{ Available() bool }

// Available reports whether the binary can be run at all.
func (v Invoker) Available() bool {
	if a, ok := v.Runner.(availabler); ok {
		return a.Available()
	}
	return false
}

// Passthrough runs the binary with its output going straight to the user.
func (v Invoker) Passthrough(args ...string) error {
	if n, ok := v.Stdout.(Narrator); ok {
		n.Begin(args)
		err := v.Runner.Run(Cmd{Args: args, Stdout: v.Stdout, Stderr: v.Stderr})
		n.End(err)
		return err
	}
	return v.Runner.Run(Cmd{Args: args, Stdout: v.Stdout, Stderr: v.Stderr})
}

// Output runs the binary and captures stdout. Any stderr becomes part of the
// error, so a caller that fails has something to print.
func (v Invoker) Output(args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	err := v.Runner.Run(Cmd{Args: args, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return stdout.String(), &Error{Binary: v.Binary, Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}

// Send runs the binary with stdin wired to r and captures nothing. It is how
// a payload reaches a guest without ever appearing in an argument vector.
func (v Invoker) Send(stdin io.Reader, args ...string) error {
	var stderr bytes.Buffer
	if err := v.Runner.Run(Cmd{Args: args, Stdin: stdin, Stderr: &stderr}); err != nil {
		return &Error{Binary: v.Binary, Args: args, Stderr: stderr.String(), Err: err}
	}
	return nil
}

// Stream runs the binary with stdout going to w, for output that is consumed
// as it arrives (following a log).
func (v Invoker) Stream(w io.Writer, args ...string) error {
	var stderr bytes.Buffer
	if err := v.Runner.Run(Cmd{Args: args, Stdout: w, Stderr: &stderr}); err != nil {
		return &Error{Binary: v.Binary, Args: args, Stderr: stderr.String(), Err: err}
	}
	return nil
}
