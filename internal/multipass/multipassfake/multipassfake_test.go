package multipassfake_test

// The fake's contract with the real client: the shapes the client sends are
// the shapes the fake answers, against the same JSON the PC printed.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/backend"
	"github.com/PtrMzk/ptrbox/internal/multipass"
	"github.com/PtrMzk/ptrbox/internal/multipass/multipassfake"
)

func newBackend() (multipass.Backend, *multipassfake.Fake) {
	f := multipassfake.New()
	return multipass.Backend{Client: multipass.New(f, io.Discard, io.Discard)}, f
}

func spec(t *testing.T, mount bool) backend.Spec {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "demo.yaml")
	if err := os.WriteFile(configPath, []byte("#cloud-config\nusers:\n  - default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := backend.Spec{Name: "demo", ConfigPath: configPath, CPUs: 2, Memory: "2GiB", Disk: "10GiB", Distro: "ubuntu2404"}
	if mount {
		s.Mount = &backend.Mount{Host: `C:\Users\you\code\demo`, Guest: "/workspace"}
	}
	return s
}

func TestTheClientCanCreateListAndDeleteThroughTheFake(t *testing.T) {
	b, f := newBackend()
	if err := b.Create(spec(t, true)); err != nil {
		t.Fatal(err)
	}
	if !b.Running("demo") || !b.Exists("demo") {
		t.Errorf("status = %q after launch", b.Status("demo"))
	}
	if mounts, err := b.Mounts("demo"); err != nil || strings.Join(mounts, " ") != "/workspace" {
		t.Errorf("Mounts = %v, %v", mounts, err)
	}
	if err := b.Stop("demo"); err != nil || b.Running("demo") || !b.Exists("demo") {
		t.Errorf("after stop: running %v exists %v err %v", b.Running("demo"), b.Exists("demo"), err)
	}
	if err := b.Delete("demo"); err != nil || b.Exists("demo") {
		t.Errorf("after delete: exists %v err %v", b.Exists("demo"), err)
	}
	if !f.Called("delete --purge demo") {
		t.Errorf("calls:\n%s", f.CallLog())
	}
}

func TestMountsDisabledOnTheHostSkipsTheMountAndTheClientNotices(t *testing.T) {
	b, f := newBackend()
	f.MountsDisabled = true
	err := b.Create(spec(t, true))
	if err == nil || !strings.Contains(err.Error(), "privileged-mounts") {
		t.Errorf("err = %v, want the mount check failing", err)
	}
	if b.Status("demo") != backend.StatusRunning {
		t.Error("launch did not leave the VM running, as the real one does")
	}
}

func TestAHostRebootLeavesTheMountOnRecordAndRestartRepairsIt(t *testing.T) {
	b, f := newBackend()
	if err := b.Create(spec(t, true)); err != nil {
		t.Fatal(err)
	}
	f.Unmounted["demo"] = true
	f.Reset()
	if err := b.Start("demo"); err != nil {
		t.Fatal(err)
	}
	if !f.InOrder("sh /workspace", "restart demo") || strings.Count(f.CallLog(), "sh /workspace") != 2 {
		t.Errorf("want a mount check, a restart, and a check again:\n%s", f.CallLog())
	}
}

func TestSendThroughTheFakeLandsOnTheGuestCommandsStdinAndLeavesNoFile(t *testing.T) {
	b, f := newBackend()
	if err := b.Create(spec(t, false)); err != nil {
		t.Fatal(err)
	}
	if err := b.Send("demo", backend.Agent, strings.NewReader("tok\n"), "bash", "-c", "cat >> ~/.profile"); err != nil {
		t.Fatal(err)
	}
	if len(f.Stdins) != 1 || f.Stdins[0] != "tok\n" {
		t.Errorf("Stdins = %q, want the payload as the guest command's stdin", f.Stdins)
	}
	for path := range f.Files["demo"] {
		if strings.Contains(path, "stdin-") {
			t.Errorf("the transfer file %s survived", path)
		}
	}
	if err := b.Send("ptrbox-proxy", backend.Login, strings.NewReader("x"), "sudo", "tee", "/etc/x"); err == nil {
		t.Error("Send into a VM that does not exist succeeded")
	}
}

func TestSendAsTheLoginUserWritesThroughSudoTee(t *testing.T) {
	b, f := newBackend()
	f.AddVM("ptrbox-proxy", backend.StatusRunning)
	if err := b.Send("ptrbox-proxy", backend.Login, strings.NewReader("conf\n"), "sudo", "tee", "/etc/squid/squid.conf"); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.ReadFile("ptrbox-proxy", "/etc/squid/squid.conf"); !ok || got != "conf\n" {
		t.Errorf("file = %q, %v", got, ok)
	}
}

func TestExecIntoAStoppedVMFails(t *testing.T) {
	b, f := newBackend()
	f.AddVM("demo", "Stopped")
	if _, err := b.Output("demo", backend.Login, "true"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("err = %v", err)
	}
}

func TestACloudInitExitOfTwoIsDoneWithWarnings(t *testing.T) {
	var shown strings.Builder
	f := multipassfake.New()
	f.CloudInitExit = 2
	b := multipass.Backend{Client: multipass.New(f, &shown, io.Discard)}
	if err := b.Create(spec(t, false)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown.String(), "schema validation") {
		t.Errorf("warnings not shown: %q", shown.String())
	}
	f.CloudInitExit = 1
	if err := b.Start("demo"); err == nil {
		t.Error("a cloud-init error was not a failed start")
	}
}

func TestTheShellIsASessionInTheWorkspace(t *testing.T) {
	b, f := newBackend()
	f.AddVM("demo", backend.StatusRunning)
	var out strings.Builder
	if err := b.Shell("demo", strings.NewReader("ls\n"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.Sessions) != 1 || f.Sessions[0].VM != "demo" || f.Sessions[0].Workdir != "/workspace" || f.Sessions[0].Typed != "ls\n" {
		t.Errorf("sessions = %+v", f.Sessions)
	}
}

func TestADeadMountIsListedAndRepairedByRestart(t *testing.T) {
	b, f := newBackend()
	if err := b.Create(spec(t, true)); err != nil {
		t.Fatal(err)
	}
	f.DeadMounts["demo"] = true
	f.Reset()
	if err := b.Ready("demo"); err != nil {
		t.Fatal(err)
	}
	if !f.Called("restart demo") {
		t.Errorf("a dead mount did not get the restart:\n%s", f.CallLog())
	}
}
