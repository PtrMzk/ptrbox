package cli

import (
	"encoding/binary"
	"os/exec"
	"strings"
	"unicode/utf16"
)

// Keychain is where the Claude OAuth token comes from: the platform's secret
// store. The only credential ptrbox ever puts in a VM, and the reason this is
// an interface: the store differs per platform, a machine may have none, and
// the tests must be able to say so.
type Keychain interface {
	// Available reports whether the store can be read here at all.
	Available() bool
	// Token returns the secret for a service, or "" if there is no such
	// entry. A missing entry is not an error - it is the state of a fresh
	// machine, and the caller warns rather than fails.
	Token(service string) string
	// Name is what the store is called, as it reads in a sentence.
	Name() string
	// SetupAdvice is what to type to create the entry, one command per line.
	// Every command here must PROMPT for the secret: advice that puts a token
	// on a command line puts it in shell history.
	SetupAdvice(service string) []string
}

// HostKeychain is the secret store of the platform ptrbox was built for.
func HostKeychain() Keychain { return hostKeychain() }

// SecurityKeychain reads the macOS Keychain via `security`.
type SecurityKeychain struct{}

func (SecurityKeychain) Name() string { return "macOS Keychain" }

func (SecurityKeychain) SetupAdvice(service string) []string {
	return []string{
		"claude setup-token",
		"security add-generic-password -a \"$USER\" -s " + service + " -w",
	}
}

func (SecurityKeychain) Available() bool {
	_, err := exec.LookPath("security")
	return err == nil
}

func (SecurityKeychain) Token(service string) string {
	// -w prints the password and nothing else. The value goes straight into a
	// string here and from there onto a pipe - never into an argument vector,
	// where ps and shell history would see it.
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\r\n")
}

// CredentialManager reads a generic credential from Windows Credential
// Manager. The read itself is one call into advapi32 and lives in
// keychain_windows.go; everything that can be said without Windows is here,
// where the suite runs it.
type CredentialManager struct{}

func (CredentialManager) Name() string { return "Windows Credential Manager" }

// cmdkey with a bare /pass prompts for the secret rather than taking it as an
// argument. /user is required by cmdkey and read by nothing.
func (CredentialManager) SetupAdvice(service string) []string {
	return []string{
		"claude setup-token",
		"cmdkey /generic:" + service + " /user:token /pass",
	}
}

func (CredentialManager) Available() bool { return credReadAvailable() }

func (CredentialManager) Token(service string) string {
	blob, ok := credRead(service)
	if !ok {
		return ""
	}
	return decodeCredentialBlob(blob)
}

// decodeCredentialBlob turns a credential's bytes into the token. The blob is
// opaque to Windows, so its encoding is whatever wrote it: cmdkey and the
// Credential Manager UI store UTF-16LE, most libraries store UTF-8.
//
// Told apart by a NUL byte. A token is ASCII, so as UTF-16LE every second
// byte of it is zero, and as UTF-8 none is - there is no token for which the
// two readings are both plausible.
func decodeCredentialBlob(blob []byte) string {
	text := string(blob)
	if len(blob)%2 == 0 && strings.IndexByte(text, 0) >= 0 {
		units := make([]uint16, len(blob)/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(blob[2*i:])
		}
		text = string(utf16.Decode(units))
	}
	return strings.TrimRight(text, "\x00\r\n")
}
