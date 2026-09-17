package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Path is where the config file lives: $PTRBOX_CONFIG, else ptrbox/config
// under the platform's config directory - $XDG_CONFIG_HOME or ~/.config on
// macOS and Linux, %APPDATA% on Windows.
func Path() string {
	if p := os.Getenv("PTRBOX_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Host.ConfigBase(), "ptrbox", "config")
}

// Dir is the directory holding the config file, and with it everything else
// ptrbox keeps host-side: the live allowlist and the install manifest.
func Dir() string { return filepath.Dir(Path()) }

// AllowlistPath is the host-side template allowlist: what a NEW sandbox may
// reach, copied into its own file at create time. Nothing live follows it
// except the proxy's base port (the Mac's own way in, and what install
// verifies) - editing it needs no reload and changes no existing VM.
func AllowlistPath() string { return filepath.Join(Dir(), "allowed_domains.txt") }

// VMAllowlistDir holds the per-VM allowlists. Beside the config file and
// never in a repo, for the same reason as VMDir: the repo is mounted into the
// sandbox, and an allowlist living there would let the agent grant its own
// egress.
func VMAllowlistDir() string { return filepath.Join(Dir(), "allowed_domains.d") }

// VMAllowlistPath is one sandbox's complete egress allowlist - what that VM
// may CONNECT to, with nothing layered behind it. It survives `ptrbox rm` so
// a re-create keeps its list; deleting it resets the VM to the template on
// the next sync. The ".txt" suffix keeps the same charset argument as
// vms/README.txt: no legal VM name contains a dot, so no two VMs can collide
// here even on a case-folding filesystem.
func VMAllowlistPath(name string) string {
	return filepath.Join(VMAllowlistDir(), name+".txt")
}

// VMDir holds the per-VM config files, one optional file per sandbox named
// for its VM. Next to the main config rather than under the repo root, and
// never inside a repo: see internal/config/overlay.go.
func VMDir() string { return filepath.Join(Dir(), "vms") }

// VMConfigPath is the per-VM config file for one sandbox. Callers reaching
// the filesystem with a name that did not come from VMName should go through
// Config.Overlay, which checks it.
func VMConfigPath(name string) string { return filepath.Join(VMDir(), name) }

// GeneratedDir is where rendered VM configs are written: lima's own directory
// on macOS and Linux, ptrbox's under %LOCALAPPDATA% on Windows. See Platform.
func GeneratedDir() string { return Host.GeneratedDir() }

// GeneratedConfig is the rendered Lima config for one VM.
func GeneratedConfig(name string) string {
	return filepath.Join(GeneratedDir(), name+".yaml")
}

// RepoFile is the sidecar recording which host directory a VM mounts: the
// VM-to-repo mapping, written by `ptrbox new` beside the rendered config and
// removed with it. One line, the absolute path.
func RepoFile(name string) string {
	return filepath.Join(GeneratedDir(), name+".repo")
}

// SSHConfigLink is the symlink into ~/.ssh/config.d that makes `ssh lima-<vm>`
// work.
func SSHConfigLink(name string) string {
	return filepath.Join(Host.Home(), ".ssh", "config.d", "lima-"+name)
}

// RecordManifest appends a line to the install manifest: what ptrbox wrote
// outside its own checkout, so a future `ptrbox uninstall` does not have to
// guess.
func RecordManifest(line string) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(Dir(), "install-manifest"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// StateDir is where ptrbox keeps things it produced rather than things you
// configured: $XDG_STATE_HOME/ptrbox, else ~/.local/state/ptrbox; on Windows,
// %LOCALAPPDATA%\ptrbox\state.
func StateDir() string { return Host.StateDir() }

// TranscriptDir holds Claude transcripts pulled out of VMs before they are
// destroyed. Mode 0700 throughout: a transcript records everything the agent
// was shown, which is a superset of what is in the repo.
func TranscriptDir() string { return filepath.Join(StateDir(), "transcripts") }
