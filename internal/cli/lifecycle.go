package cli

// rm, start and stop.
//
// rm removes the Lima VM and its disk, the generated config and the ssh config
// symlink. It never touches the repo on the host - that is the whole point of
// keeping the repo outside the VM's lifecycle.
//
// start and stop are thin wrappers over limactl whose whole reason to exist is
// the proxy coupling: the proxy must be up before a sandbox is, because from
// the sandbox's point of view the proxy is the entire internet, and it comes
// down once the last sandbox does. `limactl start` used directly skips both.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
)

func cmdRm(env *Env, args []string) error {
	archive := true
	var rest []string
	for _, arg := range args {
		switch arg {
		case "--no-archive":
			archive = false
		default:
			rest = append(rest, arg)
		}
	}
	args = rest

	name, err := sandboxTarget(env, args, "rm",
		fmt.Sprintf("%q is the shared egress proxy, not a sandbox - it stops by itself when the last sandbox does (limactl delete %s if you really mean to destroy it)",
			config.ProxyVM, config.ProxyVM))
	if err != nil {
		return err
	}

	if !env.Lima.Exists(name) {
		return unknownVM(env, name)
	}

	// Transcripts first: after Delete they are gone with the disk. A failure
	// here stops the removal rather than quietly losing the session - pass
	// --no-archive if you meant to throw it away.
	if archive {
		if err := archiveBeforeRemoval(env, name); err != nil {
			return err
		}
	}

	if err := env.Lima.Delete(name); err != nil {
		return err
	}
	for _, path := range []string{config.GeneratedConfig(name), config.SSHConfigLink(name)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	env.Out.Say("deleted VM %q (the repo on the host is untouched)", name)

	// With this sandbox gone the proxy may have nobody left to serve.
	return env.Proxy.StopIfIdle()
}

func cmdStart(env *Env, args []string) error {
	name, err := sandboxTarget(env, args, "start",
		fmt.Sprintf("%q starts automatically with any sandbox - start one of those instead", config.ProxyVM))
	if err != nil {
		return err
	}

	if !env.Lima.Exists(name) {
		return fmt.Errorf("no VM named %q - create it with: ptrbox new %s", name, args[0])
	}

	// Proxy first. This also pushes any allowlist edits made while it was down.
	if _, err := env.Proxy.Ensure(); err != nil {
		return err
	}

	if env.Lima.Running(name) {
		env.Out.Say("VM %q is already running", name)
	} else if err := env.Lima.Start(name); err != nil {
		return err
	}
	env.Out.Say("enter it: ssh lima-%s", name)
	return nil
}

func cmdStop(env *Env, args []string) error {
	name, err := sandboxTarget(env, args, "stop",
		fmt.Sprintf("%q stops automatically when the last sandbox does - stop the sandboxes instead", config.ProxyVM))
	if err != nil {
		return err
	}

	if !env.Lima.Exists(name) {
		return unknownVM(env, name)
	}

	if env.Lima.Running(name) {
		if err := env.Lima.Stop(name); err != nil {
			return err
		}
		env.Out.Say("stopped VM %q", name)
	} else {
		env.Out.Say("VM %q is not running", name)
	}

	// Even a no-op stop re-checks: a proxy left over from a crash gets cleaned
	// up on the next explicit stop rather than lingering forever.
	return env.Proxy.StopIfIdle()
}

// archiveBeforeRemoval saves what the VM can still tell us. A stopped VM
// cannot answer, and starting one just to read it would be a surprising thing
// for `rm` to do - so rm stops there instead. Warning and deleting anyway was
// the earlier behaviour, and it offered a recovery ("start it, then save") that
// the next line made impossible: the transcripts were gone with the disk before
// the user could act on the advice. Refusing keeps both choices open, and the
// only way to lose a session is now to ask for it.
func archiveBeforeRemoval(env *Env, name string) error {
	if !env.Lima.Running(name) {
		return fmt.Errorf("VM %q is not running, so its Claude transcripts cannot be archived - keep them with: ptrbox start %s && ptrbox save %s && ptrbox rm %s (or discard them with: ptrbox rm --no-archive %s)",
			name, name, name, name, name)
	}
	path, err := archiveTranscripts(env, name)
	if err != nil {
		return fmt.Errorf("%w (pass --no-archive to remove the VM anyway)", err)
	}
	if path == "" {
		env.Out.Say("no Claude transcripts to archive")
	}
	return nil
}

// sandboxTarget does the argument handling the three commands share. Their
// name derivation is identical to `new`'s, so a repo path, a bare repo name
// and the VM name all resolve the same way and the commands cannot drift.
func sandboxTarget(env *Env, args []string, verb, reserved string) (string, error) {
	if len(args) == 0 || args[0] == "" {
		return "", fmt.Errorf("usage: ptrbox %s <repo-path | vm-name>", verb)
	}
	if err := requireLima(env); err != nil {
		return "", err
	}
	return resolveSandbox(args[0], reserved)
}

// unknownVM reports a miss with the list of what does exist, which is almost
// always what the user needed to see.
func unknownVM(env *Env, name string) error {
	if names := env.Lima.Names(); len(names) > 0 {
		env.Out.Say("existing VMs: %s", strings.Join(names, " "))
	}
	return fmt.Errorf("no VM named %q", name)
}
