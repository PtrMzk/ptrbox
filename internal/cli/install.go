package cli

// cmd_install - idempotent host setup.
//
// Re-running this on a configured Mac changes nothing and says so. That
// property is what makes it safe to run before `make smoke`, or after
// upgrading ptrbox.
//
// The egress proxy is a dedicated Lima VM, so "install" means: check
// dependencies, seed the host-side allowlist, provision/start the proxy VM and
// push the current squid config into it, wire up ssh, and offer a PATH
// symlink. Nothing squid-related is installed on the Mac itself.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
)

const installHelp = `ptrbox install - set up the host

  -y, --yes      answer yes to every prompt
      --no-input never prompt; decline anything that would need an answer
`

func cmdInstall(env *Env, args []string) error {
	for _, arg := range args {
		switch arg {
		case "-y", "--yes":
			env.AssumeYes = true
		case "--no-input":
			env.NoInput = true
		case "-h", "--help":
			fmt.Fprint(env.Stdout, installHelp)
			return nil
		default:
			return fmt.Errorf("install: unknown option %q", arg)
		}
	}

	if err := preflightDeps(env); err != nil {
		return err
	}

	// --- directories --------------------------------------------------------
	for _, dir := range []string{env.Cfg.RepoRoot, config.GeneratedDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".ssh", "config.d"), 0o700); err != nil {
		return err
	}

	// --- ssh include --------------------------------------------------------
	if err := installSSHInclude(env); err != nil {
		return err
	}

	// --- the egress proxy VM ------------------------------------------------
	// Seeds the allowlist, creates/starts the proxy VM, and pushes the current
	// config.
	changed, err := env.Proxy.Ensure()
	if err != nil {
		return err
	}
	if err := reportAllowlist(env); err != nil {
		return err
	}

	// --- prove the egress path works ----------------------------------------
	// Before anything else, including the questions: an install that cannot
	// carry traffic has nothing to offer a PATH symlink for.
	if err := verifyEgress(env); err != nil {
		return err
	}

	// --- put ptrbox on PATH -------------------------------------------------
	if err := installSymlink(env); err != nil {
		return err
	}

	// --- report -------------------------------------------------------------
	preflightReport(env)
	if changed {
		env.Out.Say("host setup complete")
	} else {
		env.Out.Say("host already set up; nothing to do")
	}
	env.Out.Say("next: ptrbox new <repo>")
	return nil
}

// --- ssh ---------------------------------------------------------------------

func installSSHInclude(env *Env) error {
	path := filepath.Join(os.Getenv("HOME"), ".ssh", "config")

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	const include = "Include config.d/*"
	if bytes.Contains(existing, []byte(include)) {
		return nil // idempotent
	}

	// Prepended, because an Include has to come before any Host block that
	// would otherwise claim the connection.
	body := append([]byte(include+"\n"), existing...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	// WriteFile leaves an existing file's mode alone, and ssh is particular
	// about this one.
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	env.Out.Say("added 'Include config.d/*' to ~/.ssh/config")
	return nil
}

// --- PATH --------------------------------------------------------------------

// installSymlink offers to symlink the running binary somewhere on PATH.
// Asked rather than assumed: writing into a directory outside the checkout is
// the user's call, and the default when there is no tty is to decline.
func installSymlink(env *Env) error {
	if env.Exe == "" {
		return nil
	}
	target := filepath.Join(env.Cfg.BinDir, "ptrbox")

	// Already installed here - by `go install`, say. Linking a file to itself
	// is not a favour.
	if target == env.Exe {
		return nil
	}
	if existing, err := os.Readlink(target); err == nil && existing == env.Exe {
		return nil // already ours; nothing to say
	}

	if _, err := os.Lstat(target); err == nil {
		env.Out.Say("%s already exists and is not this ptrbox", target)
		if !confirm(env, fmt.Sprintf("replace it with a symlink to %s?", env.Exe)) {
			return nil
		}
	} else if !confirm(env, fmt.Sprintf("symlink ptrbox into %s so it is on your PATH?", env.Cfg.BinDir)) {
		env.Out.Say("skipped; run it as %s, or symlink it yourself", env.Exe)
		return nil
	}

	if err := os.MkdirAll(env.Cfg.BinDir, 0o755); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(env.Exe, target); err != nil {
		return err
	}
	if err := config.RecordManifest("linked " + target); err != nil {
		return err
	}
	env.Out.Say("linked %s -> %s", target, env.Exe)

	// A symlink in a directory nobody searches is a silent no-op, so check.
	if !onPath(env.Cfg.BinDir) {
		env.Out.Warn("%s is not on your PATH - add it to ~/.zshrc", env.Cfg.BinDir)
	}
	return nil
}

func onPath(dir string) bool {
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == dir {
			return true
		}
	}
	return false
}

// --- allowlist reporting -----------------------------------------------------

// The allowlist is the user's living capability list: seeded once, then never
// overwritten - only reported on.
func reportAllowlist(env *Env) error {
	target := config.AllowlistPath()
	current, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	shipped, err := fs.ReadFile(env.Assets, "host/allowed_domains.txt")
	if err != nil {
		return err
	}
	if bytes.Equal(current, shipped) {
		return nil
	}
	env.Out.Say("%s differs from the shipped allowlist (yours is kept).", target)

	// The shipped list is embedded, so there is no file to diff against any
	// more. Naming the entries you are missing is what the diff was for
	// anyway: a ptrbox upgrade that adds a domain should be visible.
	var missing []string
	for _, entry := range allowEntries(shipped) {
		if !allowContains(current, entry) {
			missing = append(missing, entry)
		}
	}
	if len(missing) > 0 {
		env.Out.Say("shipped entries yours does not have: %s", strings.Join(missing, " "))
	}
	return nil
}

// --- egress verification -----------------------------------------------------

// verifyEgress asserts the path a sandbox's traffic actually takes, and is
// what install's success message means. It is split across the boundary
// because the two halves cannot see each other: the host owns the port
// forward and knows nothing about squid; the proxy VM owns squid and cannot
// tell whether Lima ever published the forward.
//
// A failure here is a failed install, not a warning. The alternative is what
// this replaces - "host setup complete" on the strength of a config squid
// merely parsed - and a proxy that is down surfaces later as an agent with no
// network, which is a much harder thing to trace back to here.
func verifyEgress(env *Env) error {
	env.Out.Say("verifying the egress path")

	// The host's entire share of the proxy is this forward. Nothing listening
	// means Lima never published it, and every sandbox's traffic would go to a
	// closed port however healthy squid is on the other side.
	if !portInUse(env.Cfg.ProxyPort) {
		return fmt.Errorf("nothing is listening on 127.0.0.1:%d - the %s port forward is not up, so no sandbox could reach the proxy. Check it with: limactl list",
			env.Cfg.ProxyPort, config.ProxyVM)
	}

	return env.Proxy.Verify()
}

// --- dependencies ------------------------------------------------------------

// Required commands, paired with the Homebrew formula that provides them.
// limactl comes from the `lima` formula, which is the one that trips people
// up.
//
// Only what ptrbox actually runs on the host - every entry here is a hard
// blocker on install. (squid used to be listed and ran on the host; it lives
// inside the proxy VM now and is only ever invoked through limactl shell.)
var deps = []struct{ tool, formula string }{
	{"limactl", "lima"},
	{"git", "git"},
}

// ptrbox never installs packages on your behalf. Running a package manager as
// a side effect of "set this up for me" should be your keystroke, not ours -
// so a missing dependency prints the command to run and stops.
func preflightDeps(env *Env) error {
	var tools, formulae []string
	for _, dep := range deps {
		if !haveTool(env, dep.tool) {
			tools = append(tools, dep.tool)
			formulae = append(formulae, dep.formula)
		}
	}
	if len(tools) == 0 {
		return nil
	}
	env.Out.Say("missing dependencies: %s", strings.Join(tools, " "))
	env.Out.Say("install them with:")
	env.Out.Say("  brew install %s", strings.Join(formulae, " "))
	return ErrReported
}

// haveTool answers for limactl through the lima client, so a test's fake
// counts as an installed limactl.
func haveTool(env *Env, tool string) bool {
	if tool == "limactl" {
		return env.Lima.Available()
	}
	return lookPath(tool)
}

// preflightReport prints non-fatal environment notes: things worth knowing
// about that should not stop an install.
func preflightReport(env *Env) {
	if !env.Keychain.Available() {
		env.Out.Warn("no macOS Keychain (`security`) - VMs will need CLAUDE_CODE_OAUTH_TOKEN set by hand")
		return
	}
	if env.Keychain.Token(env.Cfg.KeychainService) == "" {
		env.Out.Warn("no Keychain entry %q - new VMs will be unauthenticated. Create one with:",
			env.Cfg.KeychainService)
		env.Out.Warn("  claude setup-token")
		env.Out.Warn("  security add-generic-password -a \"$USER\" -s %s -w", env.Cfg.KeychainService)
	}

	// A foreign listener on the proxy port means VMs would talk to whatever
	// that is - worth flagging, not worth blocking on (it is usually the
	// ptrbox-proxy port forward this very run just brought up).
	if portInUse(env.Cfg.ProxyPort) {
		env.Out.Say("something is listening on port %d (expected: the ptrbox-proxy port forward)",
			env.Cfg.ProxyPort)
	}
}
