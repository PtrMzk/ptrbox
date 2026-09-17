//go:build !windows

package cli

func hostKeychain() Keychain { return SecurityKeychain{} }

// There is no Credential Manager to read off Windows. The type still exists
// here so that its name, its advice and its decoding are tested wherever the
// suite runs.
func credReadAvailable() bool        { return false }
func credRead(string) ([]byte, bool) { return nil, false }
