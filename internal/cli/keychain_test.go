package cli

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func utf16le(s string) []byte {
	var blob []byte
	for _, unit := range utf16.Encode([]rune(s)) {
		blob = append(blob, byte(unit), byte(unit>>8))
	}
	return blob
}

func TestACredentialBlobIsReadInWhicheverEncodingWroteIt(t *testing.T) {
	const token = "sk-ant-oat01-EXAMPLE_token-0123456789"
	for _, tc := range []struct {
		name string
		blob []byte
	}{
		{"UTF-16LE, as cmdkey and the Credential Manager UI store it", utf16le(token)},
		{"UTF-16LE with the terminator some writers include", utf16le(token + "\x00")},
		{"UTF-8, as most libraries store it", []byte(token)},
		{"UTF-8 with a trailing newline", []byte(token + "\r\n")},
	} {
		if got := decodeCredentialBlob(tc.blob); got != token {
			t.Errorf("%s: decoded %q", tc.name, got)
		}
	}
	// Odd length cannot be UTF-16, whatever is in it: read as it stands.
	if got := decodeCredentialBlob([]byte("a\x00b")); got != "a\x00b" {
		t.Errorf("an odd-length blob was decoded as UTF-16: %q", got)
	}
	if got := decodeCredentialBlob(nil); got != "" {
		t.Errorf("an empty blob decoded to %q", got)
	}
}

func TestADecodedTokenNeverCarriesANulIntoTheProfile(t *testing.T) {
	// The token is written into a shell assignment. A NUL surviving the
	// decode would be a UTF-16 blob read as UTF-8: every second byte zero,
	// and an auth header that fails somewhere far from here.
	for _, blob := range [][]byte{utf16le("sk-ant-oat-EXAMPLE"), utf16le("sk-ant-oat-EXAMPLE\x00"), []byte("sk-ant-oat-EXAMPLE")} {
		if got := decodeCredentialBlob(blob); strings.ContainsRune(got, 0) {
			t.Errorf("decoded %q still contains a NUL", got)
		}
	}
}

func TestSetupAdviceNeverPutsASecretOnACommandLine(t *testing.T) {
	// Each store's advice must prompt. `security ... -w` with no value and
	// `cmdkey ... /pass` with no value both do; either one followed by a
	// value is a token in shell history.
	for _, store := range []Keychain{SecurityKeychain{}, CredentialManager{}} {
		advice := store.SetupAdvice("claude-sandbox-token")
		if len(advice) == 0 || advice[0] != "claude setup-token" {
			t.Errorf("%s: advice = %q, want it to start with how to get a token", store.Name(), advice)
		}
		last := advice[len(advice)-1]
		if !strings.Contains(last, "claude-sandbox-token") {
			t.Errorf("%s: %q does not name the service", store.Name(), last)
		}
		if !strings.HasSuffix(last, " -w") && !strings.HasSuffix(last, " /pass") {
			t.Errorf("%s: %q does not end in the flag that prompts", store.Name(), last)
		}
	}
}

func TestOffWindowsTheCredentialManagerIsSimplyNotThere(t *testing.T) {
	// Skipped on the PC, where the answer is yes.
	if _, isHost := HostKeychain().(CredentialManager); isHost {
		t.Skip("this machine has one")
	}
	store := CredentialManager{}
	if store.Available() || store.Token("claude-sandbox-token") != "" {
		t.Error("a Credential Manager answered on a machine that has none")
	}
}

func TestTheWarningsNameTheStoreTheyAreAbout(t *testing.T) {
	h := newHarness(t)
	h.keychain.like = CredentialManager{}
	h.keychain.token = ""
	h.mustRun("new", "demo")
	h.assertOutputContains(`no Windows Credential Manager entry "claude-sandbox-token"`)
	h.assertOutputContains("cmdkey /generic:claude-sandbox-token /user:token /pass")
	if strings.Contains(h.output(), "security add-generic-password") || strings.Contains(h.output(), "macOS") {
		t.Errorf("the Mac's advice was printed for another store:\n%s", h.output())
	}

	h = newHarness(t)
	h.keychain.like = CredentialManager{}
	h.keychain.token = "sk-ant-oat-EXAMPLE"
	h.mustRun("new", "demo")
	h.assertOutputContains("auth token injected from the Windows Credential Manager")
}
