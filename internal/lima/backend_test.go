package lima_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/backend"
	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/lima/limafake"
)

func newBackend(t *testing.T) (backend.Backend, *limafake.Fake) {
	t.Helper()
	client, fake := newClient(t)
	return lima.Backend{Client: client}, fake
}

func TestBackendBuildsTheShellArgvSoNoCallerDoes(t *testing.T) {
	b, fake := newBackend(t)
	fake.AddVM("demo", lima.StatusRunning)
	fake.WriteFile("demo", "/etc/squid/squid.conf", "http_port 8888\n")

	got, err := b.Output("demo", backend.Login, "sudo", "cat", "/etc/squid/squid.conf")
	if err != nil || got != "http_port 8888\n" {
		t.Fatalf("Output = %q, %v", got, err)
	}
	if !fake.Called("shell demo -- sudo cat /etc/squid/squid.conf") {
		t.Errorf("limactl was not asked for a shell in the VM:\n%s", fake.CallLog())
	}

	_, err = b.Output("demo", backend.Login, "sudo", "cat", "/nope")
	if err == nil || !strings.Contains(err.Error(), "No such file") || !strings.Contains(err.Error(), "limactl shell demo") {
		t.Errorf("err = %v, want limactl's stderr and the invocation that failed", err)
	}
}

func TestBackendUsersAreOneAccountOnLima(t *testing.T) {
	// A lima guest has a single user. If the two ever produce different
	// argv here, something has started treating lima like a backend with a
	// daemon account, and `sudo -u` in a sudo-less guest is a failed create.
	b, fake := newBackend(t)
	fake.AddVM("demo", lima.StatusRunning)

	for _, user := range []backend.User{backend.Login, backend.Agent} {
		fake.Reset()
		if _, err := b.Output("demo", user, "true"); err != nil {
			t.Fatal(err)
		}
		if got, want := fake.CallLog(), "shell demo -- true"; got != want {
			t.Errorf("user %d: calls = %q, want %q", user, got, want)
		}
	}
}

func TestBackendSendKeepsThePayloadOffTheArgv(t *testing.T) {
	b, fake := newBackend(t)
	fake.AddVM("demo", lima.StatusRunning)

	payload := "export CLAUDE_CODE_OAUTH_TOKEN=\"sk-ant-oat-EXAMPLE\"\n"
	if err := b.Send("demo", backend.Agent, strings.NewReader(payload), "bash", "-c", "cat >> ~/.profile"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Stdins) != 1 || fake.Stdins[0] != payload {
		t.Fatalf("Stdins = %q", fake.Stdins)
	}
	if strings.Contains(fake.CallLog(), "sk-ant-oat-EXAMPLE") {
		t.Errorf("the payload reached an argument vector:\n%s", fake.CallLog())
	}
}

func TestBackendStreamWritesAsItArrives(t *testing.T) {
	b, fake := newBackend(t)
	fake.AddVM("demo", lima.StatusRunning)
	fake.WriteFile("demo", "/var/log/squid/access.log", "one\ntwo\n")

	var out bytes.Buffer
	if err := b.Stream("demo", backend.Login, &out, "sudo", "cat", "/var/log/squid/access.log"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "one\ntwo\n" {
		t.Errorf("streamed %q", out.String())
	}
}

// narrator records where invocations begin and end.
type narrator struct {
	io.Writer
	began [][]string
	ended int
}

func (n *narrator) Begin(args []string) { n.began = append(n.began, args) }
func (n *narrator) End(error)           { n.ended++ }

func TestBackendPassthroughIsNarrated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := limafake.New()
	fake.AddVM("demo", lima.StatusRunning)
	n := &narrator{Writer: io.Discard}
	b := lima.Backend{Client: &lima.Client{Runner: fake, Stdout: n, Stderr: n}}

	if err := b.Passthrough("demo", backend.Agent, "true"); err != nil {
		t.Fatal(err)
	}
	if len(n.began) != 1 || n.ended != 1 || strings.Join(n.began[0], " ") != "shell demo -- true" {
		t.Errorf("began = %v, ended = %d", n.began, n.ended)
	}
}

func TestBackendCreateReadsTheConfigAndNothingElse(t *testing.T) {
	b, fake := newBackend(t)
	configPath := filepath.Join(t.TempDir(), "demo.yaml")
	if err := os.WriteFile(configPath, []byte("images: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := b.Create(backend.Spec{
		Name: "demo", ConfigPath: configPath,
		CPUs: 4, Memory: "8GiB", Disk: "50GiB", Image: "https://example.invalid/x.img",
		Mount: &backend.Mount{Host: "/Users/you/code/demo", Guest: "/workspace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Byte for byte what Client.Create has always sent. A flag appearing here
	// would be sizing said twice, with the YAML the one lima believes.
	// (The fake files any long argument under Scripts, and a temp path is one.)
	want := "start --name demo -y --timeout 20m <script:1>"
	if got := fake.CallLog(); got != want || len(fake.Scripts) != 1 || fake.Scripts[0] != configPath {
		t.Errorf("calls = %q with %q, want %q with the config path", got, fake.Scripts, want)
	}
	if !b.Running("demo") {
		t.Error("the created VM is not running")
	}
}

func TestFacts(t *testing.T) {
	facts := lima.Backend{}.Facts()

	// One address for both trips: usernet's gateway relays to the Mac's
	// loopback, where the proxy VM's forward and LM Studio both are. It used
	// to be config.ProxyHost, and the IPv4 check came here with it - the
	// value is rendered into an nftables rule.
	if ip := net.ParseIP(lima.Gateway); ip == nil || ip.To4() == nil {
		t.Errorf("Gateway = %q, want an IPv4 address", lima.Gateway)
	}
	if facts.ProxyAddr != lima.Gateway || facts.HostAddr != lima.Gateway {
		t.Errorf("ProxyAddr %q / HostAddr %q, want both %q", facts.ProxyAddr, facts.HostAddr, lima.Gateway)
	}
	if facts.SandboxTemplate != "vm/claude-repo.yaml" || facts.ProxyTemplate != "vm/proxy.yaml" {
		t.Errorf("templates = %q, %q", facts.SandboxTemplate, facts.ProxyTemplate)
	}
	if facts.ProxyReach != backend.LoopbackForward {
		t.Error("lima reaches its proxy through a loopback forward")
	}
	if !facts.HostServices || !facts.HasSSHConfigLink {
		t.Errorf("HostServices %v, HasSSHConfigLink %v, want both", facts.HostServices, facts.HasSSHConfigLink)
	}
	if got := facts.ShellAdvice("my-api"); got != "ssh lima-my-api" {
		t.Errorf("ShellAdvice = %q", got)
	}
	if facts.ListHint != "limactl list" {
		t.Errorf("ListHint = %q", facts.ListHint)
	}
	if len(facts.Deps) == 0 || facts.Deps[0] != (backend.Dep{Tool: "limactl", Package: "lima"}) {
		t.Errorf("Deps = %v, want limactl from the lima formula first", facts.Deps)
	}
}

func TestAdviceIsSomethingYouCanType(t *testing.T) {
	facts := lima.Backend{}.Facts()
	if got, want := facts.ExecAdvice("ptrbox-proxy", "sudo", "systemctl", "status", "squid"),
		"limactl shell ptrbox-proxy -- sudo systemctl status squid"; got != want {
		t.Errorf("ExecAdvice = %q, want %q", got, want)
	}
	if got, want := facts.DeleteAdvice("ptrbox-proxy"), "limactl delete ptrbox-proxy"; got != want {
		t.Errorf("DeleteAdvice = %q, want %q", got, want)
	}
}

// captureRunner keeps the invocation it was handed, for the fields the call
// log does not carry.
type captureRunner struct{ got lima.Cmd }

func (c *captureRunner) Run(cmd lima.Cmd) error { c.got = cmd; return nil }

// A shell session lasts as long as the person in it, so it is marked
// unbounded. Nothing on this backend imposes a deadline today - multipass's
// runner does, and killed a Claude Code session at ten minutes before the
// exemption existed - and the property belongs to every backend's Shell
// rather than to the one that was caught.
func TestShellDeclaresItselfInteractive(t *testing.T) {
	r := &captureRunner{}
	b := lima.Backend{Client: &lima.Client{Runner: r, Stdout: io.Discard, Stderr: io.Discard}}
	if err := b.Shell("demo", nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !r.got.Interactive {
		t.Error("the shell session is not marked interactive, so any deadline a runner grows would cut it short")
	}
}

func TestShellGoesPastTheNarratorOnTheCallersStreams(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := limafake.New()
	fake.AddVM("demo", lima.StatusRunning)
	n := &narrator{Writer: io.Discard}
	b := lima.Backend{Client: &lima.Client{Runner: fake, Stdout: n, Stderr: n}}

	var out bytes.Buffer
	if err := b.Shell("demo", strings.NewReader("ls\n"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	// --workdir, or limactl tries the host's current directory in the guest.
	if got, want := fake.CallLog(), "shell --workdir /workspace demo"; got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if len(n.began) != 0 {
		t.Error("an interactive session was narrated: a prompt has no newline to be released by")
	}
	if !strings.Contains(out.String(), "/workspace") || len(fake.Sessions) != 1 || fake.Sessions[0].Typed != "ls\n" {
		t.Errorf("out = %q, sessions = %+v", out.String(), fake.Sessions)
	}

	fake.ShellExit = 7
	err := b.Shell("demo", nil, io.Discard, io.Discard)
	var exited interface{ ExitCode() int }
	if !errors.As(err, &exited) || exited.ExitCode() != 7 {
		t.Errorf("err = %v, want the session's exit status", err)
	}
}

func TestReadyAsksNothingOfLima(t *testing.T) {
	// A virtiofs mount is part of the VM's definition; there is nothing to
	// re-check on a running lima VM, and `ptrbox start` on one must stay the
	// no-op it has always been.
	b, fake := newBackend(t)
	fake.AddVM("demo", lima.StatusRunning)
	fake.Reset()
	if err := b.Ready("demo"); err != nil || fake.CallLog() != "" {
		t.Errorf("Ready = %v with calls %q, want nothing", err, fake.CallLog())
	}
}
