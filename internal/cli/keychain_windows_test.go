package cli

import (
	"os"
	"strings"
	"testing"
)

// The one test of credRead against the real thing. Gated, because it needs an
// entry a person made:
//
//	cmdkey /generic:ptrbox-credread-probe /user:token /pass:probe-VALUE-123
//	set PTRBOX_TEST_CREDENTIAL=1
//	go test ./internal/cli -run TestCredReadAgainstCmdkey -v
//	cmdkey /delete:ptrbox-credread-probe
//
// `make windows-capture` builds this package's tests as credread-probe.exe and
// `capture.cmd credential` runs those four lines around it, so the PC needs no
// Go toolchain.
//
// (A throwaway value on the command line is fine. A real token is not, which
// is why ptrbox's own advice uses the bare /pass that prompts.)
func TestCredReadAgainstCmdkey(t *testing.T) {
	if os.Getenv("PTRBOX_TEST_CREDENTIAL") == "" {
		t.Skip("set PTRBOX_TEST_CREDENTIAL=1 after creating the probe entry (see this file)")
	}
	store := CredentialManager{}
	if !store.Available() {
		t.Fatal("advapi32's CredReadW/CredFree could not be found")
	}

	blob, ok := credRead("ptrbox-credread-probe")
	if !ok {
		t.Fatal("no entry named ptrbox-credread-probe: create it with the cmdkey line in this file")
	}
	// Logged on purpose: the raw bytes are what says how cmdkey encodes.
	t.Logf("blob: %d bytes, % x", len(blob), blob)

	if got := store.Token("ptrbox-credread-probe"); got != "probe-VALUE-123" {
		t.Errorf("Token = %q, want %q", got, "probe-VALUE-123")
	}
	if got := store.Token("ptrbox-no-such-entry-" + strings.Repeat("x", 8)); got != "" {
		t.Errorf("a missing entry read as %q, want empty", got)
	}
}
