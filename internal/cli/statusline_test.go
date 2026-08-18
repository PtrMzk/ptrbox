package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStatusline puts a script on the host and points the config at it.
func (h *harness) useStatusline(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(h.tmp, "statusline-command.sh")
	write(t, path, body)
	t.Setenv("PTRBOX_STATUSLINE", path)
	return path
}

const sampleStatusline = "#!/bin/sh\ninput=$(cat)\njq -r '.model.display_name' <<<\"$input\"\n"

func TestStatuslineIsPushedIntoTheVM(t *testing.T) {
	h := newHarness(t)
	h.useStatusline(t, sampleStatusline)
	h.mustRun("new", "demo")

	if !strings.Contains(strings.Join(h.fake.Stdins, ""), "model.display_name") {
		t.Errorf("the script did not travel to the guest: %q", h.fake.Stdins)
	}
	h.assertCalled(`shell demo -- bash -c`)
	h.assertOutputContains("statusline installed")
}

func TestStatuslinePointsClaudeSettingsAtIt(t *testing.T) {
	h := newHarness(t)
	h.useStatusline(t, sampleStatusline)
	h.mustRun("new", "demo")

	// jq does the edit in the guest so the model pre-seed survives and no
	// guest home path is hardcoded - Lima's home naming is version-dependent.
	script := h.scriptContaining(".statusLine =")
	for _, want := range []string{"$HOME/.claude/statusline-command.sh", "jq"} {
		if !strings.Contains(script, want) {
			t.Errorf("the settings edit does not mention %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "/home/") {
		t.Errorf("the guest home path is hardcoded:\n%s", script)
	}
}

// scriptContaining finds the one captured guest script matching a marker.
// Scoped on purpose: h.fake.Scripts also holds verify.sh, which legitimately
// talks about guest home paths.
func (h *harness) scriptContaining(marker string) string {
	h.t.Helper()
	for _, script := range h.fake.Scripts {
		if strings.Contains(script, marker) {
			return script
		}
	}
	h.t.Fatalf("no guest script mentions %q", marker)
	return ""
}

func TestNoStatuslineConfiguredPushesNothing(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	if strings.Contains(strings.Join(h.fake.Scripts, "\n"), "statusLine") {
		t.Error("a statusline was installed without one being configured")
	}
	if strings.Contains(h.output(), "statusline installed") {
		t.Error("ptrbox claimed to install a statusline it did not have")
	}
}

func TestABadStatuslinePathFailsBeforeAnyVMIsTouched(t *testing.T) {
	// A path typo should cost nothing, not surface after a VM has booted twice.
	h := newHarness(t)
	t.Setenv("PTRBOX_STATUSLINE", filepath.Join(h.tmp, "absent.sh"))

	if err := h.run("new", "demo"); err == nil {
		t.Fatal("new accepted a missing statusline path")
	}
	h.assertOutputContains("PTRBOX_STATUSLINE")
	h.assertNotCalled("start")
}

func TestAnEmptyStatuslineIsRefused(t *testing.T) {
	h := newHarness(t)
	h.useStatusline(t, "   \n")
	if err := h.run("new", "demo"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %v", err)
	}
	h.assertNotCalled("start")
}

func TestAStatuslineCarryingACredentialIsRefused(t *testing.T) {
	// This is the one mechanism that copies arbitrary host file content into a
	// VM, and the Claude token is the only credential a sandbox gets.
	for _, secret := range []string{
		"TOKEN=sk-ant-abcd1234efgh\n",
		"KEY=ghp_abcdefghij0123456789\n",
		"AWS=AKIAABCDEFGHIJKLMNOP\n",
		"-----BEGIN OPENSSH PRIVATE KEY-----\n",
	} {
		t.Run(strings.SplitN(secret, "=", 2)[0], func(t *testing.T) {
			h := newHarness(t)
			h.useStatusline(t, "#!/bin/sh\n"+secret)
			err := h.run("new", "demo")
			if err == nil || !strings.Contains(err.Error(), "credential") {
				t.Fatalf("err = %v", err)
			}
			h.assertNotCalled("start")
		})
	}
}

func TestStatuslinePathMayUseTilde(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.home, ".claude", "statusline-command.sh")
	write(t, path, sampleStatusline)
	t.Setenv("PTRBOX_STATUSLINE", "~/.claude/statusline-command.sh")

	h.mustRun("new", "demo")
	h.assertOutputContains("statusline installed")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestAnUnverifiedVMGetsNoStatusline(t *testing.T) {
	h := newHarness(t)
	h.useStatusline(t, sampleStatusline)
	h.fake.VerifyFails = true

	if err := h.run("new", "demo"); err == nil {
		t.Fatal("new succeeded with a failing verification")
	}
	if len(h.fake.Stdins) != 0 {
		t.Errorf("an unverified VM was sent %q", h.fake.Stdins)
	}
}
