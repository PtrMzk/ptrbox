package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Path is where the config file lives: $PTRBOX_CONFIG, else
// $XDG_CONFIG_HOME/ptrbox/config, else ~/.config/ptrbox/config.
func Path() string {
	if p := os.Getenv("PTRBOX_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "ptrbox", "config")
}

// Dir is the directory holding the config file, and with it everything else
// ptrbox keeps host-side: the live allowlist and the install manifest.
func Dir() string { return filepath.Dir(Path()) }

// AllowlistPath is the host-side egress allowlist: the user's living
// capability list, kept next to the config file so it survives proxy VM
// re-creation.
func AllowlistPath() string { return filepath.Join(Dir(), "allowed_domains.txt") }

// GeneratedDir is where rendered Lima configs are written. Lima's own
// directory rather than ptrbox's: `limactl start` is handed a path in it, and
// keeping the artifact next to the VM it describes is what makes a generated
// config findable when a VM misbehaves.
func GeneratedDir() string { return filepath.Join(os.Getenv("HOME"), ".lima", "_generated") }

// GeneratedConfig is the rendered Lima config for one VM.
func GeneratedConfig(name string) string {
	return filepath.Join(GeneratedDir(), name+".yaml")
}

// SSHConfigLink is the symlink into ~/.ssh/config.d that makes `ssh lima-<vm>`
// work.
func SSHConfigLink(name string) string {
	return filepath.Join(os.Getenv("HOME"), ".ssh", "config.d", "lima-"+name)
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
