package cli

// vm/verify.sh's LM Studio check, run for real against a fake guest home.
//
// The check is gated on the toolchain record naming opencode, reads the URL
// 40-userenv.sh recorded, and asks whether it answers with the proxy bypassed
// - which through a real wall is the question "did the one extra rule load".
// The curl stub answers for exactly one URL, so what is exercised is the
// gating, the record, and the URL the script actually dials.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lmstudioGuest builds a guest home whose toolchain record does or does not
// name opencode, with or without the URL record, and puts a curl on PATH that
// succeeds only for LMSTUDIO_UP's URL.
func lmstudioGuest(t *testing.T, toolchain, url, up string) (dir, state string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	dir = t.TempDir()
	state = filepath.Join(dir, "state")
	writeScript(t, filepath.Join(dir, "verify.sh"), asset(t, "vm/verify.sh"))
	if err := os.MkdirAll(filepath.Join(dir, ".ptrbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ptrbox", "toolchain"), []byte(toolchain+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if url != "" {
		if err := os.WriteFile(filepath.Join(dir, ".ptrbox", "lmstudio-url"), []byte(url+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stubs := sharedStubs(t, "lmstudio", func(sd string) {
		quietStubs(sd)
		// Answers for LMSTUDIO_UP's /v1/models and nothing else - so the
		// egress probes still fail quietly, and a script that dialled the
		// wrong URL would be told so.
		mustWriteScript(filepath.Join(sd, "curl"), `#!/bin/bash
case "$*" in
*"$LMSTUDIO_UP/v1/models"*) [ -n "$LMSTUDIO_UP" ] && exit 0 ;;
esac
exit 1
`)
	})
	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", dir)
	t.Setenv("LMSTUDIO_UP", up)
	return dir, state
}

func TestAnOpencodeGuestWhoseLMStudioAnswersPasses(t *testing.T) {
	dir, state := lmstudioGuest(t, "node opencode", "http://192.168.5.2:1234", "http://192.168.5.2:1234")
	if line := verifyLine(t, dir, state, "lm studio reachable"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
}

// The case the check exists for: the wall did not open the port, or LM Studio
// is not running on the Mac. Either way the sandbox cannot do what it was
// created to do, and that is a failed create rather than a slow discovery.
func TestAnOpencodeGuestWhoseLMStudioDoesNotAnswerFails(t *testing.T) {
	dir, state := lmstudioGuest(t, "opencode", "http://192.168.5.2:1234", "")
	line := verifyLine(t, dir, state, "lm studio reachable")
	if !strings.Contains(line, "FAIL") || !strings.Contains(line, "192.168.5.2:1234") {
		t.Errorf("verify.sh = %q, want a FAIL naming the URL", line)
	}
}

// A different recorded port is a different URL to dial: the check follows the
// record, not a number baked into verify.sh.
func TestTheLMStudioCheckDialsTheRecordedURL(t *testing.T) {
	dir, state := lmstudioGuest(t, "opencode", "http://192.168.5.2:4321", "http://192.168.5.2:4321")
	if line := verifyLine(t, dir, state, "lm studio reachable"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
}

// No record is a failure, not a pass: without it a 40-userenv.sh that never
// ran would leave the check with nothing to ask and a VM that looks ready.
func TestAnOpencodeGuestWithNoURLRecordFails(t *testing.T) {
	dir, state := lmstudioGuest(t, "opencode", "", "http://192.168.5.2:1234")
	if line := verifyLine(t, dir, state, "lm studio reachable"); !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want FAIL for the missing record", line)
	}
}

// A sandbox without opencode has nothing to report on, and says nothing - the
// same silence as a VM without Playwright.
func TestAGuestWithoutOpencodeIsSilentAboutLMStudio(t *testing.T) {
	dir, state := lmstudioGuest(t, "node uv", "http://192.168.5.2:1234", "http://192.168.5.2:1234")
	if line := verifyLineIfAny(t, dir, state, "lm studio"); line != "" {
		t.Errorf("verify.sh reported on LM Studio in a VM without opencode: %q", line)
	}
}
