// Package limafake simulates limactl, including each VM's filesystem.
//
// It is what lets the whole ptrbox lifecycle - install, provision, verify,
// tear down - run on a machine with no Lima, no Squid, no Keychain and no Mac
// anywhere in sight. Those are the tests that stand in for "did provisioning
// work": they assert the order of operations, what got written where, and that
// credentials travel on stdin.
//
// The in-VM file operations (tee/cat/mv/rm/tail) act on a per-VM map, which is
// how the proxy VM's squid config, allowlist and access log are simulated
// well enough for the sync logic to be exercised end to end.
package limafake

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
)

// Fake is a Runner backed by canned state. The zero value is a machine with
// no VMs on it.
type Fake struct {
	// VMs, in creation order - `limactl list` output is order-sensitive only
	// in that tests read it, but stable order makes failures readable.
	VMs []lima.VM

	// Files is each VM's filesystem: Files[vm][path] = content.
	Files map[string]map[string]string

	// Calls is one line per invocation, in order, with long or multi-line
	// arguments replaced by a <script:N> placeholder so the log stays
	// greppable. Scripts holds what those placeholders stand for.
	Calls   []string
	Scripts []string
	// Stdins is everything the fake was sent on stdin, in order.
	Stdins []string

	// Transcripts is what a Claude transcript pull returns for a VM. Absent
	// means the VM has none, which is the state of one nobody has worked in.
	Transcripts map[string][]byte

	// Canned failures.
	VerifyFails      bool // `bash -lc <verify.sh>` reports a failed sandbox
	ProxyVerifyFails bool // `bash -lc <verify-proxy.sh>` reports dead egress
	SquidParseFails  bool // in-VM `squid -k parse` rejects the config
	StartFails       bool // `limactl start` fails
	ListFails        bool // `limactl list` fails, i.e. state is unknowable
}

// New returns a Fake with no VMs.
func New() *Fake { return &Fake{Files: map[string]map[string]string{}} }

// Available reports that limactl is usable, which for a fake it always is.
func (f *Fake) Available() bool { return true }

// Run dispatches one limactl invocation.
func (f *Fake) Run(c lima.Cmd) error {
	f.record(c.Args)

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
		return f.shell(c)
	}
	return fmt.Errorf("limafake: unhandled command: %s", c.Args[0])
}

func (f *Fake) record(args []string) {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if strings.Contains(a, "\n") || len(a) > 60 {
			f.Scripts = append(f.Scripts, a)
			parts = append(parts, fmt.Sprintf("<script:%d>", len(f.Scripts)))
			continue
		}
		parts = append(parts, a)
	}
	f.Calls = append(f.Calls, strings.Join(parts, " "))
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

// shell handles the three shapes ptrbox uses:
//
//	shell <vm> -- sudo <cmd...>     proxy VM management (file ops + squid)
//	shell <vm> -- bash -lc <script> a verification run
//	shell <vm> -- bash -c <script>  the token injection, payload on stdin
//
// Which verification is which is decided by the VM: the proxy gets
// verify-proxy.sh, everything else gets verify.sh, and the two fail
// independently because an install and a sandbox creation can each go wrong
// while the other is fine.
func (f *Fake) shell(c lima.Cmd) error {
	vm := arg(c.Args, 1)
	rest := c.Args[min(3, len(c.Args)):]

	switch {
	case len(rest) > 0 && rest[0] == "sudo":
		return f.sudo(c, vm, rest[1:])

	case vm == config.ProxyVM && len(rest) > 1 && rest[1] == "-lc":
		if f.ProxyVerifyFails {
			fmt.Fprintln(c.Stderr, "  squid listening        FAIL - nothing accepts connections on port 8888 in the VM")
			return errors.New("proxy verification failed")
		}
		return nil
	case len(rest) > 2 && rest[1] == "-c" && strings.Contains(rest[2], ".claude/projects"):
		// The transcript pull: a tar streamed out on stdout. No output at all
		// is how a VM says it has nothing to archive.
		if body := f.Transcripts[vm]; len(body) > 0 {
			c.Stdout.Write(body)
		}
		return nil

	case len(rest) > 1 && rest[1] == "-c":
		// Token injection: the payload arrives on stdin and must never be in
		// argv, which is exactly what these tests exist to prove.
		if c.Stdin != nil {
			body, err := io.ReadAll(c.Stdin)
			if err != nil {
				return err
			}
			f.Stdins = append(f.Stdins, string(body))
		}
		return nil
	default:
		if f.VerifyFails {
			fmt.Fprintln(c.Stderr, "  sudo removed          FAIL - the agent user still has root")
			return errors.New("verification failed")
		}
		return nil
	}
}

func (f *Fake) sudo(c lima.Cmd, vm string, args []string) error {
	if len(args) == 0 {
		return errors.New("limafake: sudo with no command")
	}
	switch args[0] {
	case "tee":
		body, err := io.ReadAll(c.Stdin)
		if err != nil {
			return err
		}
		f.WriteFile(vm, args[1], string(body))
		return nil

	case "cat":
		body, ok := f.Files[vm][args[1]]
		if !ok {
			fmt.Fprintf(c.Stderr, "cat: %s: No such file or directory\n", args[1])
			return errors.New("cat failed")
		}
		io.WriteString(c.Stdout, body)
		return nil

	case "mv":
		body, ok := f.Files[vm][args[1]]
		if !ok {
			return fmt.Errorf("limafake: mv: no such file: %s", args[1])
		}
		f.WriteFile(vm, args[2], body)
		delete(f.Files[vm], args[1])
		return nil

	case "rm":
		for _, path := range args[1:] {
			if !strings.HasPrefix(path, "-") {
				delete(f.Files[vm], path)
			}
		}
		return nil

	case "squid":
		// `squid -k parse`, `squid -f FILE -k parse`, `squid -k reconfigure`
		if f.SquidParseFails && contains(args, "parse") {
			fmt.Fprintln(c.Stderr, "squid: FATAL: Bungled config")
			return errors.New("squid parse failed")
		}
		return nil

	case "systemctl":
		return nil

	case "tail":
		return f.tail(c, vm, args[1:])
	}
	return fmt.Errorf("limafake: unhandled sudo command: %s", args[0])
}

// tail handles `tail -n N [-f] <path>`. -f is not simulated: a fake that
// blocked would hang the suite, and emitting the tail once is enough to test
// the plumbing.
func (f *Fake) tail(c lima.Cmd, vm string, args []string) error {
	n, path := 10, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-n":
			i++
			parsed, err := strconv.Atoi(arg(args, i))
			if err != nil {
				return fmt.Errorf("limafake: tail -n %q", arg(args, i))
			}
			n = parsed
		case "-f":
		default:
			path = args[i]
		}
	}
	body, ok := f.Files[vm][path]
	if !ok {
		fmt.Fprintf(c.Stderr, "tail: cannot open '%s'\n", path)
		return errors.New("tail failed")
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if body == "" {
		return nil
	}
	fmt.Fprintln(c.Stdout, strings.Join(lines, "\n"))
	return nil
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
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

// WriteFile places content in a VM's filesystem.
func (f *Fake) WriteFile(vm, path, content string) {
	if f.Files == nil {
		f.Files = map[string]map[string]string{}
	}
	if f.Files[vm] == nil {
		f.Files[vm] = map[string]string{}
	}
	f.Files[vm][path] = content
}

// ReadFile returns a file from a VM's filesystem.
func (f *Fake) ReadFile(vm, path string) (string, bool) {
	body, ok := f.Files[vm][path]
	return body, ok
}

// Called reports whether any invocation matches the regular expression.
func (f *Fake) Called(pattern string) bool { return f.callIndex(pattern) >= 0 }

// CallIndex is the position of the first invocation matching pattern, or -1.
func (f *Fake) callIndex(pattern string) int {
	re := regexp.MustCompile(pattern)
	for i, call := range f.Calls {
		if re.MatchString(call) {
			return i
		}
	}
	return -1
}

// InOrder reports whether the first call matching first precedes the first
// call matching second. Used to assert orderings that matter: validate before
// start, the proxy up before a sandbox, a candidate parsed before it is moved
// into place.
func (f *Fake) InOrder(first, second string) bool {
	i, j := f.callIndex(first), f.callIndex(second)
	return i >= 0 && j >= 0 && i < j
}

// CallLog is the recorded invocations as one string, for failure messages.
func (f *Fake) CallLog() string { return strings.Join(f.Calls, "\n") }

// Reset clears the call log, keeping VM and filesystem state - the equivalent
// of starting a fresh assertion window mid-scenario.
func (f *Fake) Reset() { f.Calls, f.Scripts, f.Stdins = nil, nil, nil }
