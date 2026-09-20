package cli

// vm/provision/90-harden.sh, run for real - the sixth guest script the suite
// executes rather than lints - and vm/verify.sh's reading of what it leaves.
//
// The question worth executing arrived with the two-user model: the script
// used to take root away from everyone, and now it takes it away from
// everyone but a named account. "Everyone but" is where a shell script goes
// wrong quietly - a grep that matches the wrong lines, a guard on the wrong
// binary - and the golden file shows only that the text rendered, not what
// the text does to a sudoers directory. So: a real bash, a planted sudoers
// directory, a planted tree of setuid binaries, and the script pointed at
// them through the arguments it takes for exactly this purpose.
//
// Then verify.sh, against the same tree, because the two scripts share a
// contract that nothing else asserts: sudo may stay setuid only when a
// root-owned marker names a daemon user, and the marker is trusted only when
// the agent could not have written it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/render"
)

// hardenGuest is the planted guest: a state directory the script may write
// into, a sudoers directory, and a root under which the setuid binaries live.
type hardenGuest struct {
	dir, state, sudoers, root string
}

// The daemon account every case that has one uses. Not "ubuntu" by accident:
// it is what Multipass's images call the default user, and what the step-0
// capture saw keep NOPASSWD.
const daemon = "ubuntu"

// hardenScript renders 90-harden.sh for the given daemon user and plants the
// world around it. The sudoers directory gets three drop-ins: cloud-init's
// (the daemon user's grant), one granting the agent, and one that mentions
// no NOPASSWD at all. The tree gets sudo and su setuid, ssh-agent setgid.
func hardenScript(t *testing.T, daemonUser string) hardenGuest {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	dir := t.TempDir()
	g := hardenGuest{
		dir:     dir,
		state:   filepath.Join(dir, "state"),
		sudoers: filepath.Join(dir, "sudoers.d"),
		root:    filepath.Join(dir, "root"),
	}

	// Rendered the way `ptrbox new` renders it, from the same key: a test
	// that substituted the placeholder some other way would prove nothing
	// about the real script.
	var buf strings.Builder
	err := render.Render(&buf, ptrbox.Assets, "vm/provision/90-harden.sh", "vm",
		render.Values{"DAEMON_USER": daemonUser})
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(dir, "harden.sh"), buf.String())
	writeScript(t, filepath.Join(dir, "verify.sh"), asset(t, "vm/verify.sh"))

	if err := os.MkdirAll(g.sudoers, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		// What cloud-init writes for the default user.
		"90-cloud-init-users": "# Created by cloud-init\n" + daemon + " ALL=(ALL) NOPASSWD:ALL\n",
		// What an attacker, or a careless template, would add.
		"99-agent": "agent ALL=(ALL) NOPASSWD:ALL\n",
		// A drop-in that grants nothing passwordless is not this script's
		// business.
		"README": "# See the sudoers man page.\nDefaults env_reset\n",
	} {
		if err := os.WriteFile(filepath.Join(g.sudoers, name), []byte(body), 0o440); err != nil {
			t.Fatal(err)
		}
	}

	plant(t, filepath.Join(g.root, "usr/bin/sudo"), 0o755|os.ModeSetuid)
	plant(t, filepath.Join(g.root, "usr/bin/su"), 0o755|os.ModeSetuid)
	plant(t, filepath.Join(g.root, "usr/bin/ssh-agent"), 0o755|os.ModeSetgid)

	// verify.sh's egress probes are not what these cases are about, and a
	// test host is not a sandbox; the quiet stubs answer them in a second.
	// The sudo stub exits 1, which is what a sudo-less guest says.
	stubs := sharedStubs(t, "quiet", quietStubs)
	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", dir)
	return g
}

// plant writes an executable at path with the given mode - setuid and setgid
// included, which os.WriteFile's mode would lose to the umask.
func plant(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// harden runs the rendered script against the planted guest.
func (g hardenGuest) harden(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("bash", filepath.Join(g.dir, "harden.sh"), g.state, g.sudoers, g.root).CombinedOutput()
	if err != nil {
		t.Fatalf("90-harden.sh failed: %v\n%s", err, out)
	}
	return string(out)
}

// setuid reports whether the named binary under the root still has a setuid
// or setgid bit.
func (g hardenGuest) setuid(t *testing.T, name string) bool {
	t.Helper()
	info, err := os.Stat(filepath.Join(g.root, name))
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0
}

func (g hardenGuest) dropinExists(name string) bool {
	_, err := os.Stat(filepath.Join(g.sudoers, name))
	return err == nil
}

// marker writes $state/backend as a cloud-init backend would, and locks it
// the way root ownership does in a guest: neither the file nor the directory
// is writable by the user running verify.sh. Unlocked again on cleanup, or
// the temp dir could not be removed.
func (g hardenGuest) marker(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(g.state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.state, "backend"), []byte(body), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(g.state, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(g.state, 0o755) })
}

// verifyLine runs verify.sh with the planted root as its sweep root and
// returns the verdict line for the named check, or "" if it printed none.
func (g hardenGuest) verifyLine(t *testing.T, check string) string {
	t.Helper()
	// The exit status is deliberately ignored: on a test host the egress
	// checks fail, and the line under test is printed regardless.
	out, _ := exec.Command("bash", filepath.Join(g.dir, "verify.sh"), g.state, g.root).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, check) {
			return line
		}
	}
	return ""
}

// Lima's rendering: no daemon user. Everything passwordless goes, sudo loses
// its bit, and the drop-in that grants nothing is left alone.
func TestWithoutADaemonUserNobodyKeepsRoot(t *testing.T) {
	g := hardenScript(t, "")
	g.harden(t)

	if g.dropinExists("90-cloud-init-users") {
		t.Error("cloud-init's NOPASSWD drop-in survived")
	}
	if g.dropinExists("99-agent") {
		t.Error("the agent's NOPASSWD drop-in survived")
	}
	if !g.dropinExists("README") {
		t.Error("a drop-in with no NOPASSWD grant was removed")
	}
	if g.setuid(t, "usr/bin/sudo") {
		t.Error("sudo is still setuid with no daemon user")
	}
	if g.setuid(t, "usr/bin/su") {
		t.Error("su is still setuid")
	}
	if !g.setuid(t, "usr/bin/ssh-agent") {
		t.Error("ssh-agent lost its setgid bit; it is on the deliberate-keep list")
	}
	// And it records its timing under its own name, like every other script.
	timings, err := os.ReadFile(filepath.Join(g.state, "timings"))
	if err != nil || !strings.HasPrefix(string(timings), "90-harden ") {
		t.Errorf("no timing record: %v %q", err, timings)
	}

	// verify.sh, with no marker, agrees: nothing unexpected is setuid, and
	// there is no daemon-user check to print.
	if line := g.verifyLine(t, "setuid stripped"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
	if line := g.verifyLine(t, "daemon user isolated"); line != "" {
		t.Errorf("verify.sh printed a daemon-user verdict with no daemon user: %q", line)
	}
	if line := g.verifyLine(t, "backend marker"); line != "" {
		t.Errorf("verify.sh printed a marker verdict with no marker: %q", line)
	}
}

// The two-user model: the daemon user's grant and sudo's bit survive, and
// nothing else does.
func TestADaemonUserKeepsItsSudoAndNobodyElses(t *testing.T) {
	g := hardenScript(t, daemon)
	// A drop-in that grants the daemon user AND someone else on separate
	// lines: it goes whole, costing the daemon nothing it does not have from
	// its own drop-in, rather than being kept for the line that is fine.
	mixed := filepath.Join(g.sudoers, "95-mixed")
	if err := os.WriteFile(mixed, []byte(daemon+" ALL=(ALL) NOPASSWD:ALL\nagent ALL=(ALL) NOPASSWD:ALL\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	g.harden(t)

	if !g.dropinExists("90-cloud-init-users") {
		t.Error("the daemon user's NOPASSWD drop-in was removed; the daemon cannot mount without it")
	}
	if g.dropinExists("99-agent") {
		t.Error("the agent's NOPASSWD drop-in survived beside the daemon user's")
	}
	if g.dropinExists("95-mixed") {
		t.Error("a drop-in granting the daemon user and the agent was kept")
	}
	if !g.setuid(t, "usr/bin/sudo") {
		t.Error("sudo lost its setuid bit; the daemon user has no other way to root")
	}
	if g.setuid(t, "usr/bin/su") {
		t.Error("su is still setuid; only sudo is exempt")
	}
}

// verify.sh accepts a setuid sudo only on the word of a marker it can trust,
// and with one it also asks whether the agent is, or can become, the daemon
// user.
func TestASetuidSudoNeedsATrustedMarker(t *testing.T) {
	g := hardenScript(t, daemon)
	g.harden(t)

	// No marker: verify.sh knows nothing of a daemon user and sudo is a
	// regression, the same as on lima.
	line := g.verifyLine(t, "setuid stripped")
	if !strings.Contains(line, "FAIL") || !strings.Contains(line, "/usr/bin/sudo") {
		t.Errorf("verify.sh = %q, want a failure naming sudo with no marker", line)
	}

	// A marker, locked the way root ownership locks it: sudo is licensed,
	// the mount type is read, and the isolation check runs and passes
	// against a sudo that refuses.
	g.marker(t, "backend=multipass\nmount-type=fuse.sshfs\ndaemon-user="+daemon+"\n")
	if line := g.verifyLine(t, "backend marker"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK for a read-only marker", line)
	}
	if line := g.verifyLine(t, "setuid stripped"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK with a marker naming a daemon user", line)
	}
	if line := g.verifyLine(t, "daemon user isolated"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
	if line := g.verifyLine(t, "exactly one mount"); !strings.Contains(line, "fuse.sshfs") {
		t.Errorf("verify.sh = %q, want the mount type read from the marker", line)
	}
}

// A marker the agent could have written licenses nothing. verify.sh runs as
// the agent, so "writable by me" is the test.
func TestAWritableMarkerIsReportedAndIgnored(t *testing.T) {
	g := hardenScript(t, daemon)
	g.harden(t)
	// The state directory as 90-harden.sh left it - writable, since this
	// user made it - and a marker to match.
	if err := os.WriteFile(filepath.Join(g.state, "backend"), []byte("daemon-user="+daemon+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if line := g.verifyLine(t, "backend marker"); !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want FAIL for a marker the agent can write", line)
	}
	line := g.verifyLine(t, "setuid stripped")
	if !strings.Contains(line, "FAIL") || !strings.Contains(line, "/usr/bin/sudo") {
		t.Errorf("verify.sh = %q, want sudo reported - the marker licensing it is not trusted", line)
	}
	if line := g.verifyLine(t, "daemon user isolated"); line != "" {
		t.Errorf("verify.sh = %q, want no daemon-user check from an untrusted marker", line)
	}

	// A read-only file in a writable directory is the same thing: the
	// agent can replace it.
	if err := os.Chmod(filepath.Join(g.state, "backend"), 0o444); err != nil {
		t.Fatal(err)
	}
	if line := g.verifyLine(t, "backend marker"); !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want FAIL for a marker in a directory the agent can write", line)
	}
}

// The isolation check's two failures: sudo lets the agent become the daemon
// user, or this IS the daemon user.
func TestAnAgentWithAPathToTheDaemonUserFails(t *testing.T) {
	g := hardenScript(t, daemon)
	g.harden(t)
	g.marker(t, "mount-type=fuse.sshfs\ndaemon-user="+daemon+"\n")

	// A sudo that grants. Shared, see stubs_test.go; put ahead of the quiet
	// set so it wins.
	granting := sharedStubs(t, "granting-sudo", func(dir string) {
		mustWriteScript(filepath.Join(dir, "sudo"), "#!/bin/sh\nexit 0\n")
	})
	t.Setenv("PATH", granting+string(os.PathListSeparator)+os.Getenv("PATH"))
	line := g.verifyLine(t, "daemon user isolated")
	if !strings.Contains(line, "FAIL") || !strings.Contains(line, "become "+daemon) {
		t.Errorf("verify.sh = %q, want a failure saying the agent can become the daemon user", line)
	}

	// An id that says this is the daemon user. Checked first, so the sudo
	// answer does not matter.
	asDaemon := sharedStubs(t, "id-daemon", func(dir string) {
		mustWriteScript(filepath.Join(dir, "id"), "#!/bin/sh\necho "+daemon+"\n")
	})
	t.Setenv("PATH", asDaemon+string(os.PathListSeparator)+os.Getenv("PATH"))
	line = g.verifyLine(t, "daemon user isolated")
	if !strings.Contains(line, "FAIL") || !strings.Contains(line, "running as "+daemon) {
		t.Errorf("verify.sh = %q, want a failure saying this is the daemon user", line)
	}
}
