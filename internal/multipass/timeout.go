package multipass

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/PtrMzk/ptrbox/internal/backend"
)

// TimedRunner runs the multipass binary with a deadline on every call.
//
// The multipass daemon can lock up: step 0 saw it wedge on a mount requested
// during a resume, and the first real run saw it wedge re-initialising a
// sandbox's mount as the PC booted, with every client call - `multipass
// list` included - hanging until the service was killed. A client call that
// never returns is a ptrbox that never returns, so each one gets the time its
// verb can legitimately take and no more; past that, the error says what to
// do, since the recovery needs an admin shell ptrbox does not have.
type TimedRunner struct {
	Binary string
	// Timeout is the deadline for one invocation, from its argv.
	Timeout func(args []string) time.Duration
}

// Run executes the invocation, killing it at the deadline.
//
// An interactive invocation is exempt: `ptrbox shell` is a session a person
// is working in - a Claude Code run inside a sandbox lasts as long as it
// lasts - and a deadline there kills the work it was meant to protect,
// leaving the terminal in whatever mode the program that owned it had set.
// The person at the keyboard is that call's timeout.
func (r TimedRunner) Run(c backend.Cmd) error {
	ctx := context.Background()
	if !c.Interactive {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout(c.Args))
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, r.Binary, c.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s %s did not return within %s - the multipass daemon may be wedged "+
			"(it can lock up while re-establishing a mount as the PC boots). Recover it from an ADMIN PowerShell:\n"+
			"    Stop-Process -Name multipassd -Force\n    Start-Service Multipass\nthen retry",
			r.Binary, strings.Join(c.Args, " "), r.Timeout(c.Args))
	}
	return err
}

// Available reports whether the binary is on PATH.
func (r TimedRunner) Available() bool { return backend.ExecRunner{Binary: r.Binary}.Available() }

// timeoutFor is the deadline for one verb. Generous where the verb waits on
// a guest - a launch is the whole of boot-1 provisioning, bounded by its own
// --timeout; a start, restart or exec can wait on cloud-init or a slow
// verification - and short where it only talks to the daemon, since that is
// where a wedge shows first.
func timeoutFor(args []string) time.Duration {
	switch verb(args) {
	case "launch":
		return 30 * time.Minute
	case "start", "restart", "stop", "delete", "exec":
		return 10 * time.Minute
	}
	return 2 * time.Minute
}

func verb(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
