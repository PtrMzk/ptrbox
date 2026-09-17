package lima

import (
	"io"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/backend"
)

// Gateway is the address a lima guest reaches its host at: the usernet
// relay hands a connection to it over to 127.0.0.1 on the Mac. That makes it
// both where a sandbox dials squid (through the proxy VM's loopback port
// forward) and where it dials a service on the Mac itself.
const Gateway = "192.168.5.2"

// Backend is lima as a backend.Backend.
//
// The listing and the lifecycle are the Client's own methods, promoted. The
// four in-guest calls are declared here and shadow the Client's: the Client
// takes a limactl argv, a Backend takes a VM, an account and the guest's
// argv, and builds the `shell <vm> --` around it so that no caller does.
type Backend struct {
	*Client
}

var _ backend.Backend = Backend{}

func (Backend) Facts() backend.Facts {
	return backend.Facts{
		Name:             "lima",
		ProxyAddr:        Gateway,
		ProxyReach:       backend.LoopbackForward,
		HostAddr:         Gateway,
		HostServices:     true,
		SandboxTemplate:  "vm/claude-repo.yaml",
		ProxyTemplate:    "vm/proxy.yaml",
		HasSSHConfigLink: true,
		ShellAdvice:      func(vm string) string { return "ssh lima-" + vm },
		ListHint:         Binary + " list",
		ExecAdvice: func(vm string, argv ...string) string {
			return Binary + " " + strings.Join(ShellArgs(vm, argv...), " ")
		},
		DeleteAdvice: func(vm string) string { return Binary + " delete " + vm },
		// limactl comes from the `lima` formula, which is the one that trips
		// people up.
		Deps: []backend.Dep{{Tool: Binary, Package: "lima"}},
	}
}

// Create reads the rendered config and nothing else of the Spec: a lima
// template already says the sizing, the image and the mount.
func (b Backend) Create(spec backend.Spec) error {
	return b.Client.Create(spec.Name, spec.ConfigPath)
}

// A lima guest has one account - lima creates it, ssh arrives as it, and
// Claude Code runs as it - so Login and Agent are the same user and the
// argument is not read.

func (b Backend) Output(vm string, _ backend.User, argv ...string) (string, error) {
	return b.Client.Output(ShellArgs(vm, argv...)...)
}

func (b Backend) Send(vm string, _ backend.User, stdin io.Reader, argv ...string) error {
	return b.Client.Send(stdin, ShellArgs(vm, argv...)...)
}

func (b Backend) Stream(vm string, _ backend.User, w io.Writer, argv ...string) error {
	return b.Client.Stream(w, ShellArgs(vm, argv...)...)
}

func (b Backend) Passthrough(vm string, _ backend.User, argv ...string) error {
	return b.Client.Passthrough(ShellArgs(vm, argv...)...)
}

// Shell is `limactl shell` with nothing in between: straight to the Runner,
// past the Client's narrated Stdout, on the caller's streams. --workdir
// because limactl otherwise tries the host's current directory inside the
// guest, which exists there only when ptrbox happens to be run from the repo.
func (b Backend) Shell(vm string, stdin io.Reader, stdout, stderr io.Writer) error {
	return b.Client.Runner.Run(Cmd{
		Args:   []string{"shell", "--workdir", "/workspace", vm},
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
}
