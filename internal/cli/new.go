package cli

// cmd_new - create a repo (if needed) and provision its sandbox VM.
//
// Sequence, and why it is this sequence:
//
//	repo dir + git init    a brand-new project is one command
//	neutralise git hooks   agent-written hooks must never run on the Mac
//	render + validate      fail before touching any VM state
//	proxy VM up            the sandbox's only way out, once its wall is up
//	boot 1 (open network)  installers need hosts that are not on the allowlist
//	reboot                 sandbox-firewall.service starts; the wall goes up
//	verify                 every security property, asserted
//	token                  injected only into a VM that passed verification

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
	"github.com/PtrMzk/ptrbox/internal/render"
)

func cmdNew(env *Env, args []string) error {
	if len(args) == 0 || args[0] == "" {
		return errors.New("usage: ptrbox new <repo-path | repo-name>")
	}
	arg := args[0]
	if err := requireLima(env); err != nil {
		return err
	}

	// --- repo ---------------------------------------------------------------
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

	// --- generate the VM config ---------------------------------------------
	if err := os.MkdirAll(config.GeneratedDir(), 0o755); err != nil {
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
		"PROXY_HOST":     cfg.ProxyHost,
		"PROXY_PORT":     fmt.Sprint(cfg.ProxyPort),
		"DNS_LIST":       cfg.DNSList(),
		"DNS_NFT_SET":    cfg.DNSNftSet(),
		"EXTRA_PACKAGES": cfg.ExtraPackageList(),
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
	if _, err := env.Proxy.Ensure(); err != nil {
		return err
	}

	// --- boot 1: provisioning over an open network --------------------------
	env.Out.Say("provisioning %s (this takes a few minutes)", name)
	if err := env.Lima.Create(name, configPath); err != nil {
		return err
	}

	// --- reboot: the firewall clamps ----------------------------------------
	// sandbox-firewall.service is enabled but not started during
	// provisioning, because the installers need hosts that are deliberately
	// off the allowlist.
	env.Out.Say("rebooting to activate the egress firewall")
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
	env.Out.Say("verifying sandbox properties")
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

	fmt.Fprintf(env.Stdout, `
VM '%s' is ready.

  ssh lima-%s
  cd /workspace && claude

The repo lives on the host at %s and is mounted at /workspace.
Commit and push from the host: the VM has no credentials but the Claude token.
`, name, name, repoDir)
	return nil
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
		env.Out.Warn("  claude setup-token")
		env.Out.Warn("  security add-generic-password -a \"$USER\" -s %s -w", env.Cfg.KeychainService)
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
