// Package cli is ptrbox's command surface.
//
// Every command runs on the HOST with your privileges. Nothing here is ever
// provisioned into a sandbox VM - an agent that can edit its own sandbox's
// provisioning code is not sandboxed.
package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/proxy"
	"github.com/PtrMzk/ptrbox/internal/ui"
)

// Env is everything a command needs that is not its own arguments. Assembled
// once in main and, in tests, assembled against fakes - which is what lets the
// whole lifecycle be simulated without a Mac.
type Env struct {
	Cfg    *config.Config
	Lima   *lima.Client
	Proxy  *proxy.Proxy
	Assets fs.FS

	// Out carries progress notes; Stdout carries command output that a caller
	// might pipe (a log tail, the allowlist).
	Out    ui.Printer
	Stdout io.Writer
	Stdin  io.Reader

	// Keychain is where the Claude token comes from.
	Keychain Keychain

	// Exe is the path to the running binary, for the PATH symlink offer.
	Exe string

	// Interactive says whether a question can be asked at all.
	Interactive bool
	// AssumeYes and NoInput come from install's flags.
	AssumeYes bool
	NoInput   bool

	// Editor opens a file for the user. A field so tests can supply one
	// without needing a real editor on the machine.
	Editor func(path string) error

	// Now is the clock, a field so an archive filename is reproducible in
	// tests.
	Now func() time.Time

	// Load resolves the configuration and everything derived from it. It is
	// called only for commands that need it, so `ptrbox help` and
	// `ptrbox version` still work on a host whose config file does not parse.
	Load func(*Env) error

	// LoadVM re-resolves the configuration with a VM's per-VM overrides
	// layered in, once its name is known. Only `new` calls it: it is the only
	// command that consumes a per-VM key, because those keys are frozen into
	// the generated Lima config at create time and inert afterwards.
	//
	// A separate hook rather than a Config method call at the site, because
	// re-resolving also re-answers the things main derives from a config -
	// which image the narrator names, and the Proxy's view of the world.
	LoadVM func(env *Env, vm string) error
}

// now is the clock, defaulting to the real one. A method rather than a bare
// field read: a nil Now in some future entry point should not be a panic three
// layers down.
func (e *Env) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

// needsConfig lists the commands that cannot run without a resolved config.
var needsConfig = map[string]bool{
	"install": true, "new": true, "rm": true, "save": true,
	"start": true, "stop": true, "logs": true, "allow": true,
	"sync-proxy": true,
}

// ErrUsage is returned for an unknown command: exit status 2, distinct from a
// command that ran and failed.
var ErrUsage = errors.New("usage")

// ErrReported means the command already explained itself and main should just
// take the exit status. For failures whose useful form is several lines - a
// missing dependency and the brew line that fixes it - where restating the
// headline as "ptrbox: error: ..." would only say it twice.
var ErrReported = errors.New("reported")

// Run dispatches one invocation. args excludes the program name.
func Run(env *Env, args []string) error {
	command := "help"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}

	if needsConfig[command] {
		if err := env.Load(env); err != nil {
			return err
		}
	}

	switch command {
	case "install":
		return cmdInstall(env, args)
	case "new":
		return cmdNew(env, args)
	case "rm":
		return cmdRm(env, args)
	case "start":
		return cmdStart(env, args)
	case "stop":
		return cmdStop(env, args)
	case "logs":
		return cmdLogs(env, args)
	case "allow":
		return cmdAllow(env, args)
	case "sync-proxy":
		return cmdSyncProxy(env, args)
	case "save":
		return cmdSave(env, args)
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, usage)
		return nil
	case "version", "--version":
		fmt.Fprintf(env.Stdout, "ptrbox %s\n", config.Version)
		return nil
	}

	env.Out.Say("unknown command: %s", command)
	fmt.Fprintln(env.Out.W)
	fmt.Fprint(env.Out.W, usage)
	return ErrUsage
}

const usage = `ptrbox - sandboxed Claude Code VMs, one per repo

USAGE
  ptrbox <command> [args]

COMMANDS
  install            set up the host: dependency checks, the squid proxy VM,
                     ssh config. Idempotent; safe to re-run.
  new <repo>         create the repo (if needed) and provision its VM. Prints
                     what it will build and offers to edit that VM's settings
                     and allowlist first; --no-edit skips the offers
  rm <repo|vm>       destroy a VM. Never touches the repo on the host.
  start <repo|vm>    boot an already-provisioned VM (seconds, not minutes)
  stop <repo|vm>     power a VM off, keeping its disk and state
  logs [--denied]    tail the proxy log; --denied shows blocked requests only
  allow <vm> [domain...]
                     add domains to that sandbox's egress allowlist, or open
                     it in $EDITOR with no domains; --list prints it. Each VM
                     has its own list, seeded from the template on first touch
  sync-proxy         push hand-edited allowlists/config to the proxy now
                     (the other commands do this themselves as they run)
  save <repo|vm>     archive the VM's Claude transcripts onto the host
                     (rm does this for you before destroying anything)

  help               this text
  version            print the ptrbox version

new/rm bracket a project's lifetime; start/stop bracket a work session
(freeing RAM overnight, recovering after a Mac reboot). All four keep the
egress proxy - a dedicated VM, "ptrbox-proxy" - consistent automatically:
any sandbox coming up starts it, the last one going down stops it. Prefer
them over limactl start/stop, which know nothing about the proxy.

EXAMPLES
  ptrbox new my-api               # -> ~/code/my-api, git init, VM "my-api"
  ptrbox new ~/src/existing       # explicit path, existing repo used as-is
  ssh lima-my-api                 # then: cd /workspace && claude
  ptrbox logs --denied            # find the domain your build needs
  ptrbox allow my-api files.example.com   # ...then grant it to that sandbox

CONFIGURATION
  ~/.config/ptrbox/config (see config/ptrbox.conf.example). Every key can also
  be set as a PTRBOX_* environment variable, which wins over the file.

OUTPUT
  Progress goes to stderr, command output to stdout. Colour is used when
  stderr is a terminal; --no-color, NO_COLOR and TERM=dumb turn it off.
  limactl's own log is translated into ptrbox's wording as it arrives;
  --verbose shows it exactly as lima wrote it. A failed limactl invocation
  reprints its raw output either way.
`

// requireLima is the first thing several commands do, so that a machine
// without Lima says "run ptrbox install first" rather than failing three
// layers down.
func requireLima(env *Env) error {
	if !env.Lima.Available() {
		return errors.New("limactl not found - run 'ptrbox install' first")
	}
	return nil
}

// resolveSandbox turns a repo path or VM name into a sandbox VM name, refusing
// the shared proxy. reserved explains why the proxy is not a valid target for
// this particular command.
func resolveSandbox(arg, reserved string) (string, error) {
	name, err := config.VMName(arg)
	if err != nil {
		return "", err
	}
	if name == config.ProxyVM {
		return "", errors.New(reserved)
	}
	return name, nil
}

// confirm asks a yes/no question.
//
// Every prompt must be bypassable: an interactive-only question about a
// security-relevant file cannot be covered by the test suite, and an install
// that hangs waiting for input in a script is worse than one that declines.
func confirm(env *Env, question string) bool {
	if env.AssumeYes {
		return true
	}
	if env.NoInput || !env.Interactive {
		env.Out.Say("declining (no input available): %s", question)
		env.Out.Say("re-run with --yes to accept")
		return false
	}

	env.Out.Prompt(question)
	var answer string
	if _, err := fmt.Fscanln(env.Stdin, &answer); err != nil {
		return false
	}
	switch answer {
	case "y", "Y", "yes", "YES":
		return true
	}
	return false
}

// ask is confirm for an offer rather than a decision.
//
// confirm explains itself when it cannot ask, because the thing it guards is
// something the command wanted to do - a symlink, a replaced file - and
// silence there reads as "done" when it means "skipped". An offer to open your
// editor is the opposite: with no terminal there is nothing to open, nobody to
// read the explanation, and no --yes that would help. So it declines quietly,
// which is what keeps `ptrbox new` in a script identical to what it was.
func ask(env *Env, question string) bool {
	if env.NoInput || !env.Interactive {
		return false
	}
	return confirm(env, question)
}
