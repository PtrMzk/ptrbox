package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
)

// HostOS is what differs between operating systems on the host side and is
// not a path (config.Platform) or a VM backend (backend.Facts): how a
// directory gets onto PATH, what installs a missing dependency, which editor
// is there when none is configured.
//
// A value swapped at run time, for config.Platform's reason: the Windows
// behaviour is ordinary code the suite runs on Linux, rather than code first
// executed on the machine it is for.
type HostOS struct {
	Name string

	// Symlinks: install may offer to link the binary into BIN_DIR. Not on
	// Windows, where creating a symlink takes Developer Mode or an elevated
	// shell - and ptrbox never elevates.
	Symlinks bool

	// FallbackEditor is what opens a file when neither $VISUAL nor $EDITOR
	// says.
	FallbackEditor string

	// GitPackage is the name git goes by in this platform's package manager,
	// and InstallAdvice the commands that install a list of packages. ptrbox
	// prints them and never runs them.
	GitPackage    string
	InstallAdvice func(packages []string) []string

	// PathAdvice is how a directory gets onto PATH: the startup file to
	// append to - empty when there is none, or none ptrbox should guess at -
	// and the line that does it. ReloadAdvice is what makes it take effect.
	PathAdvice   func(dir string) (rcFile, line string)
	ReloadAdvice func() string

	// SamePath compares two PATH entries the way the platform resolves them.
	SamePath func(a, b string) bool
}

var unixHost = HostOS{
	Name:           "unix",
	Symlinks:       true,
	FallbackEditor: "vi",
	GitPackage:     "git",
	InstallAdvice: func(packages []string) []string {
		return []string{"brew install " + strings.Join(packages, " ")}
	},
	PathAdvice:   unixPathAdvice,
	ReloadAdvice: unixReloadAdvice,
	SamePath:     func(a, b string) bool { return a == b },
}

var windowsHost = HostOS{
	Name:           "windows",
	Symlinks:       false,
	FallbackEditor: "notepad",
	GitPackage:     "Git.Git",
	// One line per package: winget takes several ids in one invocation only
	// in recent releases, and a line that fails on an older one is worse
	// than two lines.
	InstallAdvice: func(packages []string) []string {
		lines := make([]string, 0, len(packages))
		for _, id := range packages {
			lines = append(lines, "winget install --exact --id "+id)
		}
		return lines
	},
	// No startup file: PATH on Windows is a per-user environment variable.
	// Read back from the User scope rather than taken from $env:Path, which
	// is the machine's and the user's merged - writing that to the User scope
	// would copy every machine entry into it. And not `setx`, which
	// truncates at 1024 characters, silently.
	PathAdvice: func(dir string) (string, string) {
		return "", fmt.Sprintf(`[Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";%s", "User")`, dir)
	},
	ReloadAdvice: func() string { return "open a new terminal" },
	// NTFS is case-insensitive and PATH entries are often written with a
	// trailing separator.
	SamePath: func(a, b string) bool {
		return strings.EqualFold(strings.TrimRight(a, `\/`), strings.TrimRight(b, `\/`))
	},
}

// hostOS is the operating system ptrbox is running on. A package variable
// like lookPath and portInUse, so a test can describe another machine.
var hostOS = pickHostOS(runtime.GOOS)

func pickHostOS(goos string) HostOS {
	if goos == "windows" {
		return windowsHost
	}
	return unixHost
}

// unixPathAdvice is how the user's shell puts a directory on PATH: the
// startup file to append to - empty when ptrbox should not guess - and the
// line that does it.
func unixPathAdvice(dir string) (rcFile, line string) {
	export := fmt.Sprintf(`export PATH="%s:$PATH"`, dir)
	home := config.Host.Home()

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

// unixReloadAdvice re-execs the user's shell so it re-reads what was just
// written.
func unixReloadAdvice() string {
	switch name := shellName(); name {
	case "zsh", "bash":
		return "exec " + name
	}
	return "open a new terminal"
}
