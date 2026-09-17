package backend

import "io"

// User is which account inside a guest a command runs as. The distinction is
// the backend's to draw: a backend whose transport logs in as the sandbox
// user has one account and both names mean it.
type User int

const (
	// Login is the account the backend's own transport arrives as - the one
	// that exists before ptrbox has provisioned anything. Everything on the
	// proxy VM runs as Login.
	Login User = iota
	// Agent is the account Claude Code runs as. What is verified, timed,
	// archived and handed the token is the agent's view of the guest, so
	// those run as Agent.
	Agent
)

// StatusRunning is the only status ptrbox tests for by name.
const StatusRunning = "Running"

// VM is one entry of the backend's listing.
type VM struct {
	Name   string
	Status string
}

// Mount is one host directory made visible in a guest.
type Mount struct {
	Host  string
	Guest string
}

// Spec is everything a backend is told when it creates a VM.
//
// ConfigPath is the rendered config, which is the authority on what the VM
// contains. The rest repeats what that file already decided, for a backend
// whose create command wants it as arguments rather than reading it from the
// file: lima reads the file and ignores every field below ConfigPath.
type Spec struct {
	Name       string
	ConfigPath string

	CPUs   int
	Memory string
	Disk   string
	Image  string
	// Mount is nil for a VM with no mount, which is the proxy. There is no
	// second one to name: a sandbox has exactly one (invariant 3), so this is
	// a pointer and not a slice on purpose.
	Mount *Mount
}

// Reach is how the host gets to the proxy VM's squid.
type Reach int

const (
	// LoopbackForward: the backend publishes the guest's ports on the host's
	// 127.0.0.1, and only while the guest has a listener there - so a probe
	// waits with a deadline, and a foreign listener on the port is a conflict
	// to report before the proxy exists.
	LoopbackForward Reach = iota
	// DirectAddress: the proxy VM has an address of its own and the host
	// dials it. Nothing is published on the host's loopback, so there is no
	// port there to conflict with.
	DirectAddress
)

// Dep is a command the backend needs on the host, paired with the package
// that provides it - which is the half that trips people up when the two
// names differ.
type Dep struct {
	Tool    string
	Package string
}

// Facts is what differs between backends and is not an action: the addresses
// a guest dials, the templates it is built from, what to tell the user to
// type. They are values rather than methods so that the code which reads them
// stays a plain rendering or a plain message, and so a test can vary one
// without writing a backend.
type Facts struct {
	// Name is the backend's, lowercase, as it would appear in a sentence.
	Name string

	// ProxyAddr is where a sandbox dials squid: rendered into the guest's
	// firewall rule, HTTPS_PROXY and NO_PROXY.
	ProxyAddr string
	// ProxyReach is how the HOST gets to the same squid.
	ProxyReach Reach

	// HostAddr is where a sandbox dials a service on the host itself (LM
	// Studio). HostServices says whether that trip exists at all; without it
	// a feature that needs one is refused when the plan is made, not
	// discovered missing after the VM is built.
	HostAddr     string
	HostServices bool

	// SandboxTemplate and ProxyTemplate are asset paths, rendered into the
	// config Create is handed.
	SandboxTemplate string
	ProxyTemplate   string

	// HasSSHConfigLink: the backend writes an ssh config per VM that ptrbox
	// links into ~/.ssh/config.d, and removes with the VM.
	HasSSHConfigLink bool
	// ShellAdvice is what to type to get a shell in the VM.
	ShellAdvice func(vm string) string
	// ListHint is what to type to see the backend's own view of its VMs.
	ListHint string
	// ExecAdvice is what to type to run a command in the VM, for the error
	// that ends in "look at it with". DeleteAdvice is what to type to destroy
	// a VM ptrbox itself declines to.
	ExecAdvice   func(vm string, argv ...string) string
	DeleteAdvice func(vm string) string

	// Deps are the host commands this backend cannot work without. The first
	// is the backend's own binary - the one Available answers for.
	Deps []Dep
}

// Backend is the whole of what ptrbox asks of the thing that builds its VMs.
//
// In-guest commands are argument vectors plus the account to run them as;
// how a backend spells "run this in that VM as that user" is its own
// business, and no caller builds that spelling. Nothing here takes a command
// line as one string: see the package comment.
type Backend interface {
	// Available reports whether the backend can be run at all. Several
	// commands lead with this so the failure is "run ptrbox install first"
	// rather than an exec error from three layers down.
	Available() bool
	Facts() Facts

	// List returns every VM the backend knows about. A listing that fails
	// returns no VMs and no error: callers use this to decide whether to
	// leave the proxy running, and "I could not tell" must land on the same
	// side as "yes, something is running".
	List() []VM
	Names() []string
	// Status is the VM's status, or "" if it does not exist.
	Status(vm string) string
	Exists(vm string) bool
	Running(vm string) bool

	// Validate checks a rendered config before any VM state is touched.
	Validate(configPath string) error
	// Create provisions a new VM and returns when its first boot is done.
	Create(Spec) error
	Start(vm string) error
	Stop(vm string) error
	Delete(vm string) error

	// Output captures stdout; any stderr becomes part of the error.
	Output(vm string, user User, argv ...string) (string, error)
	// Send wires stdin and captures nothing. It is how the auth token
	// reaches a guest: never in an argument vector, never in a file.
	Send(vm string, user User, stdin io.Reader, argv ...string) error
	// Stream sends stdout to w as it arrives.
	Stream(vm string, user User, w io.Writer, argv ...string) error
	// Passthrough sends the output to the user, narrated.
	Passthrough(vm string, user User, argv ...string) error

	// Shell opens an interactive shell in the VM as the agent, in /workspace,
	// on the streams it is given. Those are the user's terminal and go to the
	// child untouched: an interactive session is not a log to translate, and
	// a narrator between a person and their prompt would hold every byte
	// until the next newline. The error is the shell's own exit, which is the
	// exit status of whatever ran last in it - the caller's to pass on, not
	// to report.
	Shell(vm string, stdin io.Reader, stdout, stderr io.Writer) error
}
