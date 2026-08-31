package cli

// vm/verify.sh's setuid sweep, run for real against a planted directory.
//
// 90-harden.sh strips the setuid bit from every binary with no caller in a
// sandbox, because removing the sudoers entry takes away the permission while
// the bit is the mechanism. This is the check that says the stripping actually
// happened - and the regression it exists for is not somebody editing that
// script, it is an apt upgrade reinstalling a package and restoring a bit.
//
// The scan root is an argument for the same reason the state directory is: in
// a guest it must be /, and under test it must not be, because the suite runs
// this script on the developer machine. A sweep of a real root filesystem is a
// second in a small VM and minutes on a Mac.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setuidGuest plants files under a scan root and returns it. Each name in
// suid gets the setuid bit; each in plain is left alone.
func setuidGuest(t *testing.T, suid, plain []string) (dir, scan string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	dir = t.TempDir()
	scan = filepath.Join(dir, "scan")
	writeScript(t, filepath.Join(dir, "verify.sh"), asset(t, "vm/verify.sh"))

	for _, group := range []struct {
		names []string
		mode  os.FileMode
	}{
		// os.ModeSetuid, NOT 0o4755. Go's FileMode is not the Unix mode word:
		// the setuid flag is 1<<23, so passing the octal bit sets ordinary
		// permissions and silently drops the thing under test - which is how
		// the first version of this file passed while asserting nothing.
		// Setting it on a file you own needs no privilege.
		{suid, 0o755 | os.ModeSetuid},
		{plain, 0o755},
	} {
		for _, name := range group.names {
			path := filepath.Join(scan, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			// Written then chmod'd: WriteFile's mode is masked by umask, and
			// the setuid bit does not survive it.
			if err := os.Chmod(path, group.mode); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The egress probes are not what these cases are about, and a test host is
	// not a sandbox: without stubs each run spends 25 seconds discovering it
	// has no proxy. Shared; see stubs_test.go.
	stubs := sharedStubs(t, "quiet", quietStubs)
	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", dir)
	return dir, scan
}

// setuidLine runs verify.sh against the planted scan root.
func setuidLine(t *testing.T, dir, scan string) string {
	t.Helper()
	out, _ := exec.Command("bash", filepath.Join(dir, "verify.sh"),
		filepath.Join(dir, "state"), scan).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "setuid stripped") {
			return line
		}
	}
	t.Fatalf("verify.sh printed no setuid line:\n%s", out)
	return ""
}

// The three that are deliberately left setuid are accepted, and nothing else
// is present. This is what a correctly hardened guest looks like.
func TestTheExpectedSetuidBinariesArePermitted(t *testing.T) {
	dir, scan := setuidGuest(t,
		[]string{
			"usr/lib/dbus-1.0/dbus-daemon-launch-helper",
			"usr/bin/ssh-agent",
			"usr/sbin/unix_chkpwd",
		},
		[]string{"usr/bin/sudo", "usr/bin/su", "usr/bin/mount"})

	if line := setuidLine(t, dir, scan); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
}

// The regression this check exists for: a package upgrade puts a bit back.
func TestARestoredSetuidBitIsCaught(t *testing.T) {
	dir, scan := setuidGuest(t,
		[]string{"usr/bin/sudo", "usr/bin/ssh-agent"},
		[]string{"usr/bin/su"})

	line := setuidLine(t, dir, scan)
	if !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want a failure - sudo is setuid again", line)
	}
	if !strings.Contains(line, "sudo") {
		t.Errorf("verify.sh = %q, want it to name the binary", line)
	}
	// The permitted one must not be reported alongside it.
	if strings.Contains(line, "ssh-agent") {
		t.Errorf("verify.sh = %q, want ssh-agent accepted", line)
	}
}

// Space-padded matching, not a substring test. A bare match would accept
// /usr/bin/ssh because the permitted list contains /usr/bin/ssh-agent - and
// an ssh binary that regained setuid is exactly the kind of thing this must
// not wave through.
func TestAPrefixOfAPermittedNameIsNotPermitted(t *testing.T) {
	dir, scan := setuidGuest(t, []string{"usr/bin/ssh"}, nil)

	if line := setuidLine(t, dir, scan); !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want a failure - /usr/bin/ssh is not /usr/bin/ssh-agent", line)
	}
}

// setgid counts too: it is a different bit and the same class of escalation.
func TestASetgidBinaryIsAlsoCaught(t *testing.T) {
	dir, scan := setuidGuest(t, nil, []string{"usr/bin/wall"})
	if err := os.Chmod(filepath.Join(scan, "usr/bin/wall"), 0o755|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}

	if line := setuidLine(t, dir, scan); !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want a failure for a setgid binary", line)
	}
}
