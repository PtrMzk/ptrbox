// Package proxy manages the shared egress proxy VM.
//
// Squid runs inside a dedicated Lima VM ("ptrbox-proxy"), not on the Mac:
// squid parses attacker-influenceable bytes from every sandbox, so a parsing
// exploit should land in a VM with no mounts and no credentials instead of on
// the host. The Mac keeps exactly one piece of the proxy: a 127.0.0.1 port
// forward to the VM. The sandboxes are unchanged - they still dial the host at
// PROXY_HOST:PROXY_PORT; Lima's usernet relay hands that to 127.0.0.1, where
// the forward picks it up.
//
// The proxy VM is cattle. Everything it serves lives host-side - the config
// template embedded in this binary, the template allowlist and the per-VM
// allowlists next to ptrbox's config file - and is pushed in on every sync,
// so `limactl delete ptrbox-proxy` is always recoverable.
//
// Lifecycle is coupled to the sandboxes: `new`/`start` bring the proxy up,
// `rm`/`stop` shut it down once no sandbox VM is left running. Every decision
// here errs toward the proxy LINGERING: a proxy that is down while a sandbox
// is up bricks the agent's network; the reverse costs a few hundred MB of idle
// RAM. Hence no background janitor and no in-VM idle self-shutdown (an agent
// compiling for 40 minutes is idle at the proxy but not inactive).
package proxy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/render"
	"github.com/PtrMzk/ptrbox/internal/ui"
)

// Paths inside the proxy VM.
const (
	ConfPath      = "/etc/squid/squid.conf"
	AllowlistPath = "/etc/squid/allowed_domains.txt"
	candidatePath = ConfPath + ".ptrbox-new"
)

// Proxy is the egress proxy VM and everything ptrbox pushes into it.
type Proxy struct {
	Cfg    *config.Config
	Lima   *lima.Client
	Assets fs.FS
	Out    ui.Printer
}

// Name is the proxy VM's Lima name.
func (p *Proxy) Name() string { return config.ProxyVM }

// Running reports whether the proxy VM is up.
func (p *Proxy) Running() bool { return p.Lima.Running(config.ProxyVM) }

// sudo runs a command inside the proxy VM as root. Root on purpose:
// everything ptrbox manages there (squid config, allowlist, log) is
// root-owned, and unlike the sandboxes the proxy VM keeps sudo - no untrusted
// code ever executes in it.
func (p *Proxy) sudo(args ...string) (string, error) {
	return p.Lima.Output(lima.ShellArgs(config.ProxyVM, append([]string{"sudo"}, args...)...)...)
}

// read returns a file from the proxy VM, or "" if it is not there.
func (p *Proxy) read(path string) string {
	out, _ := p.readOK(path)
	return out
}

// readOK additionally says whether the file exists - rollback needs the
// difference between "was empty" (restore empty) and "was absent" (remove).
func (p *Proxy) readOK(path string) (string, bool) {
	out, err := p.sudo("cat", path)
	if err != nil {
		return "", false
	}
	return out, true
}

// write puts content into a file in the proxy VM. tee, not a redirect: a
// redirect would be evaluated on the host.
func (p *Proxy) write(path, content string) error {
	return p.Lima.Send(strings.NewReader(content),
		lima.ShellArgs(config.ProxyVM, "sudo", "tee", path)...)
}

// --- the host-side allowlist -------------------------------------------------

// SeedAllowlist installs the shipped template allowlist if the user has none
// yet, and reports whether it created one. An existing template is never
// overwritten: it is the user's statement of what a new sandbox starts with.
func (p *Proxy) SeedAllowlist() (bool, error) {
	target := config.AllowlistPath()
	if _, err := os.Stat(target); err == nil {
		return false, nil
	}
	seed, err := fs.ReadFile(p.Assets, "host/allowed_domains.txt")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(target, seed, 0o644); err != nil {
		return false, err
	}
	p.Out.Say("installed %s", target)
	if err := config.RecordManifest("wrote " + target); err != nil {
		return true, err
	}
	return true, nil
}

// --- syncing -----------------------------------------------------------------

// SyncResult says what a Sync did.
type SyncResult int

const (
	// Unchanged: the VM already serves exactly this configuration.
	Unchanged SyncResult = iota
	// Applied: something changed and squid picked it up.
	Applied
	// Rejected: squid refused it; the VM was restored to its previous state.
	Rejected
)

// Sync pushes the rendered squid config and the host allowlist into the proxy
// VM, validating before activating and rolling the VM back if squid refuses
// the result. The proxy VM must be running.
//
// A rejection is a result, not an error: `ptrbox allow` has host-side cleanup
// of its own to do before it decides what to tell the user.
func (p *Proxy) Sync() (SyncResult, error) {
	hostAllowPath := config.AllowlistPath()
	hostAllow, err := os.ReadFile(hostAllowPath)
	if err != nil {
		return Unchanged, fmt.Errorf("no allowlist at %s - run 'ptrbox install' first", hostAllowPath)
	}

	// Kept next to the generated VM configs rather than in a tmpdir: what the
	// proxy is meant to be serving is worth being able to look at.
	renderedPath := filepath.Join(config.GeneratedDir(), config.ProxyVM+".squid.conf")
	if err := os.MkdirAll(config.GeneratedDir(), 0o755); err != nil {
		return Unchanged, err
	}
	err = render.RenderFile(renderedPath, p.Assets, "host/squid.conf.in", "host", render.Values{
		"PROXY_PORT":         fmt.Sprint(p.Cfg.ProxyPort),
		"SANDBOX_HTTP_PORTS": sandboxHTTPPorts(p.Cfg),
	})
	if err != nil {
		return Unchanged, err
	}
	rendered, err := os.ReadFile(renderedPath)
	if err != nil {
		return Unchanged, err
	}

	// The per-VM half: every allocated port gets its host-side list (seeded
	// from the template if the file is gone - a deleted file is a reset, not
	// a parse error) and its generated access rules.
	ports, err := PortAllocations()
	if err != nil {
		return Unchanged, err
	}
	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)

	// Everything squid serves besides the config itself, in push order: the
	// config references all of it, so no parse can succeed until it is in
	// place.
	desired := []struct{ path, content string }{
		{AllowlistPath, string(hostAllow)},
	}
	for _, name := range names {
		seed, err := p.vmSeed(name, hostAllow)
		if err != nil {
			return Unchanged, err
		}
		body, err := p.ensureVMAllowlist(name, seed)
		if err != nil {
			return Unchanged, err
		}
		desired = append(desired, struct{ path, content string }{vmAllowlistVMPath(name), body})
	}
	desired = append(desired, struct{ path, content string }{VMAccessPath, VMAccessConf(ports)})

	type pushed struct {
		path, content string
		prev          string
		existed       bool
	}
	var pushes []pushed
	for _, f := range desired {
		current, ok := p.readOK(f.path)
		if ok && current == f.content {
			continue
		}
		pushes = append(pushes, pushed{f.path, f.content, current, ok})
	}
	changedConf := p.read(ConfPath) != string(rendered)
	if !changedConf && len(pushes) == 0 {
		return Unchanged, nil
	}

	// rollback puts every pushed file back the way it was after a failed
	// parse, so a later squid restart in the VM cannot trip over files we
	// knew were bad.
	rollback := func() error {
		var firstErr error
		for _, f := range pushes {
			var err error
			if f.existed {
				err = p.write(f.path, f.prev)
			} else {
				_, err = p.sudo("rm", "-f", f.path)
			}
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}

	if len(pushes) > 0 {
		if _, err := p.sudo("mkdir", "-p", vmAllowlistDir); err != nil {
			return Unchanged, err
		}
		for _, f := range pushes {
			if err := p.write(f.path, f.content); err != nil {
				return Unchanged, err
			}
		}
	}

	if changedConf {
		// Validate the CANDIDATE before it goes anywhere near the live path -
		// this proxy is every sandbox's only way out.
		if err := p.write(candidatePath, string(rendered)); err != nil {
			return Unchanged, err
		}
		if _, err := p.sudo("squid", "-f", candidatePath, "-k", "parse"); err != nil {
			p.Out.Raw(err.Error())
			if _, rmErr := p.sudo("rm", "-f", candidatePath); rmErr != nil {
				return Rejected, rmErr
			}
			return Rejected, rollback()
		}
		if _, err := p.sudo("mv", candidatePath, ConfPath); err != nil {
			return Applied, err
		}
		// A config change needs a real restart; reconfigure is only
		// guaranteed for ACL-level changes. Live tunnels drop, but this path
		// only runs on a template version bump (or first boot, where squid
		// still serves the distro's stock config): sandbox churn changes only
		// the files above, never the config, which is the point of the
		// vm_access include.
		if _, err := p.sudo("systemctl", "restart", "squid"); err != nil {
			return Applied, err
		}
		return Applied, nil
	}

	// Supporting files only - an allowlist edit or sandbox churn: validate
	// the live config against them, then reload without dropping the
	// listener or any live tunnel.
	if _, err := p.sudo("squid", "-k", "parse"); err != nil {
		p.Out.Raw(err.Error())
		return Rejected, rollback()
	}
	if _, err := p.sudo("squid", "-k", "reconfigure"); err != nil {
		return Applied, err
	}
	return Applied, nil
}

// --- verification ------------------------------------------------------------

// Verify runs vm/verify-proxy.sh inside the proxy VM and fails if any of its
// assertions do: squid running, squid listening, an allowlisted domain
// tunnelling, a domain that is not on the list refused.
//
// Sync proves that squid PARSED a config, which is a different claim: a proxy
// can parse its config and then die on start, and until this ran that state
// was indistinguishable from a working install. The output is passed through
// rather than captured - a list of OK/FAIL lines is the report, and the same
// reasoning applies here as to the sandbox verification.
func (p *Proxy) Verify() error {
	script, err := fs.ReadFile(p.Assets, "vm/verify-proxy.sh")
	if err != nil {
		return err
	}
	err = p.Lima.Passthrough(lima.ShellArgs(config.ProxyVM, "bash", "-lc", string(script))...)
	if err != nil {
		return fmt.Errorf("the proxy VM is not serving egress - sandboxes created now would have no network. Look at it with: limactl shell %s -- sudo systemctl status squid",
			config.ProxyVM)
	}
	return nil
}

// --- lifecycle ---------------------------------------------------------------

// Ensure makes sure the proxy VM exists, is running, and serves the current
// config and allowlist. It reports whether anything had to be done, which is
// what lets `ptrbox install` say "nothing to do" and mean it.
func (p *Proxy) Ensure() (changed bool, err error) {
	seeded, err := p.SeedAllowlist()
	if err != nil {
		return false, err
	}
	changed = seeded

	if err := os.MkdirAll(config.GeneratedDir(), 0o755); err != nil {
		return changed, err
	}
	configPath := config.GeneratedConfig(config.ProxyVM)
	err = render.RenderFile(configPath, p.Assets, "vm/proxy.yaml", "vm", render.Values{
		"IMAGE_URL":        p.Cfg.ImageURL,
		"PROXY_CPUS":       fmt.Sprint(p.Cfg.ProxyCPUs),
		"PROXY_MEMORY":     p.Cfg.ProxyMemory,
		"PROXY_DISK":       p.Cfg.ProxyDisk,
		"PROXY_PORT":       fmt.Sprint(p.Cfg.ProxyPort),
		"SANDBOX_PORT_MIN": fmt.Sprint(p.Cfg.SandboxPortMin()),
		"SANDBOX_PORT_MAX": fmt.Sprint(p.Cfg.SandboxPortMax()),
	})
	if err != nil {
		return changed, err
	}

	switch p.Lima.Status(config.ProxyVM) {
	case lima.StatusRunning:
		// Already up; only the sync below can still have work to do.
	case "":
		if err := p.Lima.Validate(configPath); err != nil {
			return changed, err
		}
		p.Out.Say("provisioning the proxy VM (the first run takes a few minutes)")
		if err := p.Lima.Create(config.ProxyVM, configPath); err != nil {
			return changed, err
		}
		changed = true
	default:
		p.Out.Say("starting the proxy VM")
		if err := p.Lima.Start(config.ProxyVM); err != nil {
			return changed, err
		}
		changed = true
	}

	result, err := p.Sync()
	if err != nil {
		return changed, err
	}
	switch result {
	case Applied:
		changed = true
	case Rejected:
		return changed, fmt.Errorf(
			"squid rejected the proxy configuration; the proxy VM was rolled back. Check %s",
			config.AllowlistPath())
	}
	return changed, nil
}

// StopIfIdle stops the proxy once no sandbox VM is left running.
//
// A ptrbox VM is one with a rendered config in the generated dir; the proxy
// itself is excluded from the count. Anything uncertain - listing fails,
// states unreadable - means LEAVE THE PROXY UP: stopping it under a live
// sandbox bricks the agent's network, while lingering costs idle RAM.
func (p *Proxy) StopIfIdle() error {
	if !p.Running() {
		return nil
	}

	vms := p.Lima.List()
	if len(vms) == 0 {
		return nil
	}
	running := map[string]bool{}
	for _, vm := range vms {
		if vm.Status == lima.StatusRunning {
			running[vm.Name] = true
		}
	}

	// A generated dir that is not there means no ptrbox VM was ever rendered,
	// which is certainty about zero sandboxes rather than uncertainty. Any
	// other read failure is uncertainty, and leaves the proxy up.
	entries, err := os.ReadDir(config.GeneratedDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	for _, entry := range entries {
		name, isConfig := strings.CutSuffix(entry.Name(), ".yaml")
		if !isConfig || name == config.ProxyVM {
			continue
		}
		if running[name] {
			return nil
		}
	}

	if err := p.Lima.Stop(config.ProxyVM); err != nil {
		return err
	}
	p.Out.Say("no sandboxes left running - stopped the proxy VM")
	return nil
}
