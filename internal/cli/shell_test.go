package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
)

func TestShellOpensASessionInTheWorkspaceOnTheCallersStreams(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.fake.Reset()
	h.stdin = "claude\n"

	h.mustRun("shell", "demo")

	if len(h.fake.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one", h.fake.Sessions)
	}
	session := h.fake.Sessions[0]
	if session.VM != "demo" || session.Workdir != "/workspace" {
		t.Errorf("session = %+v, want demo in /workspace", session)
	}
	// What the person types reaches the shell, and what the shell prints
	// reaches their stdout - not stderr, and not the narrator, which would
	// hold a prompt back until the newline that never comes.
	if session.Typed != "claude\n" {
		t.Errorf("the session was sent %q", session.Typed)
	}
	if !strings.Contains(h.stdout, "[demo] /workspace $") {
		t.Errorf("the session's output did not reach stdout: %q", h.stdout)
	}
	if strings.Contains(h.stderr, "[demo] /workspace $") {
		t.Errorf("the session's output went through the narrator:\n%s", h.stderr)
	}
	h.assertCalled(`^shell --workdir /workspace demo$`)
}

func TestShellResolvesARepoPathLikeEveryOtherCommand(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("shell", h.repos+"/demo")
	if len(h.fake.Sessions) != 1 || h.fake.Sessions[0].VM != "demo" {
		t.Errorf("sessions = %+v", h.fake.Sessions)
	}
}

func TestShellStartsAStoppedSandboxTheWayStartDoes(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("stop", "demo") // the last sandbox: the proxy stops with it
	h.fake.Reset()

	h.mustRun("shell", "demo")

	h.assertOutputContains(`VM "demo" is stopped - starting it`)
	// Proxy first: a sandbox brought up without it has no network, which is
	// the whole reason this goes through startSandbox and not the backend.
	h.assertOrder("^start "+config.ProxyVM, "^start demo")
	h.assertOrder("^start demo", "^shell --workdir /workspace demo")
	if strings.Contains(h.output(), "enter it:") {
		t.Errorf("shell advised entering the VM it was about to enter:\n%s", h.output())
	}
}

func TestShellPassesTheSessionsExitStatusOnAndSaysNothing(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.fake.ShellExit = 3

	err := h.run("shell", "demo")
	var status ExitStatus
	if !errors.As(err, &status) || int(status) != 3 {
		t.Fatalf("err = %v, want ExitStatus 3", err)
	}
	if errors.Is(err, ErrReported) || errors.Is(err, ErrUsage) {
		t.Error("a shell's exit status was filed under ptrbox's own failures")
	}
}

func TestShellRefusesTheProxyAndAnUnknownVM(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install", "--yes")

	err := h.run("shell", config.ProxyVM)
	if err == nil || !strings.Contains(err.Error(), "not a sandbox") ||
		!strings.Contains(err.Error(), "limactl shell "+config.ProxyVM) {
		t.Errorf("err = %v, want the refusal and the backend's own way in", err)
	}

	err = h.run("shell", "nothing-here")
	if err == nil || !strings.Contains(err.Error(), "ptrbox new nothing-here") {
		t.Errorf("err = %v, want it to say how to create the VM", err)
	}
	if len(h.fake.Sessions) != 0 {
		t.Errorf("a session was opened anyway: %+v", h.fake.Sessions)
	}

	if err := h.run("shell", "demo", "ls"); err == nil || !strings.Contains(err.Error(), "no command") {
		t.Errorf("err = %v, want a refusal to run a command", err)
	}
}
