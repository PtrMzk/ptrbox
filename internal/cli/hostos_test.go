package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/backend"
)

// onWindowsHost makes the harness's machine a PC: Windows' host behaviour,
// and a backend that writes no ssh config, which is the only kind a PC has.
// The paths stay the harness's - where files live is config.Platform's
// subject and is tested there.
func onWindowsHost(t *testing.T, h *harness) {
	t.Helper()
	real := hostOS
	hostOS = windowsHost
	t.Cleanup(func() { hostOS = real })
	h.facts = func(f *backend.Facts) { f.HasSSHConfigLink = false }
}

func TestTheHostOSIsChosenByTheOperatingSystem(t *testing.T) {
	for goos, want := range map[string]string{"windows": "windows", "darwin": "unix", "linux": "unix"} {
		if got := pickHostOS(goos).Name; got != want {
			t.Errorf("pickHostOS(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestInstallOnWindowsLinksNothingAndEditsNoStartupFile(t *testing.T) {
	h := newHarness(t)
	onWindowsHost(t, h)
	t.Setenv("SHELL", "/bin/zsh") // a Unix habit that must not be consulted
	h.tty, h.stdin = true, "y\ny\ny\n"

	h.mustRun("install")

	if h.manifestLinks() != 0 {
		t.Error("install made a symlink on a platform where that takes elevation")
	}
	for _, path := range []string{
		filepath.Join(h.home, "bin", "ptrbox"),
		filepath.Join(h.home, ".zshrc"),
		filepath.Join(h.home, ".ssh", "config"),
		filepath.Join(h.home, ".ssh", "config.d"),
	} {
		if h.exists(path) {
			t.Errorf("install created %s", path)
		}
	}
	if strings.Contains(h.output(), "and ssh") || strings.Contains(h.output(), "Include") {
		t.Errorf("install talked about ssh with a backend that has none:\n%s", h.output())
	}

	// What it does instead: say the one line that puts the binary's own
	// directory on PATH, for the user to run.
	exeDir := filepath.Dir(h.exe)
	h.assertOutputContains(exeDir + " is not on your PATH")
	h.assertOutputContains(`[Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";` + exeDir + `", "User")`)
	h.assertOutputContains("then: open a new terminal")
	if strings.Contains(h.output(), "setx") || strings.Contains(h.output(), "export PATH") {
		t.Errorf("the wrong platform's PATH advice was printed:\n%s", h.output())
	}
}

func TestInstallOnWindowsIsQuietAboutAPathThatAlreadyWorks(t *testing.T) {
	h := newHarness(t)
	onWindowsHost(t, h)
	// As Windows would have it: another case, and a trailing separator.
	exeDir := filepath.Dir(h.exe)
	t.Setenv("PATH", strings.ToUpper(exeDir)+string(filepath.Separator)+string(os.PathListSeparator)+os.Getenv("PATH"))

	h.mustRun("install")
	h.assertOutputContains("ptrbox is on your PATH")
	if strings.Contains(h.output(), "SetEnvironmentVariable") {
		t.Errorf("PATH advice was printed for a directory already on it:\n%s", h.output())
	}
}

func TestMissingDependenciesOnWindowsAreWingetLines(t *testing.T) {
	h := newHarness(t)
	onWindowsHost(t, h)
	h.missing["limactl"] = true
	h.missing["git"] = true

	if err := h.run("install"); err == nil {
		t.Fatal("install succeeded with its dependencies missing")
	}
	// One line per package, and git by the id winget knows it by.
	h.assertOutputContains("winget install --exact --id lima")
	h.assertOutputContains("winget install --exact --id Git.Git")
	if strings.Contains(h.output(), "brew") {
		t.Errorf("Homebrew was suggested on Windows:\n%s", h.output())
	}
}

func TestPathAdviceNeverUsesSetx(t *testing.T) {
	// setx truncates the value at 1024 characters without saying so, and a
	// developer's PATH is longer than that more often than not.
	_, line := windowsHost.PathAdvice(`C:\Users\you\go\bin`)
	if strings.Contains(strings.ToLower(line), "setx") {
		t.Errorf("advice = %q", line)
	}
	// Read from the User scope, not $env:Path: that one is machine and user
	// merged, and writing it back would copy every machine entry.
	if !strings.Contains(line, `GetEnvironmentVariable("Path", "User")`) || strings.Contains(line, "$env:Path") {
		t.Errorf("advice = %q, want it to extend the User-scope value", line)
	}
}

func TestEachPlatformHasAnEditorToFallBackOn(t *testing.T) {
	if unixHost.FallbackEditor != "vi" || windowsHost.FallbackEditor != "notepad" {
		t.Errorf("fallback editors = %q, %q", unixHost.FallbackEditor, windowsHost.FallbackEditor)
	}
}
