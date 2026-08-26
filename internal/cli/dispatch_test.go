package cli

// Dispatch: the command table, the exit statuses, and the fact that a broken
// config file cannot stop ptrbox explaining itself.

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestHelpListsTheCommands(t *testing.T) {
	h := newHarness(t)
	h.mustRun("help")
	for _, want := range []string{"ptrbox new", "ptrbox logs", "start", "stop", "sync-proxy"} {
		if !strings.Contains(h.stdout, want) {
			t.Errorf("help does not mention %q", want)
		}
	}
}

func TestNoArgumentsPrintsHelpRatherThanFailing(t *testing.T) {
	h := newHarness(t)
	h.mustRun()
	if !strings.Contains(h.stdout, "USAGE") {
		t.Errorf("stdout = %q", h.stdout)
	}
}

func TestVersionPrintsAVersion(t *testing.T) {
	h := newHarness(t)
	h.mustRun("version")
	if !regexp.MustCompile(`^ptrbox [0-9]`).MatchString(h.stdout) {
		t.Errorf("stdout = %q", h.stdout)
	}
}

func TestAnUnknownCommandFailsWithUsageOnStderr(t *testing.T) {
	h := newHarness(t)
	err := h.run("frobnicate")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage (which main turns into exit 2)", err)
	}
	if !strings.Contains(h.stderr, "unknown command: frobnicate") {
		t.Errorf("stderr = %q", h.stderr)
	}
	if !strings.Contains(h.stderr, "USAGE") {
		t.Error("usage was not printed to stderr")
	}
}

func TestHelpAndVersionWorkWithAConfigFileThatDoesNotParse(t *testing.T) {
	// Configuration is resolved only for the commands that need it, so a
	// broken config file cannot stop ptrbox explaining how to fix it.
	h := newHarness(t)
	if err := os.WriteFile(os.Getenv("PTRBOX_CONFIG"), []byte("this is not a config file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.mustRun("help")
	h.mustRun("version")

	if err := h.run("install"); err == nil {
		t.Error("install ran with an unparseable config file")
	}
}

func TestConfigWarningsAreNotFatal(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(os.Getenv("PTRBOX_CONFIG"), []byte("PTRBOX_NOT_A_SETTING=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.mustRun("install")
}

func TestInstallHelpPrintsUsage(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install", "--help")
	if !strings.Contains(h.stdout, "--no-input") {
		t.Errorf("stdout = %q", h.stdout)
	}
	h.assertNotCalled("start")
}

func TestInstallRejectsAnUnknownOption(t *testing.T) {
	h := newHarness(t)
	err := h.run("install", "--reformat-disk")
	if err == nil || !strings.Contains(err.Error(), "unknown option") {
		t.Errorf("err = %v", err)
	}
}

func TestConfirmDeclinesWithoutATTY(t *testing.T) {
	h := newHarness(t)
	env := &Env{Out: h.printer(), Interactive: false}
	if confirm(env, "do the thing?") {
		t.Error("confirm said yes with no way to ask")
	}
}

func TestConfirmObeysAssumeYes(t *testing.T) {
	h := newHarness(t)
	env := &Env{Out: h.printer(), AssumeYes: true}
	if !confirm(env, "do the thing?") {
		t.Error("confirm ignored --yes")
	}
}

func TestConfirmReadsATTYAnswer(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		answer string
		want   bool
	}{{"y\n", true}, {"yes\n", true}, {"n\n", false}, {"\n", false}} {
		env := &Env{Out: h.printer(), Interactive: true, Stdin: strings.NewReader(tc.answer)}
		if got := confirm(env, "do the thing?"); got != tc.want {
			t.Errorf("confirm(%q) = %v, want %v", tc.answer, got, tc.want)
		}
	}
}
