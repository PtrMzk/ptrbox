package cli

// vm/verify.sh's credential section, run for real.
//
// The property: exactly one credential belongs in a sandbox - the OAuth token
// ptrbox injects into ~/.profile from the Keychain, and only after every other
// check passes. These cases are about a SECOND one arriving.
//
// The one that matters is ~/.claude/.credentials.json, which nothing in ptrbox
// creates: it is what Claude Code writes after an interactive login, and it
// holds a refresh token - longer-lived and higher-value than the injected
// access token. It is reachable because ~/.claude is agent-writable and the
// allowlist grants the OAuth domains, so `claude /login` inside a VM completes
// over the proxy. Before this check nothing in the project would have noticed,
// and 40-userenv.sh is done-guarded so nothing would have cleaned it up.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// credGuest builds a fake guest home and returns the directory verify.sh will
// read as $HOME. profile is written verbatim to ~/.profile; when login is true
// a stored-login file is planted the way an interactive login would.
func credGuest(t *testing.T, profile string, login bool) (dir, state string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	dir = t.TempDir()
	state = filepath.Join(dir, "state")
	writeScript(t, filepath.Join(dir, "verify.sh"), asset(t, "vm/verify.sh"))

	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".profile"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	if login {
		// Shaped like the real thing, though only its existence is checked.
		body := `{"claudeAiOauth":{"accessToken":"EXAMPLE","refreshToken":"EXAMPLE"}}`
		if err := os.WriteFile(filepath.Join(dir, ".claude", ".credentials.json"),
			[]byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The rest of verify.sh is not what these cases are about, and a test host
	// is not a sandbox: without stubs the egress probes would spend half a
	// minute discovering they have no proxy. Shared rather than per-test; see
	// stubs_test.go for why that is worth doing.
	stubs := sharedStubs(t, "quiet", quietStubs)
	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", dir)
	return dir, state
}

// The steady state: the injected token and nothing else.
func TestAGuestWithOnlyTheInjectedTokenPasses(t *testing.T) {
	dir, state := credGuest(t, "export CLAUDE_CODE_OAUTH_TOKEN=\"EXAMPLE\"\n", false)

	if line := verifyLine(t, dir, state, "no stored login"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
	if line := verifyLine(t, dir, state, "one credential only"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
}

// A fresh create runs verify.sh BEFORE the token is injected, so an empty
// profile is the expected state at that moment, not a failure.
func TestAGuestBeforeTokenInjectionPasses(t *testing.T) {
	dir, state := credGuest(t, "# nothing yet\n", false)

	if line := verifyLine(t, dir, state, "one credential only"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK before injection", line)
	}
}

// The case this section exists for.
func TestAnInteractiveLoginIsCaught(t *testing.T) {
	dir, state := credGuest(t, "export CLAUDE_CODE_OAUTH_TOKEN=\"EXAMPLE\"\n", true)

	line := verifyLine(t, dir, state, "no stored login")
	if !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want a failure - a refresh token is in the VM", line)
	}
	if !strings.Contains(line, "refresh token") {
		t.Errorf("verify.sh = %q, want it to say what was found", line)
	}
}

// A second key-shaped export is either a credential ptrbox did not put there
// or one it put there twice. Both are worth a red line.
func TestASecondCredentialInTheProfileIsCaught(t *testing.T) {
	for name, profile := range map[string]string{
		"a foreign secret": "export CLAUDE_CODE_OAUTH_TOKEN=\"EXAMPLE\"\n" +
			"export AWS_SECRET_ACCESS_KEY=\"EXAMPLE\"\n",
		"a duplicated token": "export CLAUDE_CODE_OAUTH_TOKEN=\"EXAMPLE\"\n" +
			"export CLAUDE_CODE_OAUTH_TOKEN=\"EXAMPLE\"\n",
		"an api key": "export OPENAI_API_KEY=\"EXAMPLE\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir, state := credGuest(t, profile, false)
			line := verifyLine(t, dir, state, "one credential only")
			if !strings.Contains(line, "FAIL") {
				t.Errorf("verify.sh = %q, want a failure for %s", line, name)
			}
		})
	}
}
