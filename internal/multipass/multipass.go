// Package multipass is the whole of ptrbox's contact with Multipass: the
// backend on a Windows PC, Hyper-V underneath.
//
// Every argv here is one the step-0 capture saw work on Windows 1.16.4
// (tests/windows-capture, 2026-09-19/20), and three of that capture's
// findings shape the package. `multipass exec` forwards no stdin on Windows,
// so a payload reaches a guest through `multipass transfer -` into a private
// file and an exec that redirects from it. `multipass launch` and `start`
// return before the guest is what the template says - launch waits for
// cloud-init, start does not - so both are followed by `cloud-init status
// --wait`. And a mount the daemon has on record can be absent from the guest
// (launch skips it with exit 0 when mounts are disabled; a host reboot brings
// the VM back without it), so /proc/mounts is read after every launch and
// start, and a running VM missing its mount is restarted once, which is what
// the capture showed re-establishes it.
//
// Two accounts, as backend.Facts.DaemonUser says: Multipass logs in as the
// image's default user and needs its sudo on every boot, so that account is
// the daemon's, and everything of the user's runs as the agent through it.
package multipass

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/backend"
	"github.com/PtrMzk/ptrbox/internal/config"
)

const (
	// Binary is the executable every call goes to.
	Binary = "multipass"

	// Switch is the Hyper-V Internal switch every ptrbox VM's second NIC is
	// on, created by the two PowerShell lines `ptrbox install` prints. Its
	// subnet is Network; the host holds HostAddr on it, the proxy ProxyAddr,
	// and sandboxes count up from firstSlot.
	Switch    = "ptrbox"
	Network   = "172.31.255.0/24"
	HostAddr  = "172.31.255.1"
	ProxyAddr = "172.31.255.2"
	firstSlot = 16

	// DaemonUser is the account Multipass logs into a guest as, and AgentUser
	// the one ptrbox adds for everything of the user's.
	DaemonUser = "ubuntu"
	AgentUser  = "agent"

	// launchTimeout bounds `multipass launch`, in seconds. Launch waits for
	// cloud-init, which here is the whole of boot-1 provisioning; the default
	// of 300 is what a first boot will exceed.
	launchTimeout = "1200"
)

// images maps PTRBOX_DISTRO to the alias `multipass launch` takes. Multipass
// on Hyper-V fetches images from Canonical's catalogue by alias; whether it
// accepts a URL to a foreign image is untested, so a distro absent here is
// refused rather than tried.
var images = map[string]string{"ubuntu2404": "24.04"}

// Exec is the real runner.
func Exec() backend.ExecRunner { return backend.ExecRunner{Binary: Binary} }

// Client is the typed interface to the multipass CLI.
type Client struct{ backend.Invoker }

// New wires a Client to a runner and the streams passthrough output goes to.
func New(r backend.Runner, stdout, stderr io.Writer) *Client {
	return &Client{backend.Invoker{Binary: Binary, Runner: r, Stdout: stdout, Stderr: stderr}}
}

// --- VM state ----------------------------------------------------------------

// listing is `multipass list --format json`, the fields ptrbox reads.
type listing struct {
	List []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	} `json:"list"`
}

// List returns every VM multipass knows about. A listing that fails returns
// no VMs and no error: callers use this to decide whether to leave the proxy
// running, and "I could not tell" must land on the same side as "yes,
// something is running".
func (c *Client) List() []backend.VM {
	out, err := c.Output("list", "--format", "json")
	if err != nil {
		return nil
	}
	var l listing
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		return nil
	}
	vms := make([]backend.VM, 0, len(l.List))
	for _, vm := range l.List {
		vms = append(vms, backend.VM{Name: vm.Name, Status: vm.State})
	}
	return vms
}

// Names returns just the VM names.
func (c *Client) Names() []string {
	var names []string
	for _, vm := range c.List() {
		names = append(names, vm.Name)
	}
	return names
}

// Status is the VM's status, or "" if it does not exist.
func (c *Client) Status(vm string) string {
	for _, v := range c.List() {
		if v.Name == vm {
			return v.Status
		}
	}
	return ""
}

// Exists reports whether multipass knows this VM.
func (c *Client) Exists(vm string) bool { return c.Status(vm) != "" }

// Running reports whether the VM exists and is up.
func (c *Client) Running(vm string) bool { return c.Status(vm) == backend.StatusRunning }

// details is `multipass info <vm> --format json`, the fields ptrbox reads.
type details struct {
	Info map[string]struct {
		Mounts map[string]json.RawMessage `json:"mounts"`
	} `json:"info"`
}

// Mounts is the guest paths the daemon has on record for a VM - what it
// intends, which the capture showed is not always what the guest has.
func (c *Client) Mounts(vm string) ([]string, error) {
	out, err := c.Output("info", vm, "--format", "json")
	if err != nil {
		return nil, err
	}
	var d details
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		return nil, fmt.Errorf("multipass info %s: %w", vm, err)
	}
	var guests []string
	for guest := range d.Info[vm].Mounts {
		guests = append(guests, guest)
	}
	sort.Strings(guests)
	return guests, nil
}

// --- lifecycle ---------------------------------------------------------------

// Validate checks a rendered config before any VM state is touched. Multipass
// has no validate verb; what it hands cloud-init has to begin with the one
// line cloud-init keys on, and everything past that is the template's job.
func (c *Client) Validate(configPath string) error {
	body, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	if first, _, _ := strings.Cut(string(body), "\n"); first != "#cloud-config" {
		return fmt.Errorf("%s is not cloud-init user-data: the first line is %q, want #cloud-config", configPath, first)
	}
	return nil
}

// Backend is the backend.Backend over a Client: the listing and Validate are
// the Client's, and everything that knows about accounts, the switch and the
// mount is here.
type Backend struct{ *Client }

var _ backend.Backend = Backend{}

func (Backend) Facts() backend.Facts {
	return backend.Facts{
		Name:      "multipass",
		ProxyAddr: ProxyAddr,
		// The proxy VM has an address of its own on the switch and the host
		// dials it there; nothing is published on the host's loopback.
		ProxyReach: backend.DirectAddress,
		// Sandboxes arrive across the switch; the proxy's own loopback is
		// for the in-VM verification.
		ProxyClientSrc: []string{"127.0.0.1", Network},
		HostAddr:       HostAddr,
		// LM Studio on the PC would be reached at HostAddr, but nothing has
		// tried it and the Windows firewall's Public profile on the switch
		// adapter is what sits in the way. Refused at plan time until then.
		HostServices:    false,
		GuestAddr:       guestAddr,
		SandboxTemplate: "vm/claude-repo.cloud-init.yaml",
		ProxyTemplate:   "vm/proxy.cloud-init.yaml",
		DaemonUser:      DaemonUser,
		// `multipass shell <vm>` would land as the daemon user; the only way
		// in as the agent is ptrbox's own.
		HasSSHConfigLink: false,
		// Output comes back whole, through a file: nothing can be followed
		// as it arrives (see capture).
		StreamsLive: false,
		ShellAdvice: func(vm string) string { return "ptrbox shell " + vm },
		ListHint:    Binary + " list",
		ExecAdvice: func(vm string, argv ...string) string {
			return Binary + " exec " + vm + " -- " + strings.Join(argv, " ")
		},
		DeleteAdvice: func(vm string) string { return Binary + " delete --purge " + vm },
		// The winget id, which is what HostOS prints for a missing dependency.
		Deps: []backend.Dep{{Tool: Binary, Package: "Canonical.Multipass"}},
	}
}

// InterfaceAddrs is how Preflight sees the host's own addresses - a variable
// so the suite can describe a PC with or without the switch adapter.
var InterfaceAddrs = net.InterfaceAddrs

// Preflight is what the PC must have before a VM exists, and what only this
// backend knows to ask for. Three things, each checked and named with the
// command that fixes it, because ptrbox never elevates and never changes a
// host setting on its own.
//
// The switch: `multipass networks` must list it, and an adapter on the host
// must hold HostAddr on it - both are what the two PowerShell lines create,
// and a switch without the address is one the proxy VM cannot be reached
// across. Mounts: `local.privileged-mounts` must be true, or `launch` skips
// the repo mount with exit 0 (step 0, stage 2); turning it on RESTARTS the
// daemon, saving and resuming every VM, which is why it is asked here, before
// any VM exists, and never done by ptrbox while one does.
func (b Backend) Preflight() error {
	var problems []string

	out, err := b.Client.Output("networks", "--format", "json")
	if err != nil {
		return err
	}
	var nets struct {
		List []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"list"`
	}
	if err := json.Unmarshal([]byte(out), &nets); err != nil {
		return fmt.Errorf("multipass networks: %w", err)
	}
	hasSwitch := false
	for _, n := range nets.List {
		if n.Name == Switch && n.Type == "switch" {
			hasSwitch = true
		}
	}
	hasAddr := false
	if addrs, err := InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.String() == HostAddr {
				hasAddr = true
			}
		}
	}
	if !hasSwitch || !hasAddr {
		what := "the ptrbox switch does not exist"
		if hasSwitch {
			what = "the ptrbox switch exists but this PC holds no address on it"
		}
		problems = append(problems, what+". Once, in an admin PowerShell:\n"+
			"    New-VMSwitch -Name "+Switch+" -SwitchType Internal\n"+
			`    New-NetIPAddress -InterfaceAlias "vEthernet (`+Switch+`)" -IPAddress `+HostAddr+" -PrefixLength 24")
	}

	setting, err := b.Client.Output("get", "local.privileged-mounts")
	if err != nil {
		return err
	}
	if strings.TrimSpace(setting) != "true" {
		problems = append(problems, "mounts are disabled in multipass, so a sandbox would come up without its repo. Once, with no ptrbox VM running (it restarts the multipass daemon):\n"+
			"    multipass set local.privileged-mounts=true")
	}

	if len(problems) == 0 {
		return nil
	}
	return errors.New("this PC is not ready for ptrbox: " + strings.Join(problems, "\nand "))
}

// guestAddr is a sandbox's address on the switch, from its proxy port: the
// first slot's port maps to .16, and the sixteen slots fill .16 to .31. The
// host is .1 and the proxy .2, so no port can collide with either.
func guestAddr(proxyPort int) string {
	return fmt.Sprintf("172.31.255.%d", firstSlot+proxyPort-config.SandboxPortMin())
}

// Create is `multipass launch` with the Spec as arguments - the sizing, the
// image alias, the switch NIC and the one mount - and the rendered cloud-init
// as what the guest contains. Launch returns when cloud-init's first boot is
// done; the two checks after it are for what launch does not report: a
// cloud-init that finished with errors, and a mount it skipped.
func (b Backend) Create(spec backend.Spec) error {
	image, ok := images[spec.Distro]
	if !ok {
		return fmt.Errorf("PTRBOX_DISTRO %q has no Multipass image; this backend supports: %s",
			spec.Distro, strings.Join(distros(), " "))
	}
	args := []string{"launch", "--name", spec.Name,
		"--cpus", strconv.Itoa(spec.CPUs), "--memory", size(spec.Memory), "--disk", size(spec.Disk),
		"--cloud-init", spec.ConfigPath,
		"--network", "name=" + Switch + ",mode=manual",
		"--timeout", launchTimeout}
	if spec.Mount != nil {
		args = append(args, "--mount", spec.Mount.Host+":"+spec.Mount.Guest)
	}
	args = append(args, image)
	if err := b.Client.Passthrough(args...); err != nil {
		return err
	}
	if err := b.waitCloudInit(spec.Name); err != nil {
		return err
	}
	if spec.Mount != nil {
		// Launch skips a mount WITH EXIT 0 when mounts are disabled on the
		// host (step 0, stage 2). The guest is the authority.
		return b.requireMount(spec.Name, spec.Mount.Guest)
	}
	return nil
}

func distros() []string {
	names := make([]string, 0, len(images))
	for name := range images {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// size spells a config size the way multipass takes it: 8GiB -> 8G, 512MiB
// -> 512M. The config's spelling is lima's, which is the one in the docs.
func size(s string) string {
	return strings.TrimSuffix(strings.TrimSuffix(s, "iB"), "B")
}

// Start boots an existing VM and returns when the guest is what the template
// says: `multipass start` returns when ssh is up, while the per-boot scripts
// - the firewall among them - run inside cloud-init after that. Then the
// mounts the daemon has on record are checked against the guest, because a
// host reboot brings a VM back without them (step 0, stage 9); one restart
// re-establishes them, and a mount still missing after it is a failed start.
func (b Backend) Start(vm string) error {
	if err := b.Client.Passthrough("start", vm); err != nil {
		return err
	}
	if err := b.waitCloudInit(vm); err != nil {
		return err
	}
	return b.Ready(vm)
}

// Stop powers a VM off, keeping its disk and state.
func (b Backend) Stop(vm string) error { return b.Client.Passthrough("stop", vm) }

// Delete destroys a VM and its disk. Without --purge multipass keeps a
// deleted VM around to recover, and its name stays taken.
func (b Backend) Delete(vm string) error { return b.Client.Passthrough("delete", "--purge", vm) }

// waitCloudInit blocks until cloud-init has run everything for this boot,
// and fails if a stage failed: a per-boot script that died is a guest that is
// not what the template says, and the exit status is the one place that says
// so. `status --wait` exits 0 for done, 1 for error, and 2 for done with
// recoverable errors - warnings cloud-init logged about the user-data, a
// schema complaint or a deprecation, which the capture saw beside a printed
// `status: done`. Those are about the document's spelling, not the guest's
// state, and the guest's state is vm/verify.sh's question; so 2 is done, and
// what cloud-init complained about is shown rather than swallowed.
func (b Backend) waitCloudInit(vm string) error {
	out, err := b.Client.Output(execArgs(vm, backend.Login, "cloud-init", "status", "--wait")...)
	if err == nil {
		return nil
	}
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		long, _ := b.Client.Output(execArgs(vm, backend.Login, "cloud-init", "status", "--long")...)
		fmt.Fprintf(b.Client.Stdout, "cloud-init in %s finished with warnings:\n%s\n", vm, strings.TrimSpace(long))
		return nil
	}
	return fmt.Errorf("cloud-init did not finish cleanly in %s: %w\n%s", vm, err, strings.TrimSpace(out))
}

// Ready compares the daemon's mount record with the guest's /proc/mounts and
// restarts the VM once if they disagree. It is the second half of Start, and
// on its own it is what `ptrbox start` asks of a VM the daemon already
// brought back after a host reboot - Running, with the mount on record and
// absent from the guest (step 0, stage 9).
func (b Backend) Ready(vm string) error {
	guests, err := b.Mounts(vm)
	if err != nil {
		return err
	}
	var missing []string
	for _, guest := range guests {
		if !b.mounted(vm, guest) {
			missing = append(missing, guest)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := b.Client.Passthrough("restart", vm); err != nil {
		return err
	}
	if err := b.waitCloudInit(vm); err != nil {
		return err
	}
	for _, guest := range missing {
		if err := b.requireMount(vm, guest); err != nil {
			return err
		}
	}
	return nil
}

// mounted reads the guest's own view. An empty directory at the mount point
// is what an unmounted mount looks like, so `ls` succeeding proves nothing;
// a /proc/mounts line is the test - asked as an exit status, because
// /proc/mounts itself can be longer than a direct exec can carry (see
// capture).
func (b Backend) mounted(vm, guest string) bool {
	_, err := b.Client.Output(execArgs(vm, backend.Login, "grep", "-qsF", " "+guest+" ", "/proc/mounts")...)
	return err == nil
}

func (b Backend) requireMount(vm, guest string) error {
	if b.mounted(vm, guest) {
		return nil
	}
	return fmt.Errorf("%s has no mount at %s: multipass has it on record but the guest shows nothing there "+
		"(mounts disabled on this host? `multipass set local.privileged-mounts=true`, then `ptrbox start %s`)",
		vm, guest, vm)
}

// --- in-guest execution ------------------------------------------------------

// execArgs builds the argv for running a command inside a VM as the given
// account. The exec arrives as the daemon user; the agent is reached through
// that account's sudo, with -H so $HOME is the agent's. Never mapping the
// working directory, because multipass otherwise tries the host's current
// directory inside the guest.
//
// A direct exec is for commands whose output is known to be small. On
// Windows 1.16.4 the client STALLS, never returning, once a command has
// written more than 4096 bytes - one pipe buffer - to a stdout that is not
// a console, a pipe or a file alike, and ptrbox's is always a pipe (first
// real run, 2026-09-20: the fourth `sudo cat` of the install, the first file
// over that size, hung for good; `head -c 4096` returns, `head -c 4097` does
// not). So this package runs a command directly only when it knows the
// answer fits - an exit status, `cloud-init status`, a mkdir - and
// everything a caller asks for goes through capture, which never lets
// output cross the exec channel at all.
func execArgs(vm string, user backend.User, argv ...string) []string {
	args := []string{"exec", vm, "--no-map-working-directory", "--"}
	if user == backend.Agent {
		args = append(args, "sudo", "-n", "-u", AgentUser, "-H")
	}
	return append(args, argv...)
}

// asUser is the shell fragment that runs "$@" as the given account, from a
// shell that is the daemon user's.
func asUser(user backend.User) string {
	if user == backend.Agent {
		return `sudo -n -u ` + AgentUser + ` -H "$@"`
	}
	return `"$@"`
}

// ScratchName names a fresh file in the daemon user's private directory. A
// variable so tests can know the name in advance.
var ScratchName = func() string {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(suffix[:])
}

// scratch makes the daemon user's private directory and returns a fresh path
// in it: 0700, so only that account and root can read what lands there.
func (b Backend) scratch(vm, kind string) (string, error) {
	dir := "/home/" + DaemonUser + "/.ptrbox"
	if _, err := b.Client.Output(execArgs(vm, backend.Login, "mkdir", "-p", "-m", "0700", dir)...); err != nil {
		return "", err
	}
	return dir + "/" + kind + "-" + ScratchName(), nil
}

// capture runs argv in the guest as the given account and returns what it
// wrote, without any of it crossing the exec channel: the command's stdout
// and stderr go to two files the daemon user owns, the exec carries only the
// exit status, and the files come back over `multipass transfer` - SFTP, a
// different path that carries any size - and are removed. Three or four
// multipass calls instead of one; the one would hang on the first file
// bigger than a pipe buffer.
//
// The redirection is done by the daemon user's shell, so the file is that
// account's whatever the command ran as - the agent's output lands in a
// directory the agent cannot open, and the agent's stderr with it.
func (b Backend) capture(vm string, user backend.User, argv ...string) (stdout, stderr string, err error) {
	base, err := b.scratch(vm, "out")
	if err != nil {
		return "", "", err
	}
	script := `f=$1; shift; ` + asUser(user) + ` >"$f" 2>"$f.err"`
	args := append([]string{"sh", "-c", script, "sh", base}, argv...)
	_, runErr := b.Client.Output(execArgs(vm, backend.Login, args...)...)

	stdout, err = b.Client.Output("transfer", vm+":"+base, "-")
	if err != nil {
		return "", "", err
	}
	if runErr != nil {
		stderr, _ = b.Client.Output("transfer", vm+":"+base+".err", "-")
	}
	if _, err := b.Client.Output(execArgs(vm, backend.Login, "rm", "-f", base, base+".err")...); err != nil {
		return "", "", err
	}
	if runErr != nil {
		// The guest command's own failure, with its stderr, spelled as the
		// invocation a person could retype - not the wrapper.
		cause := runErr
		if u := errors.Unwrap(runErr); u != nil {
			cause = u
		}
		return stdout, stderr, &backend.Error{Binary: Binary, Args: execArgs(vm, user, argv...), Stderr: stderr, Err: cause}
	}
	return stdout, "", nil
}

// Output captures stdout; any stderr becomes part of the error.
func (b Backend) Output(vm string, user backend.User, argv ...string) (string, error) {
	out, _, err := b.capture(vm, user, argv...)
	return out, err
}

// Stream writes the command's output to w - once it has all arrived, since
// nothing here streams. A command that never ends (`tail -f`) never returns;
// Facts.StreamsLive says so, and the caller refuses it.
func (b Backend) Stream(vm string, user backend.User, w io.Writer, argv ...string) error {
	out, _, err := b.capture(vm, user, argv...)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out)
	return err
}

// Passthrough shows the command's output to the user, narrated as one
// invocation, once the command is done.
func (b Backend) Passthrough(vm string, user backend.User, argv ...string) error {
	args := execArgs(vm, user, argv...)
	n, narrated := b.Client.Stdout.(backend.Narrator)
	if narrated {
		n.Begin(args)
	}
	out, stderr, err := b.capture(vm, user, argv...)
	io.WriteString(b.Client.Stdout, out)
	if stderr != "" && b.Client.Stderr != nil {
		io.WriteString(b.Client.Stderr, stderr)
	}
	if narrated {
		n.End(err)
	}
	return err
}

// Send gets a payload into a guest command's stdin on a backend whose exec
// forwards no stdin at all (step 0, stage 5). `multipass transfer -` reads
// the client's stdin and writes it over SFTP into a file the daemon user
// owns, in a directory only that user can read; then one exec, as that user,
// runs the command with its stdin redirected from the file and removes it,
// whatever the command's exit. For the agent the command is wrapped in the
// daemon user's sudo, which inherits the open descriptor - so the agent
// reads a file it could not open. The payload is never on an argv; the file
// is the price of this backend, and it is gone before Send returns.
//
// The command's stdout goes to /dev/null and its stderr to a file, for the
// same reason capture exists: Send captures nothing, and `sudo tee` - the
// proxy's config push - echoes every byte it writes back to stdout, which
// on the first real run was the second way a squid.conf stalled the exec
// channel. Nothing a guest command prints may cross it.
func (b Backend) Send(vm string, user backend.User, stdin io.Reader, argv ...string) error {
	file, err := b.scratch(vm, "stdin")
	if err != nil {
		return err
	}
	if err := b.Client.Send(stdin, "transfer", "-", vm+":"+file); err != nil {
		return err
	}
	script := `f=$1; shift; ` + asUser(user) + ` <"$f" >/dev/null 2>"$f.err"; s=$?; rm -f "$f"; exit $s`
	args := append([]string{"sh", "-c", script, "sh", file}, argv...)
	_, runErr := b.Client.Output(execArgs(vm, backend.Login, args...)...)
	var stderr string
	if runErr != nil {
		stderr, _ = b.Client.Output("transfer", vm+":"+file+".err", "-")
	}
	if _, err := b.Client.Output(execArgs(vm, backend.Login, "rm", "-f", file+".err")...); err != nil {
		return err
	}
	if runErr != nil {
		cause := runErr
		if u := errors.Unwrap(runErr); u != nil {
			cause = u
		}
		return &backend.Error{Binary: Binary, Args: execArgs(vm, user, argv...), Stderr: stderr, Err: cause}
	}
	return nil
}

// Shell is an interactive login shell as the agent, in /workspace, on the
// caller's streams: straight to the Runner and past the narrated Stdout. -d
// sets the working directory the exec starts in, which sudo keeps; -H and -l
// give bash the agent's home and profile. With a terminal on the client's
// stdout multipass allocates a pty, which is what makes it a session.
func (b Backend) Shell(vm string, stdin io.Reader, stdout, stderr io.Writer) error {
	return b.Client.Runner.Run(backend.Cmd{
		Args:   []string{"exec", vm, "-d", "/workspace", "--", "sudo", "-n", "-u", AgentUser, "-H", "bash", "-l"},
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
}
