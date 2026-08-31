package cli

// cmd_new - create a repo (if needed) and provision its sandbox VM.
//
// Sequence, and why it is this sequence:
//
//	repo dir + git init    a brand-new project is one command
//	neutralise git hooks   agent-written hooks must never run on the Mac
//	the plan, and two offers  what this VM will be, while it can still change
//	render + validate      fail before touching any VM state
//	proxy VM up            the sandbox's only way out, once its wall is up
//	boot 1 (open network)  installers need hosts that are not on the allowlist
//	reboot                 sandbox-firewall.service starts; the wall goes up
//	verify                 every security property, asserted
//	token                  injected only into a VM that passed verification
//
// The offers are third for a reason. Both settings a sandbox has are frozen
// into it at create time, so the only two moments they can be changed are
// before this command and after a `ptrbox rm` - and the first of those
// requires knowing that two files exist, in a directory, neither of which is
// there yet on a first create. Asking here is the difference between a
// decision and an archaeology exercise.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/proxy"
	"github.com/PtrMzk/ptrbox/internal/render"
)

const newHelp = `ptrbox new - create a repo (if needed) and provision its sandbox VM

  ptrbox new <repo-path | repo-name>

      --no-edit  do not offer the configuration and allowlist editors; take
                 the settings as they are
`

func cmdNew(env *Env, args []string) error {
	arg := ""
	noEdit := false
	for _, a := range args {
		switch {
		case a == "--no-edit":
			noEdit = true
		case a == "-h" || a == "--help":
			fmt.Fprint(env.Stdout, newHelp)
			return nil
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("new: unknown option %q", a)
		case arg != "":
			return fmt.Errorf("new: one repo at a time (got %q and %q)", arg, a)
		default:
			arg = a
		}
	}
	if arg == "" {
		return errors.New("usage: ptrbox new <repo-path | repo-name>")
	}
	if err := requireLima(env); err != nil {
		return err
	}

	// The five steps below are what the wait is made of. Numbered because
	// "provisioning" on its own could be the first of two things or the first
	// of ten, and this one is followed by several minutes of someone else's
	// log output.
	steps := env.Out.Plan(5)

	// --- repo ---------------------------------------------------------------
	steps.Next("preparing the repo and its VM config")
	repoDir, err := prepareRepoDir(env, env.Cfg.RepoDir(arg))
	if err != nil {
		return err
	}

	name, err := config.VMName(repoDir)
	if err != nil {
		return err
	}
	if name == config.ProxyVM {
		return fmt.Errorf("%q is reserved for the egress proxy VM - pick another repo name", name)
	}
	if env.Lima.Exists(name) {
		return fmt.Errorf("VM %q already exists. Enter it: ssh lima-%s   Remove it: ptrbox rm %s",
			name, name, name)
	}

	// --- per-VM overrides ---------------------------------------------------
	// The name is what selects them, so this is the earliest it can happen -
	// and it has to happen before anything below reads a setting. Every key a
	// per-VM file may set is consumed in this function and then frozen into
	// the generated config, which is why no other command re-resolves.
	if env.LoadVM != nil {
		if err := env.LoadVM(env, name); err != nil {
			return err
		}
	}

	// --- the plan, and the two offers ---------------------------------------
	// Everything below this point is expensive or irreversible; everything
	// above it is knowable now. This is the last moment either file can be
	// edited without a re-create.
	if err := reviewPlan(env, name, noEdit); err != nil {
		return err
	}

	// --- generate the VM config ---------------------------------------------
	if err := os.MkdirAll(config.GeneratedDir(), 0o755); err != nil {
		return err
	}

	// The VM's own proxy port: its identity at squid, baked into the firewall
	// ruleset below. Allocated before the render so the sidecar and the
	// rendered config can never disagree.
	proxyPort, err := proxy.AllocatePort(name)
	if err != nil {
		return err
	}

	configPath := config.GeneratedConfig(name)
	cfg := env.Cfg
	err = render.RenderFile(configPath, env.Assets, "vm/claude-repo.yaml", "vm", render.Values{
		"REPO_DIR":       repoDir,
		"VM_NAME":        name,
		"VM_COLOR":       config.VMColor(name),
		"IMAGE_URL":      cfg.ImageURL,
		"CPUS":           fmt.Sprint(cfg.CPUs),
		"MEMORY":         cfg.Memory,
		"DISK":           cfg.Disk,
		"PORT_MIN":       fmt.Sprint(cfg.PortMin),
		"PORT_MAX":       fmt.Sprint(cfg.PortMax),
		"PROXY_HOST":     config.ProxyHost,
		"PROXY_PORT":     fmt.Sprint(proxyPort),
		"DNS_LIST":       cfg.DNSList(),
		"DNS_NFT_SET":    cfg.DNSNftSet(),
		"EXTRA_PACKAGES": cfg.ExtraPackageList(),
		"TOOLCHAIN":      cfg.ToolchainList(),
		"NODE_VERSION":   cfg.NodeVersion,
		"PLAYWRIGHT":     fmt.Sprint(cfg.Playwright),
		"CLAUDE_MODEL":   cfg.ClaudeModel,
		"GIT_USER_NAME":  cfg.GitUserName,
		"GIT_USER_EMAIL": cfg.GitUserEmail,
	})
	if err != nil {
		return err
	}

	// Validate before touching any VM state.
	if err := env.Lima.Validate(configPath); err != nil {
		return err
	}

	// --- the shared egress proxy --------------------------------------------
	// Up-front, not lazily: from the post-provision reboot onward the proxy is
	// this VM's only way out, and vm/verify.sh needs it to prove that egress
	// works. Idempotent and cheap when the proxy is already running.
	steps.Next("bringing up the egress proxy")
	if _, err := env.Proxy.Ensure(); err != nil {
		return err
	}

	// --- boot 1: provisioning over an open network --------------------------
	steps.Next("provisioning %s (this takes a few minutes)", name)
	if err := env.Lima.Create(name, configPath); err != nil {
		return err
	}

	// --- reboot: the firewall clamps ----------------------------------------
	// sandbox-firewall.service is enabled but not started during
	// provisioning, because the installers need hosts that are deliberately
	// off the allowlist.
	steps.Next("rebooting to activate the egress firewall")
	if err := env.Lima.Stop(name); err != nil {
		return err
	}
	if err := env.Lima.Start(name); err != nil {
		return err
	}

	// --- ssh convenience ----------------------------------------------------
	if err := linkSSHConfig(name); err != nil {
		return err
	}

	// --- verification -------------------------------------------------------
	steps.Next("verifying sandbox properties")
	verify, err := fs.ReadFile(env.Assets, "vm/verify.sh")
	if err != nil {
		return err
	}
	if err := env.Lima.Passthrough(lima.ShellArgs(name, "bash", "-lc", string(verify))...); err != nil {
		return fmt.Errorf("verification FAILED for %q. Do not use this VM; remove it with: ptrbox rm %s",
			name, name)
	}

	// --- auth ---------------------------------------------------------------
	if err := injectToken(env, name); err != nil {
		return err
	}

	// The summary is where a per-VM file gets said out loud. Editing one
	// changes nothing until the VM is re-created, so create time is the only
	// moment the file and the VM are known to agree.
	lines := []string{
		fmt.Sprintf("ssh lima-%s", name),
		"cd /workspace && claude",
		"",
		fmt.Sprintf("distro   %s, %d CPUs, %s memory", env.Cfg.Distro, env.Cfg.CPUs, env.Cfg.Memory),
		fmt.Sprintf("repo     %s, mounted at /workspace", repoDir),
	}
	// Always shown, unlike the extras: which runtimes a sandbox has is now a
	// decision rather than a constant, and "none" is a valid answer somebody
	// will want confirmed.
	runtimes := env.Cfg.ToolchainList()
	if runtimes == "" {
		runtimes = "none (claude only)"
	}
	lines = append(lines, "runtime  "+runtimes)
	if packages := env.Cfg.ExtraPackageList(); packages != "" {
		lines = append(lines, "extra    "+packages)
	}
	if config.HasVMConfig(name) {
		lines = append(lines, "config   "+config.VMConfigPath(name))
	}
	// The VM's own egress: its port at the proxy and the one file that
	// decides what it may reach. Always shown - the list outlives the VM, so
	// this is where its existence is said out loud.
	lines = append(lines, fmt.Sprintf("egress   proxy port %d, allowlist %s", proxyPort, config.VMAllowlistPath(name)))
	lines = append(lines,
		"push     from the host; the VM has no credentials but the Claude token")
	env.Out.Summary(fmt.Sprintf("VM %q is ready", name), lines...)
	return nil
}

// reviewPlan says what this VM will be and offers the two files that decide
// it, configuration first: it is what chooses the runtimes, and the runtimes
// are what the allowlist seed is filtered by, so asking in the other order
// would show a list that the next answer could invalidate.
//
// Every path through here is a no-op without a terminal, which is what keeps
// scripted and tested runs exactly what they were.
func reviewPlan(env *Env, name string, noEdit bool) error {
	printPlan(env, name)
	if noEdit {
		return nil
	}

	if ask(env, fmt.Sprintf("edit the configuration for %q first?", name)) {
		if err := seedVMConfig(env, name); err != nil {
			return err
		}
		if err := env.Editor(config.VMConfigPath(name)); err != nil {
			return err
		}
		// Re-resolved rather than patched: a distro named here re-derives the
		// image URL, and the plan printed below has to be the one that will
		// be built, not the one that was offered.
		if env.LoadVM != nil {
			if err := env.LoadVM(env, name); err != nil {
				return err
			}
		}
		printPlan(env, name)
	}

	if ask(env, fmt.Sprintf("edit the egress allowlist for %q first?", name)) {
		// The template first: this is the one thing here that runs BEFORE
		// Proxy.Ensure(), which is what normally creates it, and `ptrbox new`
		// has never required `ptrbox install` to have been run. Without this,
		// accepting the offer on a fresh host fails with "run ptrbox install
		// first" - for a file the next step was about to write anyway.
		if _, err := env.Proxy.SeedAllowlist(); err != nil {
			return err
		}
		// Then the VM's own list, so there is a file to open - seeded with the
		// runtimes just settled above, so it holds the right groups.
		if _, err := env.Proxy.EnsureVMAllowlist(name); err != nil {
			return err
		}
		if err := env.Editor(config.VMAllowlistPath(name)); err != nil {
			return err
		}
	}
	return nil
}

// printPlan is what the VM will be, in the vocabulary the closing summary
// uses. The same lines twice - once as a plan, once as a record - so that
// "what did I ask for" and "what did I get" are comparable.
func printPlan(env *Env, name string) {
	cfg := env.Cfg
	runtimes := cfg.ToolchainList()
	if runtimes == "" {
		runtimes = "none (claude only)"
	}
	env.Out.Say("VM %q will be built with:", name)
	env.Out.Detail("distro   %s, %d CPUs, %s memory, %s disk",
		cfg.Distro, cfg.CPUs, cfg.Memory, cfg.Disk)
	env.Out.Detail("runtime  %s", runtimes)
	if packages := cfg.ExtraPackageList(); packages != "" {
		env.Out.Detail("extra    %s", packages)
	}
	settings := config.VMConfigPath(name)
	if !config.HasVMConfig(name) {
		settings += " (none yet)"
	}
	env.Out.Detail("config   %s", settings)
	env.Out.Detail("egress   %s", config.VMAllowlistPath(name))
}

// vmConfigHeader introduces a per-VM file seeded from the shipped example. The
// example is the whole key list on purpose - one document to learn rather than
// two - and this says which half of it applies here, at the top of the file
// somebody is about to read.
// vmConfigMarker is a line from that header, used to recognise a file this
// seeded. It is what tells a seeded per-VM file - which install may bring up
// to date - apart from a sparse hand-written one, where "state only what
// differs" is the documented style and inflating it would be rude.
const vmConfigMarker = "Only the keys marked [vm] below may appear in"

const vmConfigHeader = `# Per-VM configuration for %q. Only the keys marked [vm] below may appear in
# this file; the rest describe your Mac and belong in %s.
#
# Read once, when the VM is created. Editing it later takes effect on the next
# ptrbox rm %s && ptrbox new %s.

`

// seedVMConfig makes sure there is a per-VM file to open. Create what is
// absent, never touch what is present: an existing file holds settings
// somebody typed, and this is the command that opens an editor on it.
func seedVMConfig(env *Env, name string) error {
	path := config.VMConfigPath(name)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	example, err := fs.ReadFile(env.Assets, "config/ptrbox.conf.example")
	if err != nil {
		return err
	}
	body := fmt.Sprintf(vmConfigHeader, name, config.Path(), name, name) + string(example)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	env.Out.Say("created %s", path)
	return config.RecordManifest("wrote " + path)
}

// prepareRepoDir creates the repo if it is not there and neutralises git hooks
// on the host clone.
func prepareRepoDir(env *Env, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Physical path, not logical: Lima wants a real host path for the mount.
	repoDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	repoDir, err = filepath.Abs(repoDir)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		if err := git(env, repoDir, "init"); err != nil {
			return "", err
		}
	}

	// Neutralise git hooks on the HOST clone. The agent can write .git/hooks
	// through the mount, and hooks execute on the Mac when YOU run git there -
	// that is agent-authored code running outside the sandbox. Residual risk:
	// .git/config is itself agent-writable and repo config outranks global, so
	// this blocks the common case, not a targeted attack. See SECURITY.md.
	if err := git(env, repoDir, "config", "core.hooksPath", "/dev/null"); err != nil {
		return "", err
	}
	return repoDir, nil
}

func git(env *Env, dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout, cmd.Stderr = env.Out.W, env.Out.W
	return cmd.Run()
}

func linkSSHConfig(name string) error {
	link := config.SSHConfigLink(name)
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		return err
	}
	target := filepath.Join(os.Getenv("HOME"), ".lima", name, "ssh.config")
	// ln -sf: re-creating a VM must replace the old link, not fail on it.
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Symlink(target, link)
}

// injectToken moves the Claude token from the Keychain (encrypted at rest) to
// the guest's ~/.profile over stdin. Never as a CLI argument (ps and shell
// history see those) and never substituted into the generated YAML (that
// persists on disk).
//
// Deliberately called after verification: an unverified VM does not get
// credentials.
func injectToken(env *Env, name string) error {
	if !env.Keychain.Available() {
		env.Out.Warn("no macOS Keychain here - set CLAUDE_CODE_OAUTH_TOKEN in the VM yourself")
		return nil
	}
	token := env.Keychain.Token(env.Cfg.KeychainService)
	if token == "" {
		env.Out.Warn("no Keychain entry %q; create one with:", env.Cfg.KeychainService)
		env.Out.Detail("claude setup-token")
		env.Out.Detail("security add-generic-password -a \"$USER\" -s %s -w", env.Cfg.KeychainService)
		return nil
	}
	if strings.ContainsAny(token, "\"\\") {
		return errors.New("the Keychain token contains a quote or backslash; refusing to write a broken ~/.profile")
	}

	// Built by concatenation rather than %q: the token has already been
	// checked for the two characters that would break the assignment, and %q
	// would additionally re-encode anything non-ASCII in it.
	payload := "export CLAUDE_CODE_OAUTH_TOKEN=\"" + token + "\"\n"
	err := env.Lima.Send(strings.NewReader(payload), lima.ShellArgs(name, "bash", "-c",
		"grep -q CLAUDE_CODE_OAUTH_TOKEN ~/.profile || cat >> ~/.profile")...)
	if err != nil {
		return err
	}
	env.Out.Say("auth token injected from the Keychain")
	return nil
}
