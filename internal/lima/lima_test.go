package lima_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/lima/limafake"
)

func newClient(t *testing.T) (*lima.Client, *limafake.Fake) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	fake := limafake.New()
	return &lima.Client{Runner: fake, Stdout: io.Discard, Stderr: io.Discard}, fake
}

func TestListAndStatus(t *testing.T) {
	client, fake := newClient(t)
	fake.AddVM("demo", lima.StatusRunning)
	fake.AddVM("other", "Stopped")

	if got := client.List(); len(got) != 2 || got[0].Name != "demo" || got[1].Status != "Stopped" {
		t.Fatalf("List = %+v", got)
	}
	if got := client.Names(); strings.Join(got, ",") != "demo,other" {
		t.Errorf("Names = %v", got)
	}
	if client.Status("demo") != lima.StatusRunning || client.Status("nope") != "" {
		t.Errorf("Status = %q / %q", client.Status("demo"), client.Status("nope"))
	}
	if !client.Exists("other") || client.Exists("nope") {
		t.Error("Exists is wrong")
	}
	if !client.Running("demo") || client.Running("other") {
		t.Error("Running is wrong")
	}
}

func TestAnUnreadableListingLooksLikeNoInformation(t *testing.T) {
	// Callers use List to decide whether to stop the proxy. A failed listing
	// must not read as "nothing is running" by accident - it returns nothing,
	// and every caller treats an empty answer from a failing command as a
	// reason to leave things alone.
	client, fake := newClient(t)
	fake.AddVM("demo", lima.StatusRunning)
	fake.ListFails = true

	if got := client.List(); got != nil {
		t.Errorf("List = %+v, want nil", got)
	}
	if client.Running("demo") {
		t.Error("Running said yes off a failed listing")
	}
}

func TestLifecycleCalls(t *testing.T) {
	client, fake := newClient(t)
	config := filepath.Join(t.TempDir(), "demo.yaml")
	if err := os.WriteFile(config, []byte("vmType: vz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := client.Validate(config); err != nil {
		t.Fatal(err)
	}
	if err := client.Create("demo", config); err != nil {
		t.Fatal(err)
	}
	if fake.VMStatus("demo") != lima.StatusRunning {
		t.Errorf("demo is %q after Create", fake.VMStatus("demo"))
	}
	if err := client.Stop("demo"); err != nil || fake.VMStatus("demo") != "Stopped" {
		t.Errorf("Stop: %v, status %q", err, fake.VMStatus("demo"))
	}
	if err := client.Start("demo"); err != nil || fake.VMStatus("demo") != lima.StatusRunning {
		t.Errorf("Start: %v, status %q", err, fake.VMStatus("demo"))
	}
	if err := client.Delete("demo"); err != nil || fake.VMStatus("demo") != "" {
		t.Errorf("Delete: %v, status %q", err, fake.VMStatus("demo"))
	}

	if !fake.InOrder("validate", "start --name demo") {
		t.Errorf("validate did not precede start:\n%s", fake.CallLog())
	}
	if !fake.Called(`start --name demo -y --timeout 20m`) {
		t.Errorf("creation used unexpected arguments:\n%s", fake.CallLog())
	}
	if !fake.Called(`delete -f demo`) {
		t.Errorf("delete was not forced:\n%s", fake.CallLog())
	}
}

func TestValidateRejectsAMissingConfig(t *testing.T) {
	client, _ := newClient(t)
	if err := client.Validate(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("validating a missing config succeeded")
	}
}

func TestOutputCapturesStdoutAndPutsStderrInTheError(t *testing.T) {
	client, fake := newClient(t)
	fake.AddVM("demo", lima.StatusRunning)
	fake.WriteFile("demo", "/etc/squid/squid.conf", "http_port 8888\n")

	got, err := client.Output(lima.ShellArgs("demo", "sudo", "cat", "/etc/squid/squid.conf")...)
	if err != nil || got != "http_port 8888\n" {
		t.Fatalf("Output = %q, %v", got, err)
	}

	_, err = client.Output(lima.ShellArgs("demo", "sudo", "cat", "/nope")...)
	if err == nil || !strings.Contains(err.Error(), "No such file") {
		t.Errorf("err = %v, want it to carry limactl's stderr", err)
	}
}

func TestSendWiresStdinIntoTheGuest(t *testing.T) {
	client, fake := newClient(t)
	fake.AddVM("demo", lima.StatusRunning)

	payload := "export CLAUDE_CODE_OAUTH_TOKEN=\"sk-ant-oat-EXAMPLE\"\n"
	err := client.Send(strings.NewReader(payload),
		lima.ShellArgs("demo", "bash", "-c", "cat >> ~/.profile")...)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Stdins) != 1 || fake.Stdins[0] != payload {
		t.Fatalf("Stdins = %q", fake.Stdins)
	}
	// The point of Send: the payload is never an argument.
	if strings.Contains(fake.CallLog(), "sk-ant-oat-EXAMPLE") {
		t.Errorf("the payload reached argv:\n%s", fake.CallLog())
	}
}

func TestStreamWritesToTheGivenWriter(t *testing.T) {
	client, fake := newClient(t)
	fake.AddVM("ptrbox-proxy", lima.StatusRunning)
	fake.WriteFile("ptrbox-proxy", "/var/log/squid/access.log", "one\ntwo\nthree\n")

	var out bytes.Buffer
	err := client.Stream(&out, lima.ShellArgs("ptrbox-proxy", "sudo", "tail", "-n", "2",
		"/var/log/squid/access.log")...)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "two\nthree\n" {
		t.Errorf("Stream wrote %q", out.String())
	}
}

func TestShellArgsPutTheSeparatorInTheRightPlace(t *testing.T) {
	got := lima.ShellArgs("demo", "sudo", "cat", "/etc/hosts")
	want := []string{"shell", "demo", "--", "sudo", "cat", "/etc/hosts"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ShellArgs = %v", got)
	}
}

func TestFakeRecordsLongArgumentsOutOfLine(t *testing.T) {
	// The verification script is one enormous argv element; the call log has
	// to stay line-oriented for order assertions to be readable.
	client, fake := newClient(t)
	fake.AddVM("demo", lima.StatusRunning)
	script := strings.Repeat("echo verifying\n", 20)

	if err := client.Passthrough(lima.ShellArgs("demo", "bash", "-lc", script)...); err != nil {
		t.Fatal(err)
	}
	if !fake.Called(`shell demo -- bash -lc <script:1>`) {
		t.Errorf("call log = %q", fake.CallLog())
	}
	if len(fake.Scripts) != 1 || fake.Scripts[0] != script {
		t.Errorf("Scripts = %q", fake.Scripts)
	}
}

func TestFakeReportsAFailedVerification(t *testing.T) {
	client, fake := newClient(t)
	fake.AddVM("demo", lima.StatusRunning)
	fake.VerifyFails = true
	if err := client.Passthrough(lima.ShellArgs("demo", "bash", "-lc", "verify")...); err == nil {
		t.Error("verification succeeded with VerifyFails set")
	}
}

func TestFakeFileOperations(t *testing.T) {
	client, fake := newClient(t)
	fake.AddVM("ptrbox-proxy", lima.StatusRunning)

	err := client.Send(strings.NewReader("candidate\n"),
		lima.ShellArgs("ptrbox-proxy", "sudo", "tee", "/etc/squid/squid.conf.ptrbox-new")...)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := fake.ReadFile("ptrbox-proxy", "/etc/squid/squid.conf.ptrbox-new"); got != "candidate\n" {
		t.Fatalf("tee wrote %q", got)
	}

	if err := client.Passthrough(lima.ShellArgs("ptrbox-proxy", "sudo", "mv",
		"/etc/squid/squid.conf.ptrbox-new", "/etc/squid/squid.conf")...); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.ReadFile("ptrbox-proxy", "/etc/squid/squid.conf.ptrbox-new"); ok {
		t.Error("mv left the source behind")
	}
	if got, _ := fake.ReadFile("ptrbox-proxy", "/etc/squid/squid.conf"); got != "candidate\n" {
		t.Errorf("mv produced %q", got)
	}

	if err := client.Passthrough(lima.ShellArgs("ptrbox-proxy", "sudo", "rm", "-f",
		"/etc/squid/squid.conf")...); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.ReadFile("ptrbox-proxy", "/etc/squid/squid.conf"); ok {
		t.Error("rm -f left the file behind")
	}
}

func TestFakeSquidParseCanBeMadeToReject(t *testing.T) {
	client, fake := newClient(t)
	fake.AddVM("ptrbox-proxy", lima.StatusRunning)
	fake.SquidParseFails = true

	if _, err := client.Output(lima.ShellArgs("ptrbox-proxy", "sudo", "squid", "-k", "parse")...); err == nil {
		t.Error("squid -k parse succeeded with SquidParseFails set")
	}
	// Only parsing fails; a reconfigure is not a parse.
	if err := client.Passthrough(lima.ShellArgs("ptrbox-proxy", "sudo", "squid", "-k", "reconfigure")...); err != nil {
		t.Errorf("reconfigure failed: %v", err)
	}
}

func TestFakeWritesTheSSHConfigLimaWouldWrite(t *testing.T) {
	client, _ := newClient(t)
	config := filepath.Join(t.TempDir(), "demo.yaml")
	if err := os.WriteFile(config, []byte("vmType: vz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.Create("demo", config); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".lima", "demo", "ssh.config")); err != nil {
		t.Errorf("no per-VM ssh config: %v", err)
	}
}

func TestAvailable(t *testing.T) {
	client, _ := newClient(t)
	if !client.Available() {
		t.Error("the fake reports limactl unavailable")
	}
	real := &lima.Client{Runner: lima.Exec{}}
	// Whatever the answer, it must come from PATH rather than panicking.
	_ = real.Available()
}
