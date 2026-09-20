// Package multipassfake simulates the multipass CLI on a Windows PC.
//
// It is limafake's counterpart: what is here is multipass's half - the VM
// list, the lifecycle, the mount record, the JSON it prints, the spelling of
// `exec <vm> -- ` - and what a command then does inside a VM is guestfake's,
// embedded, so that both backends' fakes simulate the same guests. Every
// shape it answers is one the step-0 capture saw (internal/multipass/testdata
// and tests/windows-capture), and the machine it simulates has the capture's
// two habits: a mount can be on record and absent from the guest, and
// `launch` skips a mount with exit 0 when mounts are disabled on the host.
package multipassfake

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/backend"
	"github.com/PtrMzk/ptrbox/internal/backend/guestfake"
	"github.com/PtrMzk/ptrbox/internal/multipass"
)

// Fake is a Runner backed by canned state. The zero value is a PC with the
// ptrbox switch, mounts enabled, and no VMs.
type Fake struct {
	// Guest is every VM's insides and the call log, promoted.
	guestfake.Guest

	// VMs, in creation order.
	VMs []backend.VM
	// Mounts is the daemon's record: Mounts[vm][guest path] = host path.
	Mounts map[string]map[string]string
	// Unmounted marks VMs whose recorded mounts the guest does not have -
	// what a host reboot leaves behind. `restart` clears it, as it did on
	// the PC.
	Unmounted map[string]bool

	// The host.
	MountsDisabled bool // local.privileged-mounts is false: launch skips the mount, exit 0
	NetworkMissing bool // `networks` lists no ptrbox switch

	// Canned failures and outputs.
	LaunchFails   bool
	ListFails     bool
	CloudInitExit int    // what `cloud-init status --wait` exits with: 0 done, 1 error, 2 done with warnings
	LaunchOutput  []byte // what launch writes to stdout, in place of its two lines
}

// New returns a Fake with no VMs.
func New() *Fake {
	return &Fake{Mounts: map[string]map[string]string{}, Unmounted: map[string]bool{}}
}

// Available reports that multipass is usable, which for a fake it always is.
func (f *Fake) Available() bool { return true }

// Run dispatches one multipass invocation.
func (f *Fake) Run(c backend.Cmd) error {
	f.Record(c.Args)
	if len(c.Args) == 0 {
		return errors.New("multipassfake: no arguments")
	}
	switch c.Args[0] {
	case "version":
		fmt.Fprint(c.Stdout, "multipass   1.16.4+win\nmultipassd  1.16.4+win\n")
		return nil
	case "networks":
		return f.networks(c)
	case "get":
		if arg(c.Args, 1) == "local.privileged-mounts" {
			fmt.Fprintln(c.Stdout, !f.MountsDisabled)
			return nil
		}
	case "list":
		return f.list(c)
	case "info":
		return f.info(c)
	case "launch":
		return f.launch(c)
	case "start":
		return f.setStatus(arg(c.Args, 1), backend.StatusRunning)
	case "stop":
		return f.setStatus(arg(c.Args, 1), "Stopped")
	case "restart":
		delete(f.Unmounted, arg(c.Args, 1))
		return f.setStatus(arg(c.Args, 1), backend.StatusRunning)
	case "delete":
		return f.delete(c)
	case "exec":
		return f.exec(c)
	case "transfer":
		return f.transfer(c)
	}
	return fmt.Errorf("multipassfake: unhandled command: %s", strings.Join(c.Args, " "))
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// --- what multipass prints ---------------------------------------------------

type network struct {
	Description string `json:"description"`
	Name        string `json:"name"`
	Type        string `json:"type"`
}

func (f *Fake) networks(c backend.Cmd) error {
	list := []network{
		{"Virtual Switch with internal networking", "Default Switch", "switch"},
		{"Ethernet adapter (wired)", "Ethernet", "ethernet"},
	}
	if !f.NetworkMissing {
		list = append(list, network{"Virtual Switch with internal networking", multipass.Switch, "switch"})
	}
	return writeJSON(c.Stdout, map[string]any{"list": list})
}

type listed struct {
	IPv4    []string `json:"ipv4"`
	Name    string   `json:"name"`
	Release string   `json:"release"`
	State   string   `json:"state"`
}

func (f *Fake) list(c backend.Cmd) error {
	if f.ListFails {
		fmt.Fprintln(c.Stderr, "list failed: cannot connect to the multipass socket")
		return errors.New("multipassfake: list failed")
	}
	list := make([]listed, 0, len(f.VMs))
	for _, vm := range f.VMs {
		list = append(list, listed{IPv4: f.addresses(vm), Name: vm.Name, Release: "Ubuntu 24.04 LTS", State: vm.Status})
	}
	return writeJSON(c.Stdout, map[string]any{"list": list})
}

// addresses is what `list` shows: nothing for a stopped VM, and for a running
// one the default switch's lease and, when the VM has a switch NIC, its
// address there. The fake does not know a VM's netplan, so the second is a
// placeholder in the right subnet.
func (f *Fake) addresses(vm backend.VM) []string {
	if vm.Status != backend.StatusRunning {
		return []string{}
	}
	return []string{"172.26.0.100", "172.31.255.17"}
}

type mountRecord struct {
	GIDMappings []string `json:"gid_mappings"`
	SourcePath  string   `json:"source_path"`
	UIDMappings []string `json:"uid_mappings"`
}

func (f *Fake) info(c backend.Cmd) error {
	name := arg(c.Args, 1)
	vm, ok := f.vm(name)
	if !ok {
		fmt.Fprintf(c.Stderr, "info failed: instance \"%s\" does not exist\n", name)
		return guestfake.ExitError(2)
	}
	mounts := map[string]mountRecord{}
	for guest, host := range f.Mounts[name] {
		mounts[guest] = mountRecord{GIDMappings: []string{"-2:default"}, SourcePath: host, UIDMappings: []string{"-2:default"}}
	}
	return writeJSON(c.Stdout, map[string]any{
		"errors": []string{},
		"info": map[string]any{
			name: map[string]any{
				"cpu_count":      "2",
				"image_release":  "24.04 LTS",
				"ipv4":           f.addresses(vm),
				"mounts":         mounts,
				"release":        "Ubuntu 24.04.5 LTS",
				"snapshot_count": "0",
				"state":          vm.Status,
			},
		},
	})
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "    ")
	return enc.Encode(v)
}

// --- lifecycle ---------------------------------------------------------------

// launch is `launch --name X --cpus N --memory M --disk D --cloud-init PATH
// --network name=ptrbox,mode=manual --timeout T [--mount HOST:GUEST] ALIAS`.
func (f *Fake) launch(c backend.Cmd) error {
	var name, cloudInit, mount string
	for i := 1; i < len(c.Args); i++ {
		switch c.Args[i] {
		case "--name":
			i++
			name = arg(c.Args, i)
		case "--cloud-init":
			i++
			cloudInit = arg(c.Args, i)
		case "--mount":
			i++
			mount = arg(c.Args, i)
		case "--cpus", "--memory", "--disk", "--network", "--timeout":
			i++
		}
	}
	if name == "" {
		return errors.New("multipassfake: launch without --name")
	}
	if cloudInit != "" {
		body, err := os.ReadFile(cloudInit)
		if err != nil {
			fmt.Fprintf(c.Stderr, "launch failed: cannot read %s\n", cloudInit)
			return errors.New("multipassfake: launch failed")
		}
		if first, _, _ := strings.Cut(string(body), "\n"); first != "#cloud-config" {
			fmt.Fprintln(c.Stderr, "launch failed: cloud-init user-data must begin with #cloud-config")
			return errors.New("multipassfake: launch failed")
		}
	}
	if f.LaunchFails {
		fmt.Fprintln(c.Stderr, "launch failed: The following errors occurred:\nfake launch failure")
		return errors.New("multipassfake: launch failed")
	}
	f.AddVM(name, backend.StatusRunning)

	out := &strings.Builder{}
	fmt.Fprintf(out, "Launched: %s\n", name)
	if mount != "" {
		// The host path may carry a drive letter, so the guest path is what
		// follows the LAST colon.
		i := strings.LastIndex(mount, ":")
		host, guest := mount[:i], mount[i+1:]
		if f.MountsDisabled {
			fmt.Fprintln(out, "Skipping mount due to disabled mounts feature")
		} else {
			f.Mounts[name] = map[string]string{guest: host}
			fmt.Fprintf(out, "Mounted '%s' into '%s:%s'\n", host, name, guest)
		}
	}
	if c.Stdout != nil {
		if len(f.LaunchOutput) > 0 {
			c.Stdout.Write(f.LaunchOutput)
		} else {
			io.WriteString(c.Stdout, out.String())
		}
	}
	return nil
}

func (f *Fake) delete(c backend.Cmd) error {
	name := arg(c.Args, 1)
	if name == "--purge" {
		name = arg(c.Args, 2)
	}
	kept := f.VMs[:0]
	for _, vm := range f.VMs {
		if vm.Name != name {
			kept = append(kept, vm)
		}
	}
	f.VMs = kept
	delete(f.Mounts, name)
	delete(f.Unmounted, name)
	return nil
}

// --- in-guest execution ------------------------------------------------------

// exec is `exec <vm> [--no-map-working-directory | -d <dir>] -- <argv>`. The
// agent's commands arrive wrapped in the daemon user's sudo; the fake strips
// the wrapper and hands the guest the command. Four shapes are multipass's
// own or this backend's and are answered here; everything else is the
// guest's.
func (f *Fake) exec(c backend.Cmd) error {
	name := arg(c.Args, 1)
	sep := -1
	workdir := "/home/" + multipass.DaemonUser
	for i := 2; i < len(c.Args); i++ {
		if c.Args[i] == "--" {
			sep = i
			break
		}
		if c.Args[i] == "-d" {
			i++
			workdir = arg(c.Args, i)
		}
	}
	if sep < 0 {
		return errors.New("multipassfake: exec without --")
	}
	vm, ok := f.vm(name)
	if !ok || vm.Status != backend.StatusRunning {
		fmt.Fprintf(c.Stderr, "exec failed: instance \"%s\" is not running\n", name)
		return guestfake.ExitError(1)
	}
	argv := c.Args[sep+1:]
	if len(argv) >= 5 && strings.Join(argv[:5], " ") == "sudo -n -u "+multipass.AgentUser+" -H" {
		argv = argv[5:]
	}
	joined := strings.Join(argv, " ")
	switch {
	case joined == "cloud-init status --wait":
		switch f.CloudInitExit {
		case 0:
			fmt.Fprintln(c.Stdout, "status: done")
			return nil
		case 2:
			fmt.Fprintln(c.Stdout, "status: done")
			return guestfake.ExitError(2)
		default:
			fmt.Fprintln(c.Stdout, "status: error")
			return guestfake.ExitError(f.CloudInitExit)
		}
	case joined == "cloud-init status --long":
		fmt.Fprint(c.Stdout, "status: done\nextended_status: degraded done\nrecoverable_errors:\nWARNING:\n  - cloud-config failed schema validation!\n")
		return nil
	case len(argv) == 4 && argv[0] == "grep" && argv[1] == "-qsF" && argv[3] == "/proc/mounts":
		// The mount check: an exit status, no output.
		if !f.Unmounted[name] {
			for _, guest := range f.guests(name) {
				if argv[2] == " "+guest+" " {
					return nil
				}
			}
		}
		return guestfake.ExitError(1)
	case len(argv) > 0 && argv[0] == "mkdir":
		return nil
	case len(argv) > 0 && argv[0] == "rm":
		for _, path := range argv[1:] {
			if path != "-f" {
				delete(f.Files[name], path)
			}
		}
		return nil
	case len(argv) >= 5 && argv[0] == "sh" && argv[1] == "-c" && argv[3] == "sh" && strings.Contains(argv[2], `<"$f"`):
		// The Send redirect: `sh -c <script> sh <file> <argv...>`. The file
		// arrived by transfer; it is the command's stdin and it goes away.
		file := argv[4]
		payload, ok := f.Files[name][file]
		if !ok {
			fmt.Fprintf(c.Stderr, "sh: 1: cannot open %s: No such file\n", file)
			return guestfake.ExitError(2)
		}
		delete(f.Files[name], file)
		// The command's stdout is discarded in the guest and its stderr
		// filed, as the script says; the exec carries the exit status.
		var stderr strings.Builder
		err := f.Exec(name, argv[5:], strings.NewReader(payload), io.Discard, &stderr)
		f.WriteFile(name, file+".err", stderr.String())
		return err
	case len(argv) >= 5 && argv[0] == "sh" && argv[1] == "-c" && argv[3] == "sh" && strings.Contains(argv[2], `>"$f"`):
		// The capture redirect: the command's stdout and stderr land in two
		// files for transfer to bring back; the exec carries the exit status
		// and nothing else - which is the whole point, since a real exec
		// stalls past 4096 bytes of output.
		file := argv[4]
		var stdout, stderr strings.Builder
		err := f.Exec(name, argv[5:], c.Stdin, &stdout, &stderr)
		f.WriteFile(name, file, stdout.String())
		f.WriteFile(name, file+".err", stderr.String())
		return err
	case joined == "bash -l":
		return f.Interactive(name, workdir, c.Stdin, c.Stdout)
	}
	return f.Exec(name, argv, c.Stdin, c.Stdout, c.Stderr)
}

// transfer is `transfer - <vm>:<path>` (the client's stdin into a guest
// file) or `transfer <vm>:<path> -` (a guest file onto the client's stdout).
// SFTP, not the exec channel: any size comes through.
func (f *Fake) transfer(c backend.Cmd) error {
	switch {
	case arg(c.Args, 1) == "-":
		name, path, ok := strings.Cut(arg(c.Args, 2), ":")
		if !ok || c.Stdin == nil {
			return errors.New("multipassfake: transfer needs <vm>:<path> and stdin")
		}
		body, err := io.ReadAll(c.Stdin)
		if err != nil {
			return err
		}
		f.WriteFile(name, path, string(body))
		return nil
	case arg(c.Args, 2) == "-":
		name, path, ok := strings.Cut(arg(c.Args, 1), ":")
		if !ok {
			return errors.New("multipassfake: transfer needs <vm>:<path>")
		}
		body, exists := f.ReadFile(name, path)
		if !exists {
			fmt.Fprintf(c.Stderr, "transfer failed: [sftp] cannot open remote file %s: No such file\n", path)
			return guestfake.ExitError(1)
		}
		io.WriteString(c.Stdout, body)
		return nil
	}
	return fmt.Errorf("multipassfake: only `transfer -` shapes are simulated, got %v", c.Args)
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
	f.VMs = append(f.VMs, backend.VM{Name: name, Status: status})
}

// SetStatus flips a VM's status without going through ptrbox.
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
	vm, _ := f.vm(name)
	return vm.Status
}

func (f *Fake) vm(name string) (backend.VM, bool) {
	for _, vm := range f.VMs {
		if vm.Name == name {
			return vm, true
		}
	}
	return backend.VM{}, false
}

func (f *Fake) guests(name string) []string {
	var guests []string
	for guest := range f.Mounts[name] {
		guests = append(guests, guest)
	}
	sort.Strings(guests)
	return guests
}
