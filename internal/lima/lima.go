// Package lima is the whole of ptrbox's contact with limactl.
//
// Everything goes through Runner, which exists so the test suite can simulate
// a Mac's worth of VMs without one: the fake in limafake answers the same
// calls from canned state, which is what makes install, provisioning and
// teardown testable on Linux, in CI, and on a Mac with nothing running.
//
// Commands are built as argument vectors and executed directly - never
// through a shell - so a repo path or a domain with a space in it cannot
// become a second command.
package lima

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Binary is the executable every call goes to.
const Binary = "limactl"

// Cmd is one limactl invocation.
type Cmd struct {
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner executes limactl invocations.
type Runner interface {
	Run(Cmd) error
}

// Exec is the real runner.
type Exec struct{}

func (Exec) Run(c Cmd) error {
	cmd := exec.Command(Binary, c.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	return cmd.Run()
}

// Available reports whether limactl is on PATH.
func (Exec) Available() bool {
	_, err := exec.LookPath(Binary)
	return err == nil
}

// availabler lets a Runner answer "is limactl usable here" for itself, which
// is how the fake reports yes without a limactl anywhere on the machine.
type availabler interface{ Available() bool }

// Client is the typed interface to limactl. Stdout and Stderr are where
// passthrough output goes - the provisioning chatter of `limactl start`, say,
// which belongs on the user's terminal rather than in a buffer.
type Client struct {
	Runner Runner
	Stdout io.Writer
	Stderr io.Writer
}

// Available reports whether limactl can be run at all. Several commands lead
// with this so the failure is "run ptrbox install first" rather than an exec
// error from three layers down.
func (c *Client) Available() bool {
	if a, ok := c.Runner.(availabler); ok {
		return a.Available()
	}
	return false
}

// Passthrough runs limactl with its output going straight to the user.
func (c *Client) Passthrough(args ...string) error {
	return c.Runner.Run(Cmd{Args: args, Stdout: c.Stdout, Stderr: c.Stderr})
}

// Output runs limactl and captures stdout. Any stderr becomes part of the
// error, so a caller that fails has something to print.
func (c *Client) Output(args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	err := c.Runner.Run(Cmd{Args: args, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return stdout.String(), &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}

// Send runs limactl with stdin wired to r and captures nothing. Used for the
// auth token, which must reach the guest without ever appearing in an
// argument vector.
func (c *Client) Send(stdin io.Reader, args ...string) error {
	var stderr bytes.Buffer
	if err := c.Runner.Run(Cmd{Args: args, Stdin: stdin, Stderr: &stderr}); err != nil {
		return &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return nil
}

// Stream runs limactl with stdout going to w, for output that is consumed as
// it arrives (following a log).
func (c *Client) Stream(w io.Writer, args ...string) error {
	var stderr bytes.Buffer
	if err := c.Runner.Run(Cmd{Args: args, Stdout: w, Stderr: &stderr}); err != nil {
		return &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return nil
}

// Error carries what limactl said, which is usually more useful than the exit
// status on its own.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s %s: %v", Binary, strings.Join(e.Args, " "), e.Err)
	if trimmed := strings.TrimSpace(e.Stderr); trimmed != "" {
		msg += ": " + trimmed
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// --- VM state ----------------------------------------------------------------

// StatusRunning is the only status ptrbox tests for by name.
const StatusRunning = "Running"

// VM is one entry of `limactl list`.
type VM struct {
	Name   string
	Status string
}

// List returns every VM limactl knows about. A listing that fails returns no
// VMs and no error: callers use this to decide whether to leave the proxy
// running, and "I could not tell" must land on the same side as "yes,
// something is running".
func (c *Client) List() []VM {
	out, err := c.Output("list", "--format", "{{.Name}} {{.Status}}")
	if err != nil {
		return nil
	}
	var vms []VM
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		vms = append(vms, VM{Name: fields[0], Status: fields[1]})
	}
	return vms
}

// Names returns just the VM names, as `limactl list -q` prints them.
func (c *Client) Names() []string {
	out, err := c.Output("list", "-q")
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// Status is the VM's status, or "" if it does not exist.
func (c *Client) Status(name string) string {
	for _, vm := range c.List() {
		if vm.Name == name {
			return vm.Status
		}
	}
	return ""
}

// Exists reports whether limactl knows this VM, by name, the way
// `limactl list -q` does.
func (c *Client) Exists(name string) bool {
	for _, n := range c.Names() {
		if n == name {
			return true
		}
	}
	return false
}

// Running reports whether the VM exists and is up.
func (c *Client) Running(name string) bool { return c.Status(name) == StatusRunning }

// --- lifecycle ---------------------------------------------------------------

// Validate checks a rendered config before any VM state is touched.
func (c *Client) Validate(config string) error {
	return c.Passthrough("validate", config)
}

// Create provisions a new VM from a rendered config.
//
// The timeout is generous on purpose: first boot downloads the base image and
// runs installers, and limactl's default 10m is not always enough - while a
// timeout aborts ptrbox and leaves the VM provisioning in the background.
func (c *Client) Create(name, config string) error {
	return c.Passthrough("start", "--name", name, "-y", "--timeout", "20m", config)
}

// Start boots an existing VM.
func (c *Client) Start(name string) error { return c.Passthrough("start", name) }

// Stop powers a VM off, keeping its disk and state.
func (c *Client) Stop(name string) error { return c.Passthrough("stop", name) }

// Delete destroys a VM and its disk.
func (c *Client) Delete(name string) error { return c.Passthrough("delete", "-f", name) }

// --- in-guest execution ------------------------------------------------------

// ShellArgs builds the argv for running a command inside a VM.
func ShellArgs(vm string, args ...string) []string {
	return append([]string{"shell", vm, "--"}, args...)
}
