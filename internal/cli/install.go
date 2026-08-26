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

	steps := env.Out.Plan(5)

	// --- preflight ----------------------------------------------------------
	// Everything that can be known before any VM state is touched, said while
	// it is still cheap to act on.
	steps.Next("checking the host")
	if err := preflight(env); err != nil {
		return err
	}

	// --- directories and the ssh include ------------------------------------
	steps.Next("preparing directories and ssh")
	for _, dir := range []string{env.Cfg.RepoRoot, config.GeneratedDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	// The config directory is left ready to edit, not merely present: see
	// seed.go for why an empty directory is not "set up".
	if err := seedConfigDir(env); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".ssh", "config.d"), 0o700); err != nil {
		return err
	}
	if err := installSSHInclude(env); err != nil {
		return err
	}

	// --- the egress proxy VM ------------------------------------------------
	// Seeds the allowlist, creates/starts the proxy VM, and pushes the current
	// config.
	steps.Next("bringing up the egress proxy")
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
	steps.Next("verifying the egress path")
	if err := verifyEgress(env); err != nil {
		return err
	}

	// --- put ptrbox on PATH -------------------------------------------------
	steps.Next("putting ptrbox on PATH")
	if err := installSymlink(env); err != nil {
		return err
	}

	// --- report -------------------------------------------------------------
	headline := "host setup complete"
	if !changed {
		headline = "host already set up; nothing to do"
	}
	env.Out.Summary(headline,
		"next: ptrbox new <repo>",
		"",
		fmt.Sprintf("proxy     %s, reached at 127.0.0.1:%d", config.ProxyVM, env.Cfg.ProxyPort),
		fmt.Sprintf("template  %s (what new VMs may reach)", config.AllowlistPath()),
		fmt.Sprintf("settings  %s", config.Path()),
		fmt.Sprintf("per VM    %s/<vm-name>", config.VMDir()),
	)
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
//
// Every path out of here says what it decided. A step that announces itself
// and then goes quiet reads as "done" when it means "skipped", and this is
// the step people re-run precisely because `which ptrbox` came up empty.
func installSymlink(env *Env) error {
	if env.Exe == "" {
		env.Out.Warn("cannot tell where this binary is; skipping the PATH link")
		return nil
	}
	target := filepath.Join(env.Cfg.BinDir, "ptrbox")

	// Already installed here - by `go install`, say. Linking a file to itself
	// is not a favour.
	if target == env.Exe {
		env.Out.Say("ptrbox is already installed at %s", env.Exe)
		return installPathEntry(env)
	}
	if existing, err := os.Readlink(target); err == nil && existing == env.Exe {
		env.Out.Say("%s already links to this ptrbox", target)
		return installPathEntry(env)
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
	return installPathEntry(env)
}

// installPathEntry gets BinDir searched, or explains how.
//
// Called from every outcome that leaves a usable ptrbox in BinDir, not just
// from the run that created it. The link and the PATH entry are two separate
// facts: a link in a directory nothing searches is a silent no-op, so
// re-running install and hearing nothing is the wrong answer to "why is
// `which ptrbox` empty?".
//
// Offered rather than assumed, like the symlink - a shell startup file is
// somewhere a bad edit costs you every new terminal. But offered, not
// refused: install already prepends an Include to ~/.ssh/config, and printing
// a line for you to paste while editing that one unprompted was an
// inconsistency, not a boundary.
func installPathEntry(env *Env) error {
	if onPath(env.Cfg.BinDir) {
		return nil
	}
	rcFile, line := pathAdvice(env.Cfg.BinDir)

	// A shell ptrbox does not know how to edit, or one with its own way of
	// doing this. Say the line and stop.
	if rcFile == "" {
		env.Out.Warn("%s is not on your PATH, so that link does nothing yet. Add it with:", env.Cfg.BinDir)
		env.Out.Detail("%s", line)
		return nil
	}

	body, err := os.ReadFile(rcFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	// Already written, but not in this process's environment: the file is
	// only read by shells that started after it was. Nothing to add, and the
	// missing step is one nobody can take on the user's behalf.
	if hasLine(body, line) {
		env.Out.Warn("%s is in %s but not in this shell - start a new terminal, or:", env.Cfg.BinDir, rcFile)
		env.Out.Detail("%s", reloadCommand())
		return nil
	}

	env.Out.Say("%s is not on your PATH, so that link does nothing yet", env.Cfg.BinDir)
	if !confirm(env, fmt.Sprintf("add it to %s?", rcFile)) {
		env.Out.Detail("%s", line)
		return nil
	}

	// A leading newline when the file does not end in one, so the export
	// cannot land on the end of somebody's last line.
	addition := "\n# added by ptrbox\n" + line + "\n"
	if len(body) == 0 || bytes.HasSuffix(body, []byte("\n")) {
		addition = addition[1:]
	}
	f, err := os.OpenFile(rcFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(addition); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := config.RecordManifest("appended a PATH line to " + rcFile); err != nil {
		return err
	}
	env.Out.Say("added %s to %s", env.Cfg.BinDir, rcFile)

	// A child process cannot change its parent shell's environment, so this
	// last step is genuinely the user's however much of the rest is not.
	env.Out.Detail("%s", reloadCommand())
	return nil
}

// pathAdvice is how the user's shell puts a directory on PATH: the startup
// file to append to - empty when ptrbox should not guess - and the line that
// does it.
func pathAdvice(dir string) (rcFile, line string) {
	export := fmt.Sprintf(`export PATH="%s:$PATH"`, dir)
	home := os.Getenv("HOME")

	switch shellName() {
	case "zsh":
		return filepath.Join(home, ".zshrc"), export
	case "bash":
		// Terminal.app and iTerm start login shells, which read
		// .bash_profile and never .bashrc.
		return filepath.Join(home, ".bash_profile"), export
	case "fish":
		// fish has a command for this, and would not even parse an export
		// line. Nothing here to append to.
		return "", fmt.Sprintf("fish_add_path %s", dir)
	}
	return "", export
}

// shellName is the user's shell, by the name of its binary.
func shellName() string { return filepath.Base(os.Getenv("SHELL")) }

// reloadCommand re-execs the user's shell so it re-reads what was just
// written.
func reloadCommand() string {
	switch name := shellName(); name {
	case "zsh", "bash":
		return "exec " + name
	}
	return "open a new terminal"
}

// hasLine reports whether body already contains want as a line of its own,
// ignoring surrounding space. The idempotence check: a second install must
// not append a second copy.
func hasLine(body []byte, want string) bool {
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
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
	// The host's entire share of the proxy is this forward. Nothing listening
	// means Lima never published it, and every sandbox's traffic would go to a
	// closed port however healthy squid is on the other side. Waited for
	// rather than probed once, because the squid restart Ensure may just have
	// issued takes the forward down with it - see waitForPort.
	if !waitForPort(env.Cfg.ProxyPort, forwardDeadline) {
		return fmt.Errorf("nothing is listening on 127.0.0.1:%d - the %s port forward is not up, so no sandbox could reach the proxy. Check it with: limactl list",
			env.Cfg.ProxyPort, config.ProxyVM)
	}
	// The sandbox range is a second lima forward, published independently of
	// the base port's - so the base being up says nothing about it, and it is
	// the one the sandboxes actually dial. The first port stands in for the
	// block: they are one forwarding rule, live or not together.
	if !waitForPort(env.Cfg.SandboxPortMin(), forwardDeadline) {
		return fmt.Errorf("nothing is listening on 127.0.0.1:%d - the %s sandbox port range (%d-%d) is not forwarded, so no sandbox could reach the proxy. Check it with: limactl list",
			env.Cfg.SandboxPortMin(), config.ProxyVM, env.Cfg.SandboxPortMin(), env.Cfg.SandboxPortMax())
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
	env.Out.Say("missing dependencies: %s. Install them with:", strings.Join(tools, " "))
	env.Out.Detail("brew install %s", strings.Join(formulae, " "))
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

// preflight is everything install can decide before it changes anything: the
// dependencies it needs, a conflict on the proxy port, and the Keychain entry
// new VMs will want.
//
// The position is the whole point. All of this used to run after Ensure(),
// under the name preflightReport, where a warning about a missing Keychain
// entry arrived at the end of a multi-minute provision - and where a listener
// on the proxy port could not be read at all, because by then the healthy
// case has one.
func preflight(env *Env) error {
	if err := preflightDeps(env); err != nil {
		return err
	}
	if err := preflightProxyPort(env); err != nil {
		return err
	}
	preflightKeychain(env)
	return nil
}

// preflightProxyPort refuses to install over a foreign listener.
//
// With the proxy VM down, nothing of ptrbox's should hold its port, so
// whatever does is somebody else's - a leftover host squid from before the
// proxy moved into a VM, most likely. Left alone it does not announce itself:
// Lima's forward cannot bind, the sandboxes dial that port anyway, and their
// egress silently belongs to a process ptrbox knows nothing about. The
// symptom arrives much later as "the agent has no network", or worse, does
// not arrive at all.
//
// A running proxy is the same observation with the opposite meaning - that
// listener is ours - which is why this can only be asked before Ensure().
func preflightProxyPort(env *Env) error {
	if env.Proxy.Running() {
		return nil
	}
	// The whole block, not just the base port: the sandbox range carries each
	// VM's egress, so a foreign listener on any of it silently owns one
	// sandbox's network instead of all of them - smaller blast radius, same
	// failure class, harder to spot.
	for port := env.Cfg.ProxyPort; port <= env.Cfg.SandboxPortMax(); port++ {
		if portInUse(port) {
			return fmt.Errorf("something else is already listening on 127.0.0.1:%d, which is where a %s port forward has to go (ports %d-%d) - sandboxes would reach that instead of ptrbox's proxy. Find it with: lsof -nP -iTCP:%d -sTCP:LISTEN (a squid left running on the Mac from before the proxy moved into a VM is the usual answer), or set PTRBOX_PROXY_PORT to move the whole block",
				port, config.ProxyVM, env.Cfg.ProxyPort, env.Cfg.SandboxPortMax(), port)
		}
	}
	return nil
}

// preflightKeychain reports a missing token source. A warning rather than an
// error: a VM without a token is still a usable VM, and `ptrbox new` says the
// same thing again at the point where it matters. Said here so that it is
// said before the provisioning, not after it.
func preflightKeychain(env *Env) {
	if !env.Keychain.Available() {
		env.Out.Warn("no macOS Keychain (`security`) - VMs will need CLAUDE_CODE_OAUTH_TOKEN set by hand")
		return
	}
	if env.Keychain.Token(env.Cfg.KeychainService) == "" {
		env.Out.Warn("no Keychain entry %q - new VMs will be unauthenticated. Create one with:",
			env.Cfg.KeychainService)
		env.Out.Detail("claude setup-token")
		env.Out.Detail("security add-generic-password -a \"$USER\" -s %s -w", env.Cfg.KeychainService)
	}
}
