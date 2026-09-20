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
	"io"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/backend"
)

// Binary is the executable every call goes to.
const Binary = "limactl"

// The invocation, the thing that runs it, its error and the narrator are the
// backend package's: nothing about them is lima's. The names stay here as
// aliases, so limafake and every caller read as they always did.
type (
	Cmd      = backend.Cmd
	Runner   = backend.Runner
	Error    = backend.Error
	Narrator = backend.Narrator
)

// Exec is the real runner.
type Exec struct{}

func (Exec) Run(c Cmd) error { return backend.ExecRunner{Binary: Binary}.Run(c) }

// Available reports whether limactl is on PATH.
func (Exec) Available() bool { return backend.ExecRunner{Binary: Binary}.Available() }

// Client is the typed interface to limactl. Stdout and Stderr are where
// passthrough output goes - the provisioning chatter of `limactl start`, say,
// which belongs on the user's terminal rather than in a buffer.
type Client struct {
	Runner Runner
	Stdout io.Writer
	Stderr io.Writer
}

// invoker is the four invocation shapes, which are the backend package's;
// the fields stay on Client so that every constructor and the fake read as
// they always did.
func (c *Client) invoker() backend.Invoker {
	return backend.Invoker{Binary: Binary, Runner: c.Runner, Stdout: c.Stdout, Stderr: c.Stderr}
}

// Available reports whether limactl can be run at all. Several commands lead
// with this so the failure is "run ptrbox install first" rather than an exec
// error from three layers down.
func (c *Client) Available() bool { return c.invoker().Available() }

// Passthrough runs limactl with its output going straight to the user.
func (c *Client) Passthrough(args ...string) error { return c.invoker().Passthrough(args...) }

// Output runs limactl and captures stdout. Any stderr becomes part of the
// error, so a caller that fails has something to print.
func (c *Client) Output(args ...string) (string, error) { return c.invoker().Output(args...) }

// Send runs limactl with stdin wired to r and captures nothing. Used for the
// auth token, which must reach the guest without ever appearing in an
// argument vector.
func (c *Client) Send(stdin io.Reader, args ...string) error { return c.invoker().Send(stdin, args...) }

// Stream runs limactl with stdout going to w, for output that is consumed as
// it arrives (following a log).
func (c *Client) Stream(w io.Writer, args ...string) error { return c.invoker().Stream(w, args...) }

// --- VM state ----------------------------------------------------------------

// StatusRunning is the only status ptrbox tests for by name.
const StatusRunning = backend.StatusRunning

// VM is one entry of `limactl list`.
type VM = backend.VM

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
