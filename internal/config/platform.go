package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// Platform is where a kind of machine keeps things: the home directory, and
// the three places ptrbox writes under it. It answers "where", never "how" -
// which VM backend runs here, which secret store, which editor are other
// packages' questions.
//
// It is a value chosen at run time rather than a pair of build-tagged files,
// and that is the point of it: the Windows paths are ordinary code that the
// suite exercises on Linux by setting Host, instead of code that compiles on
// one machine and is first executed on another.
type Platform struct {
	Name string

	// Home is the user's home directory.
	Home func() string
	// ConfigBase holds ptrbox/config, and beside it everything else that is
	// the user's to edit: the allowlists, the per-VM files.
	ConfigBase func() string
	// StateDir is what ptrbox produced rather than what you configured.
	StateDir func() string
	// GeneratedDir is where rendered VM configs are written. It doubles as
	// the registry of ptrbox's VMs: the proxy's idle check and the port
	// allocations both read it.
	GeneratedDir func() string
}

// Unix is macOS and Linux: XDG for ptrbox's own files, and lima's directory
// for the rendered configs - `limactl start` is handed a path in it, and
// keeping the artifact next to the VM it describes is what makes a generated
// config findable when a VM misbehaves.
var Unix = Platform{
	Name: "unix",
	Home: func() string { return os.Getenv("HOME") },
	ConfigBase: func() string {
		if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
			return base
		}
		return filepath.Join(os.Getenv("HOME"), ".config")
	},
	StateDir: func() string {
		if base := os.Getenv("XDG_STATE_HOME"); base != "" {
			return filepath.Join(base, "ptrbox")
		}
		return filepath.Join(os.Getenv("HOME"), ".local", "state", "ptrbox")
	},
	GeneratedDir: func() string { return filepath.Join(os.Getenv("HOME"), ".lima", "_generated") },
}

// Windows keeps what you edit under %APPDATA% (it roams with the profile,
// like the settings it is) and what ptrbox produced under %LOCALAPPDATA%
// (it describes VMs on this machine, and means nothing on another). The
// fallbacks are where Windows itself puts those two when the variables are
// missing, which a service account or a stripped environment can manage.
var Windows = Platform{
	Name:       "windows",
	Home:       func() string { return os.Getenv("USERPROFILE") },
	ConfigBase: func() string { return windowsDir("APPDATA", "Roaming") },
	StateDir: func() string {
		return filepath.Join(windowsDir("LOCALAPPDATA", "Local"), "ptrbox", "state")
	},
	GeneratedDir: func() string {
		return filepath.Join(windowsDir("LOCALAPPDATA", "Local"), "ptrbox", "generated")
	},
}

func windowsDir(variable, fallback string) string {
	if dir := os.Getenv(variable); dir != "" {
		return dir
	}
	return filepath.Join(os.Getenv("USERPROFILE"), "AppData", fallback)
}

// Host is the platform ptrbox is running on. Every path in this package goes
// through it.
var Host = hostPlatform(runtime.GOOS)

func hostPlatform(goos string) Platform {
	if goos == "windows" {
		return Windows
	}
	return Unix
}
