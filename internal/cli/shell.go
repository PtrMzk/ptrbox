package cli

// shell - an interactive shell in a sandbox, as the agent, in /workspace.
//
// `ssh lima-<vm>` is lima's way in, and only lima has it. This is ptrbox's,
// and means the same thing on every backend - which is also what lets it do
// the one thing the backend's own tool cannot: notice the sandbox is stopped
// and bring it up the way `start` does, proxy first.

import (
	"errors"
	"fmt"

	"github.com/PtrMzk/ptrbox/internal/config"
)

// ExitStatus is a command that has nothing to say beyond its exit status: the
// shell a person just left, whose status is that of the last thing they ran
// in it. main exits with it and prints nothing - "error: exit status 1" after
// a session that ended with a failed grep would be ptrbox reporting somebody
// else's business as its own failure.
type ExitStatus int

func (s ExitStatus) Error() string { return fmt.Sprintf("exit status %d", int(s)) }

func cmdShell(env *Env, args []string) error {
	name, err := sandboxTarget(env, args, "shell",
		fmt.Sprintf("%q is the shared egress proxy, not a sandbox - nothing of yours runs there. Look inside it with: %s",
			config.ProxyVM, env.Backend.Facts().ExecAdvice(config.ProxyVM, "bash")))
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return fmt.Errorf("shell: one sandbox, and no command (got %q) - it opens a shell for you to type into", args[1])
	}

	// A stopped sandbox is started rather than refused: whoever typed this
	// wants to be in it, and the alternative is an error telling them to run
	// the command this can run for them. Through startSandbox, not the
	// backend - a sandbox started without its proxy has no network.
	if !env.Backend.Running(name) {
		if env.Backend.Exists(name) {
			env.Out.Say("VM %q is stopped - starting it", name)
		}
		if err := startSandbox(env, name, args[0]); err != nil {
			return err
		}
	}

	err = env.Backend.Shell(name, env.Stdin, env.Stdout, env.Out.W)
	var exited interface{ ExitCode() int }
	if errors.As(err, &exited) && exited.ExitCode() > 0 {
		return ExitStatus(exited.ExitCode())
	}
	return err
}
