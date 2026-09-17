// Package limafake simulates limactl.
//
// It is what lets the whole ptrbox lifecycle - install, provision, verify,
// tear down - run on a machine with no Lima, no Squid, no Keychain and no Mac
// anywhere in sight. Those are the tests that stand in for "did provisioning
// work": they assert the order of operations, what got written where, and that
// credentials travel on stdin.
//
// What is here is limactl's half: the VM list, the lifecycle, and the
// spelling of `shell <vm> --`. What a command then does inside a VM - each
// one's filesystem, squid, the verification runs - is guestfake's, embedded,
// so that a fake of another backend simulates the same guests.
package limafake

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PtrMzk/ptrbox/internal/backend/guestfake"
	"github.com/PtrMzk/ptrbox/internal/lima"
)

// Fake is a Runner backed by canned state. The zero value is a machine with
// no VMs on it.
type Fake struct {
	// Guest is every VM's insides and the call log: Files, Transcripts, the
	// canned verification failures, Calls, Stdins and the assertions over
	// them are all its fields and methods, promoted.
	guestfake.Guest

	// VMs, in creation order - `limactl list` output is order-sensitive only
	// in that tests read it, but stable order makes failures readable.
	VMs []lima.VM

	// StartOutput is what `limactl start` writes to stderr, where lima's log
	// goes. Empty by default - most tests do not care - and set to a recorded
	// transcript by the ones that exercise the output translator.
	StartOutput []byte

	// RestartOutput, when set, is what a start of an EXISTING instance
	// (`start <name>`, no --name) writes instead - the real lima emits a
	// different stream there ("Using the existing instance", no image
	// lines), and `ptrbox new`'s reboot takes exactly that path. Unset, the
	// restart replays StartOutput like everything else.
	RestartOutput []byte

	// Canned failures.
	StartFails bool // `limactl start` fails
	ListFails  bool // `limactl list` fails, i.e. state is unknowable
}

// New returns a Fake with no VMs.
func New() *Fake { return &Fake{} }

// Available reports that limactl is usable, which for a fake it always is.
func (f *Fake) Available() bool { return true }

// Run dispatches one limactl invocation.
func (f *Fake) Run(c lima.Cmd) error {
	f.Record(c.Args)

	if len(c.Args) == 0 {
		return errors.New("limafake: no arguments")
	}
	switch c.Args[0] {
	case "list":
		return f.list(c)
	case "validate":
		return f.validate(c)
	case "start":
		return f.start(c)
	case "stop":
		return f.setStatus(arg(c.Args, 1), "Stopped")
	case "delete":
		return f.delete(c)
	case "shell":
		// `shell <vm> -- <argv...>`: the spelling is limactl's, the rest is
		// the guest's.
		return f.Exec(arg(c.Args, 1), c.Args[min(3, len(c.Args)):], c.Stdin, c.Stdout, c.Stderr)
	}
	return fmt.Errorf("limafake: unhandled command: %s", c.Args[0])
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func (f *Fake) list(c lima.Cmd) error {
	if f.ListFails {
		return errors.New("limafake: list failed")
	}
	for _, vm := range f.VMs {
		if arg(c.Args, 1) == "--format" {
			fmt.Fprintf(c.Stdout, "%s %s\n", vm.Name, vm.Status)
			continue
		}
		fmt.Fprintln(c.Stdout, vm.Name)
	}
	return nil
}

func (f *Fake) validate(c lima.Cmd) error {
	path := arg(c.Args, 1)
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(c.Stderr, "limactl: no such config: %s\n", path)
		return errors.New("validate failed")
	}
	return nil
}

func (f *Fake) start(c lima.Cmd) error {
	output := f.StartOutput
	if arg(c.Args, 1) != "--name" && len(f.RestartOutput) > 0 {
		output = f.RestartOutput
	}
	if len(output) > 0 && c.Stderr != nil {
		c.Stderr.Write(output)
	}
	if f.StartFails {
		fmt.Fprintln(c.Stderr, "limactl: FATA start failed")
		return errors.New("start failed")
	}
	if arg(c.Args, 1) != "--name" {
		return f.setStatus(arg(c.Args, 1), lima.StatusRunning)
	}

	// Creation: `start --name X -y --timeout 20m <config>`.
	name := arg(c.Args, 2)
	f.AddVM(name, lima.StatusRunning)

	// Lima writes a per-VM ssh config; ptrbox symlinks it.
	dir := filepath.Join(os.Getenv("HOME"), ".lima", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("Host lima-%s\n  Hostname 127.0.0.1\n", name)
	return os.WriteFile(filepath.Join(dir, "ssh.config"), []byte(body), 0o644)
}

func (f *Fake) delete(c lima.Cmd) error {
	name := arg(c.Args, 1)
	if name == "-f" {
		name = arg(c.Args, 2)
	}
	kept := f.VMs[:0]
	for _, vm := range f.VMs {
		if vm.Name != name {
			kept = append(kept, vm)
		}
	}
	f.VMs = kept
	return nil
}

// --- state helpers for tests -------------------------------------------------

// AddVM declares a VM without going through ptrbox.
func (f *Fake) AddVM(name, status string) {
	for i, vm := range f.VMs {
		if vm.Name == name {
			f.VMs[i].Status = status
			return
		}
	}
	f.VMs = append(f.VMs, lima.VM{Name: name, Status: status})
}

// SetStatus flips a VM's status without going through ptrbox, e.g. to
// simulate a proxy stopped out of band.
func (f *Fake) SetStatus(name, status string) { _ = f.setStatus(name, status) }

func (f *Fake) setStatus(name, status string) error {
	for i, vm := range f.VMs {
		if vm.Name == name {
			f.VMs[i].Status = status
			return nil
		}
	}
	return nil
}

// VMStatus is the status the fake holds for a VM ("" if it does not exist).
func (f *Fake) VMStatus(name string) string {
	for _, vm := range f.VMs {
		if vm.Name == name {
			return vm.Status
		}
	}
	return ""
}
