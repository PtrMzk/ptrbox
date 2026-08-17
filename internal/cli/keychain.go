package cli

import (
	"os/exec"
	"strings"
)

// Keychain is where the Claude OAuth token comes from. The only credential
// ptrbox ever puts in a VM, and the reason this is an interface: on anything
// but a Mac there is no Keychain, and the tests must be able to say so.
type Keychain interface {
	// Available reports whether a Keychain can be read here at all.
	Available() bool
	// Token returns the secret for a service, or "" if there is no such
	// entry. A missing entry is not an error - it is the state of a fresh
	// Mac, and the caller warns rather than fails.
	Token(service string) string
}

// SecurityKeychain reads the macOS Keychain via `security`.
type SecurityKeychain struct{}

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
