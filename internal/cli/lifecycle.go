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

	"github.com/PtrMzk/ptrbox/internal/backend"
	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/proxy"
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
		fmt.Sprintf("%q is the shared egress proxy, not a sandbox - it stops by itself when the last sandbox does (%s if you really mean to destroy it)",
			config.ProxyVM, env.Backend.Facts().DeleteAdvice(config.ProxyVM)))
	if err != nil {
		return err
	}

	if !env.Backend.Exists(name) {
		// A `new` that failed before the VM existed still allocated a proxy
		// port and rendered a config. Those must be rm-able, or an abandoned
		// name holds one of the sixteen slots forever with nothing anywhere
		// saying how to free it.
		if removed, err := removeArtifacts(env.Backend.Facts(), name); err != nil {
			return err
		} else if removed {
			env.Out.Say("no VM named %q, but a failed create had left its artifacts - removed them", name)
			return retireFromProxy(env)
		}
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

	if err := env.Backend.Delete(name); err != nil {
		return err
	}
	if _, err := removeArtifacts(env.Backend.Facts(), name); err != nil {
		return err
	}
	env.Out.Say("deleted VM %q (the repo on the host is untouched)", name)
	return retireFromProxy(env)
}

// removeArtifacts deletes what `new` created outside the VM itself: the
// rendered config, the repo sidecar, the ssh symlink (on a backend that has
// one), and the proxy-port sidecar - the port is the VM's slot at the proxy,
// and holding it past the VM would leak one of the sixteen. The VM's
// allowlist is deliberately NOT among these: it is what makes a later
// re-create come back with the same egress.
func removeArtifacts(facts backend.Facts, name string) (removed bool, err error) {
	paths := []string{config.GeneratedConfig(name), config.RepoFile(name), proxy.PortFile(name)}
	if facts.HasSSHConfigLink {
		paths = append(paths, config.SSHConfigLink(name))
	}
	for _, path := range paths {
		switch err := os.Remove(path); {
		case err == nil:
			removed = true
		case !errors.Is(err, fs.ErrNotExist):
			return removed, err
		}
	}
	return removed, nil
}

// retireFromProxy is rm's tail: the departed VM's rules leave the running
// proxy (so the freed port serves deny-all until reallocated rather than a
// dead VM's list), and the proxy itself stops if nobody is left.
func retireFromProxy(env *Env) error {
	if env.Proxy.Running() {
		if result, err := env.Proxy.Sync(); err != nil {
			return err
		} else if result == proxy.Rejected {
			return fmt.Errorf("squid rejected the updated proxy configuration; the proxy VM was rolled back. Check %s",
				config.AllowlistPath())
		}
	}

	// With this sandbox gone the proxy may have nobody left to serve.
	return env.Proxy.StopIfIdle()
}

func cmdStart(env *Env, args []string) error {
	name, err := sandboxTarget(env, args, "start",
		fmt.Sprintf("%q starts automatically with any sandbox - start one of those instead", config.ProxyVM))
	if err != nil {
		return err
	}

	if err := startSandbox(env, name, args[0]); err != nil {
		return err
	}
	env.Out.Say("enter it: %s", env.Backend.Facts().ShellAdvice(name))
	return nil
}

// startSandbox is everything `start` does to bring a sandbox up, without the
// closing advice: the proxy first, the hooks redirect re-asserted, then the
// VM. `shell` runs it too, for a sandbox it finds stopped. asked is what the
// user typed, for the message that tells them how to create it.
func startSandbox(env *Env, name, asked string) error {
	if !env.Backend.Exists(name) {
		return fmt.Errorf("no VM named %q - create it with: ptrbox new %s", name, asked)
	}

	// Proxy first. This also pushes any allowlist edits made while it was down.
	if _, err := env.Proxy.Ensure(); err != nil {
		return err
	}

	// Re-assert the hooks redirect before the sandbox comes up.
	//
	// `new` set it once, at create, and nothing has touched it since - so a
	// single `git config --unset core.hooksPath` inside the mount was
	// permanent and silent, and the control quietly stopped existing. The
	// pattern this follows is 90-harden.sh, which is deliberately unguarded so
	// the sudo removal re-asserts on every boot: a control that has to hold
	// continuously cannot be applied once. This is that, for the host side.
	if repoDir, ok := mountedRepo(name); ok {
		// This VM's own answer, not the host default: PTRBOX_HOST_HOOKS is
		// per-VM, and a repo whose owner allowed hooks must not have them
		// taken back every time the sandbox starts.
		cfg, err := env.Cfg.Overlay(name)
		if err != nil {
			return err
		}
		if err := neutraliseHooks(env, cfg, repoDir); err != nil {
			return err
		}
	}

	if env.Backend.Running(name) {
		env.Out.Say("VM %q is already running", name)
		// Running is not ready: on a backend whose daemon brings VMs back
		// after a host reboot, the mount can be on record and absent from
		// the guest. Ready repairs what it can, or says what is missing.
		return env.Backend.Ready(name)
	}
	return env.Backend.Start(name)
}

func cmdStop(env *Env, args []string) error {
	name, err := sandboxTarget(env, args, "stop",
		fmt.Sprintf("%q stops automatically when the last sandbox does - stop the sandboxes instead", config.ProxyVM))
	if err != nil {
		return err
	}

	if !env.Backend.Exists(name) {
		return unknownVM(env, name)
	}

	if env.Backend.Running(name) {
		if err := env.Backend.Stop(name); err != nil {
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
	if !env.Backend.Running(name) {
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
	if err := requireBackend(env); err != nil {
		return "", err
	}
	return resolveSandbox(args[0], reserved)
}

// unknownVM reports a miss with the list of what does exist, which is almost
// always what the user needed to see.
func unknownVM(env *Env, name string) error {
	if names := env.Backend.Names(); len(names) > 0 {
		env.Out.Say("existing VMs: %s", strings.Join(names, " "))
	}
	return fmt.Errorf("no VM named %q", name)
}
