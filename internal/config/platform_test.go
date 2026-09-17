package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// onWindows points Host at the Windows platform with a profile under a temp
// directory, the way a PC has it. Forward slashes in the expectations: the
// suite runs on Linux, and it is the layout that is being tested, not the
// separator filepath picks.
func onWindows(t *testing.T) (profile string) {
	t.Helper()
	profile = filepath.Join(t.TempDir(), "Users", "you")
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "PTRBOX_CONFIG"} {
		unset(t, key)
	}
	t.Setenv("USERPROFILE", profile)
	t.Setenv("APPDATA", filepath.Join(profile, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(profile, "AppData", "Local"))

	real := Host
	Host = Windows
	t.Cleanup(func() { Host = real })
	return profile
}

func TestHostIsChosenByTheOperatingSystem(t *testing.T) {
	for goos, want := range map[string]string{"windows": "windows", "darwin": "unix", "linux": "unix"} {
		if got := hostPlatform(goos).Name; got != want {
			t.Errorf("hostPlatform(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestWindowsKeepsSettingsInAppDataAndProductsInLocalAppData(t *testing.T) {
	profile := onWindows(t)
	roaming := filepath.Join(profile, "AppData", "Roaming", "ptrbox")
	local := filepath.Join(profile, "AppData", "Local", "ptrbox")

	for _, tc := range []struct{ name, got, want string }{
		{"Path", Path(), filepath.Join(roaming, "config")},
		{"Dir", Dir(), roaming},
		{"AllowlistPath", AllowlistPath(), filepath.Join(roaming, "allowed_domains.txt")},
		{"VMAllowlistPath", VMAllowlistPath("demo"), filepath.Join(roaming, "allowed_domains.d", "demo.txt")},
		{"VMConfigPath", VMConfigPath("demo"), filepath.Join(roaming, "vms", "demo")},
		{"GeneratedDir", GeneratedDir(), filepath.Join(local, "generated")},
		{"GeneratedConfig", GeneratedConfig("demo"), filepath.Join(local, "generated", "demo.yaml")},
		{"StateDir", StateDir(), filepath.Join(local, "state")},
		{"TranscriptDir", TranscriptDir(), filepath.Join(local, "state", "transcripts")},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestWindowsFallsBackToTheProfileWhenTheVariablesAreMissing(t *testing.T) {
	profile := onWindows(t)
	unset(t, "APPDATA")
	unset(t, "LOCALAPPDATA")

	if got, want := Dir(), filepath.Join(profile, "AppData", "Roaming", "ptrbox"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
	if got, want := GeneratedDir(), filepath.Join(profile, "AppData", "Local", "ptrbox", "generated"); got != want {
		t.Errorf("GeneratedDir = %q, want %q", got, want)
	}
}

func TestPtrboxConfigOverridesThePlatformOnBoth(t *testing.T) {
	onWindows(t)
	override := filepath.Join(t.TempDir(), "elsewhere", "config")
	t.Setenv("PTRBOX_CONFIG", override)
	if Path() != override || Dir() != filepath.Dir(override) {
		t.Errorf("Path = %q, Dir = %q, want them under %q", Path(), Dir(), override)
	}
}

func TestTheDefaultsAndTildeFollowTheWindowsProfile(t *testing.T) {
	profile := onWindows(t)
	configPath := filepath.Join(t.TempDir(), "config")
	t.Setenv("PTRBOX_CONFIG", configPath)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	pinArch(t, "amd64")

	cfg := mustLoad(t)
	if want := filepath.Join(profile, "code"); cfg.RepoRoot != want {
		t.Errorf("RepoRoot = %q, want %q", cfg.RepoRoot, want)
	}

	writeConfig(t, configPath, "PTRBOX_REPO_ROOT=~/src\n")
	if got, want := mustLoad(t).RepoRoot, profile+"/src"; got != want {
		t.Errorf("RepoRoot from ~/src = %q, want %q", got, want)
	}
}

func TestUnixPathsAreWhatTheyAlwaysWere(t *testing.T) {
	home, _ := setup(t)
	unset(t, "PTRBOX_CONFIG")
	unset(t, "XDG_STATE_HOME")

	if got, want := Path(), filepath.Join(home, ".config", "ptrbox", "config"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
	if got, want := GeneratedDir(), filepath.Join(home, ".lima", "_generated"); got != want {
		t.Errorf("GeneratedDir = %q, want %q", got, want)
	}
	if got, want := StateDir(), filepath.Join(home, ".local", "state", "ptrbox"); got != want {
		t.Errorf("StateDir = %q, want %q", got, want)
	}
	if got, want := SSHConfigLink("demo"), filepath.Join(home, ".ssh", "config.d", "lima-demo"); got != want {
		t.Errorf("SSHConfigLink = %q, want %q", got, want)
	}
}

// Host is only the answer if it is the only one asked. A HOME read anywhere
// else on the host side is a path that is right on a Mac and empty on a PC -
// and empty joins into a relative path, which works, in the wrong place.
func TestNothingElseAsksTheEnvironmentWhereHomeIs(t *testing.T) {
	where := regexp.MustCompile(`Getenv\("(HOME|USERPROFILE|APPDATA|LOCALAPPDATA|XDG_[A-Z_]+)"\)|UserHomeDir\(|UserConfigDir\(`)
	var files []string
	for _, pattern := range []string{"*.go", "../cli/*.go", "../proxy/*.go", "../render/*.go", "../../cmd/ptrbox/*.go"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) < 20 {
		t.Fatalf("only %d source files found: the test is looking in the wrong place", len(files))
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "platform.go" {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if where.MatchString(line) {
				t.Errorf("%s:%d asks the environment directly - go through config.Host: %s", file, i+1, strings.TrimSpace(line))
			}
		}
	}
}
