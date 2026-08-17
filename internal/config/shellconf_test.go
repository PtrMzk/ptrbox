package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFileHandlesShellishSyntax(t *testing.T) {
	home, path := setup(t)
	t.Setenv("PTRBOX_TEST_SCRATCH", "from-env")

	writeConfig(t, path, strings.Join([]string{
		"# a comment",
		"",
		"   # an indented comment",
		"PTRBOX_CPUS=8            # trailing comment",
		`PTRBOX_MEMORY="16GiB"`,
		`PTRBOX_DNS_SERVERS="9.9.9.9 1.1.1.1"`,
		`export PTRBOX_CLAUDE_MODEL=opus`,
		`PTRBOX_REPO_ROOT="$HOME/src"`,
		`PTRBOX_BIN_DIR=~/tools`,
		`PTRBOX_KEYCHAIN_SERVICE='literal$HOME'`,
		"PTRBOX_DISK=${PTRBOX_TEST_SCRATCH}",
	}, "\n")+"\n")

	values, warnings, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	for _, tc := range []struct{ key, want string }{
		{"CPUS", "8"},
		{"MEMORY", "16GiB"},
		{"DNS_SERVERS", "9.9.9.9 1.1.1.1"},
		{"CLAUDE_MODEL", "opus"},
		{"REPO_ROOT", filepath.Join(home, "src")},
		{"BIN_DIR", filepath.Join(home, "tools")},
		// Single quotes are literal, as in shell.
		{"KEYCHAIN_SERVICE", "literal$HOME"},
		{"DISK", "from-env"},
	} {
		if values[tc.key] != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, values[tc.key], tc.want)
		}
	}
}

func TestParseFileWarnsAboutUnknownSettings(t *testing.T) {
	// A typo used to be silently sourced and then ignored. Naming it is the
	// whole benefit of parsing the file instead.
	_, path := setup(t)
	writeConfig(t, path, "PTRBOX_SQUID_PORT=8888\n")
	_, warnings, err := parseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "PTRBOX_SQUID_PORT") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestParseFileKeepsHelperVariablesForExpansion(t *testing.T) {
	// Not a setting, but usable by a later line - which is what it did when
	// the file was sourced.
	_, path := setup(t)
	writeConfig(t, path, "base=/opt/x\nPTRBOX_REPO_ROOT=$base/repos\n")
	values, warnings, err := parseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if values["REPO_ROOT"] != "/opt/x/repos" {
		t.Errorf("REPO_ROOT = %q", values["REPO_ROOT"])
	}
}

func TestParseFileRejectsWhatItCannotParse(t *testing.T) {
	// Sourcing would have RUN these. Parsing must refuse them loudly rather
	// than apply half a config file.
	for _, tc := range []struct{ name, line string }{
		{"a command", "rm -rf /\n"},
		{"an unterminated quote", `PTRBOX_MEMORY="16GiB` + "\n"},
		{"an unquoted value with spaces", "PTRBOX_DNS_SERVERS=9.9.9.9 1.1.1.1\n"},
		{"a command substitution", "PTRBOX_CPUS=$(nproc)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, path := setup(t)
			writeConfig(t, path, tc.line)
			if _, _, err := parseFile(path); err == nil {
				t.Errorf("parseFile accepted %q", tc.line)
			}
		})
	}
}

func TestParseFileTreatsAMissingFileAsNoConfig(t *testing.T) {
	setup(t)
	values, warnings, err := parseFile(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(values) != 0 || len(warnings) != 0 {
		t.Errorf("got %v, %v, %v; want an empty result and no error", values, warnings, err)
	}
}

func TestLoadSurfacesFileWarnings(t *testing.T) {
	_, path := setup(t)
	writeConfig(t, path, "PTRBOX_NOT_A_SETTING=1\n")
	cfg := mustLoad(t)
	if len(cfg.Warnings) != 1 {
		t.Errorf("Warnings = %v", cfg.Warnings)
	}
}

func TestLoadFailsOnAnUnparseableConfigFile(t *testing.T) {
	_, path := setup(t)
	writeConfig(t, path, "this is not a config file\n")
	loadErr(t, "not a KEY=value assignment")
}

// The shipped example config is the documentation for this parser; every line
// of it must survive being uncommented.
func TestExampleConfigParses(t *testing.T) {
	_, path := setup(t)
	example, err := os.ReadFile("../../config/ptrbox.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	var uncommented []string
	for _, line := range strings.Split(string(example), "\n") {
		if strings.HasPrefix(line, "#PTRBOX_") {
			uncommented = append(uncommented, strings.TrimPrefix(line, "#"))
		}
	}
	if len(uncommented) < 15 {
		t.Fatalf("only found %d settings in the example config", len(uncommented))
	}
	writeConfig(t, path, strings.Join(uncommented, "\n")+"\n")

	values, warnings, err := parseFile(path)
	if err != nil {
		t.Fatalf("the example config does not parse: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("the example config names settings that do not exist: %v", warnings)
	}
	if values["DISTRO"] != "debian13" || values["PROXY_PORT"] != "8888" {
		t.Errorf("example config parsed oddly: %v", values)
	}
}
