package multipass_test

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/backend"
	"github.com/PtrMzk/ptrbox/internal/multipass"
)

// script is a runner that answers each argv from a table and records what it
// was asked. Outputs are consumed in order per argv, the last one repeating,
// so a test can say what the guest showed the first time and the second.
type script struct {
	calls   []string
	stdins  []string
	outputs map[string][]string
	fails   map[string]string
	exits   map[string][]int // consumed in order like outputs; 0 is success
}

func newScript() *script {
	return &script{outputs: map[string][]string{}, fails: map[string]string{}, exits: map[string][]int{}}
}

func (s *script) Available() bool { return true }

// exitStatus is what a real *exec.ExitError gives the client: a code.
type exitStatus int

func (e exitStatus) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e exitStatus) ExitCode() int { return int(e) }

func (s *script) Run(c backend.Cmd) error {
	key := strings.Join(c.Args, " ")
	s.calls = append(s.calls, key)
	if c.Stdin != nil {
		body, _ := io.ReadAll(c.Stdin)
		s.stdins = append(s.stdins, string(body))
	}
	if outs, ok := s.outputs[key]; ok && len(outs) > 0 {
		if c.Stdout != nil {
			io.WriteString(c.Stdout, outs[0])
		}
		if len(outs) > 1 {
			s.outputs[key] = outs[1:]
		}
	}
	if stderr, ok := s.fails[key]; ok {
		if c.Stderr != nil {
			io.WriteString(c.Stderr, stderr)
		}
		return errors.New("exit status 1")
	}
	if codes, ok := s.exits[key]; ok && len(codes) > 0 {
		code := codes[0]
		if len(codes) > 1 {
			s.exits[key] = codes[1:]
		}
		if code != 0 {
			return exitStatus(code)
		}
	}
	return nil
}

func (s *script) answer(argv, out string) { s.outputs[argv] = append(s.outputs[argv], out) }

func newBackend(t *testing.T) (multipass.Backend, *script) {
	t.Helper()
	return newBackendWith(t, io.Discard)
}

func newBackendWith(t *testing.T, stdout io.Writer) (multipass.Backend, *script) {
	t.Helper()
	s := newScript()
	return multipass.Backend{Client: multipass.New(s, stdout, io.Discard)}, s
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

const (
	listArgv  = "list --format json"
	infoArgv  = "info scratch --format json"
	waitArgv  = "exec scratch --no-map-working-directory -- cloud-init status --wait"
	mountArgv = "exec scratch --no-map-working-directory -- grep -qsF  /workspace  /proc/mounts"
	// The line the capture saw for the mount, backslashes octal-escaped.
	mountLine = `:C:\134Users\134you\134code\134ptrbox-scratch /workspace fuse.sshfs rw,nosuid,nodev,relatime,user_id=0,group_id=0,allow_other 0 0` + "\n"
)

// --- listing -----------------------------------------------------------------

func TestListReadsMultipassJSON(t *testing.T) {
	b, s := newBackend(t)
	s.answer(listArgv, fixture(t, "list.json"))

	vms := b.List()
	if len(vms) != 1 || vms[0] != (backend.VM{Name: "scratch", Status: "Running"}) {
		t.Errorf("List = %v, want scratch Running", vms)
	}
	if !b.Running("scratch") || !b.Exists("scratch") || b.Status("scratch") != backend.StatusRunning {
		t.Error("the listed VM is not running/existing by name")
	}
	if b.Exists("other") || b.Status("other") != "" {
		t.Error("an unlisted VM exists")
	}
	if names := b.Names(); len(names) != 1 || names[0] != "scratch" {
		t.Errorf("Names = %v", names)
	}
}

func TestAStoppedVMIsListedAsSuch(t *testing.T) {
	b, s := newBackend(t)
	s.answer(listArgv, fixture(t, "list-stopped.json"))
	if b.Running("scratch") || !b.Exists("scratch") {
		t.Errorf("Status = %q, want existing and not running", b.Status("scratch"))
	}
}

func TestAnUnreadableListingIsNoInformation(t *testing.T) {
	b, s := newBackend(t)
	s.fails[listArgv] = "list failed: cannot connect to the multipass socket\n"
	if vms := b.List(); vms != nil {
		t.Errorf("List = %v, want nil", vms)
	}
	if b.Exists("scratch") {
		t.Error("a VM exists in a listing that failed")
	}
}

// --- lifecycle ---------------------------------------------------------------

func spec(t *testing.T) backend.Spec {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "scratch.yaml")
	if err := os.WriteFile(configPath, []byte("#cloud-config\nusers:\n  - default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return backend.Spec{
		Name: "scratch", ConfigPath: configPath,
		CPUs: 4, Memory: "8GiB", Disk: "50GiB",
		Image: "https://cloud-images.ubuntu.com/x.img", Distro: "ubuntu2404",
		Mount: &backend.Mount{Host: `C:\Users\you\code\ptrbox-scratch`, Guest: "/workspace"},
	}
}

func TestCreateLaunchesWithTheCapturedArgvThenChecksCloudInitAndTheMount(t *testing.T) {
	b, s := newBackend(t)
	sp := spec(t)
	s.answer(mountArgv, "proc /proc proc rw 0 0\n"+mountLine)

	if err := b.Create(sp); err != nil {
		t.Fatal(err)
	}
	want := []string{
		// The shape step 0 launched with: sizing in multipass's spelling, the
		// cloud-init file, the switch NIC, the mount, the alias last.
		"launch --name scratch --cpus 4 --memory 8G --disk 50G --cloud-init " + sp.ConfigPath +
			" --network name=ptrbox,mode=manual --timeout 1200 --mount " + `C:\Users\you\code\ptrbox-scratch:/workspace 24.04`,
		waitArgv,
		mountArgv,
	}
	if strings.Join(s.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(s.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestCreateRefusesADistroMultipassHasNoImageFor(t *testing.T) {
	b, s := newBackend(t)
	sp := spec(t)
	sp.Distro = "debian13"
	err := b.Create(sp)
	if err == nil || !strings.Contains(err.Error(), "debian13") || !strings.Contains(err.Error(), "ubuntu2404") {
		t.Errorf("err = %v, want a refusal naming the distro and the supported one", err)
	}
	if len(s.calls) != 0 {
		t.Errorf("multipass was called for a distro it cannot launch: %v", s.calls)
	}
}

func TestALaunchThatSkippedTheMountIsAFailedCreate(t *testing.T) {
	// Mounts disabled on the host: launch says "Skipping mount" and exits 0.
	b, s := newBackend(t)
	s.exits[mountArgv] = []int{1}
	err := b.Create(spec(t))
	if err == nil || !strings.Contains(err.Error(), "/workspace") || !strings.Contains(err.Error(), "privileged-mounts") {
		t.Errorf("err = %v, want a failure naming the mount and the setting", err)
	}
}

func TestACloudInitThatDidNotFinishCleanlyIsAFailedCreate(t *testing.T) {
	b, s := newBackend(t)
	s.fails[waitArgv] = "status: error\n"
	err := b.Create(spec(t))
	if err == nil || !strings.Contains(err.Error(), "cloud-init") {
		t.Errorf("err = %v, want cloud-init's failure", err)
	}
}

func TestACloudInitDoneWithWarningsIsDoneAndTheWarningsAreShown(t *testing.T) {
	// The capture: `status --wait` printed "status: done" and exited 2 - done
	// with recoverable errors, which are complaints about the user-data's
	// spelling. The guest's state is verify.sh's question; the complaints
	// are shown, not swallowed, and not fatal.
	var shown strings.Builder
	b, s := newBackendWith(t, &shown)
	s.answer(waitArgv, "status: done\n")
	s.exits[waitArgv] = []int{2}
	s.answer("exec scratch --no-map-working-directory -- cloud-init status --long",
		"status: done\nextended_status: degraded done\nrecoverable_errors:\nWARNING:\n  - cloud-config failed schema validation!\n")
	s.answer(mountArgv, mountLine)

	if err := b.Create(spec(t)); err != nil {
		t.Fatalf("a degraded-done cloud-init failed the create: %v", err)
	}
	if !strings.Contains(shown.String(), "failed schema validation") || !strings.Contains(shown.String(), "scratch") {
		t.Errorf("the warnings were not shown:\n%s", shown.String())
	}
}

func TestCreateWithoutAMountChecksNoMount(t *testing.T) {
	b, s := newBackend(t)
	sp := spec(t)
	sp.Mount = nil
	if err := b.Create(sp); err != nil {
		t.Fatal(err)
	}
	for _, call := range s.calls {
		if strings.Contains(call, "--mount") || strings.Contains(call, "/proc/mounts") {
			t.Errorf("a mountless VM was given or checked for a mount: %s", call)
		}
	}
}

func TestStartWaitsForCloudInitAndTrustsTheGuestAboutTheMount(t *testing.T) {
	b, s := newBackend(t)
	s.answer(infoArgv, fixture(t, "info.json"))
	s.answer(mountArgv, mountLine)

	if err := b.Start("scratch"); err != nil {
		t.Fatal(err)
	}
	want := []string{"start scratch", waitArgv, infoArgv, mountArgv}
	if strings.Join(s.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls = %v, want %v", s.calls, want)
	}
}

func TestARecordedMountTheGuestLacksGetsOneRestart(t *testing.T) {
	// Step 0, stage 9: after a host reboot the daemon brings the VM back
	// with the mount on record and absent from the guest; `restart` is what
	// re-established it.
	b, s := newBackend(t)
	s.answer(infoArgv, fixture(t, "info.json"))
	s.exits[mountArgv] = []int{1, 0}

	if err := b.Start("scratch"); err != nil {
		t.Fatal(err)
	}
	want := []string{"start scratch", waitArgv, infoArgv, mountArgv, "restart scratch", waitArgv, mountArgv}
	if strings.Join(s.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(s.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestAMountStillMissingAfterTheRestartIsAFailedStart(t *testing.T) {
	b, s := newBackend(t)
	s.answer(infoArgv, fixture(t, "info.json"))
	s.exits[mountArgv] = []int{1, 1}

	err := b.Start("scratch")
	if err == nil || !strings.Contains(err.Error(), "/workspace") {
		t.Errorf("err = %v, want a failure naming the mount", err)
	}
	if n := strings.Count(strings.Join(s.calls, "\n"), "restart scratch"); n != 1 {
		t.Errorf("restarted %d times, want exactly once", n)
	}
}

func TestStopAndDelete(t *testing.T) {
	b, s := newBackend(t)
	if err := b.Stop("scratch"); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete("scratch"); err != nil {
		t.Fatal(err)
	}
	// --purge, or the name stays taken by a recoverable VM.
	if got := strings.Join(s.calls, "\n"); got != "stop scratch\ndelete --purge scratch" {
		t.Errorf("calls = %q", got)
	}
}

func TestValidateWantsCloudConfigOnLineOne(t *testing.T) {
	b, _ := newBackend(t)
	good := filepath.Join(t.TempDir(), "good.yaml")
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(good, []byte("#cloud-config\nusers: []\n"), 0o644)
	os.WriteFile(bad, []byte("# a comment first\n#cloud-config\n"), 0o644)
	if err := b.Validate(good); err != nil {
		t.Errorf("a cloud-config was refused: %v", err)
	}
	if err := b.Validate(bad); err == nil || !strings.Contains(err.Error(), "#cloud-config") {
		t.Errorf("err = %v, want a refusal naming the header", err)
	}
	if err := b.Validate(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("a missing config validated")
	}
}

// --- in-guest execution ------------------------------------------------------

// fixScratch makes the daemon user's scratch file names predictable.
func fixScratch(t *testing.T) {
	t.Helper()
	real := multipass.ScratchName
	multipass.ScratchName = func() string { return "fixed" }
	t.Cleanup(func() { multipass.ScratchName = real })
}

const (
	outFile   = "/home/ubuntu/.ptrbox/out-fixed"
	mkdirArgv = "exec scratch --no-map-working-directory -- mkdir -p -m 0700 /home/ubuntu/.ptrbox"
	rmArgv    = "exec scratch --no-map-working-directory -- rm -f " + outFile + " " + outFile + ".err"
)

// The first real run (2026-09-20) found `multipass exec` stalling for good
// once a command wrote more than 4096 bytes to a piped stdout. So nothing a
// caller asks for crosses the exec channel: the command writes to files the
// daemon user owns, the exec carries the exit status, transfer brings the
// files back over SFTP, and they are removed.
func TestOutputRunsToAFileAndBringsItBackOverSFTP(t *testing.T) {
	b, s := newBackend(t)
	fixScratch(t)
	s.answer("transfer scratch:"+outFile+" -", "agent\n")

	out, err := b.Output("scratch", backend.Agent, "id", "-un")
	if err != nil || out != "agent\n" {
		t.Fatalf("Output = %q, %v", out, err)
	}
	want := []string{
		mkdirArgv,
		// The daemon user's shell does the redirecting, so the file is its
		// own whatever the command runs as.
		`exec scratch --no-map-working-directory -- sh -c f=$1; shift; sudo -n -u agent -H "$@" >"$f" 2>"$f.err" sh ` + outFile + " id -un",
		"transfer scratch:" + outFile + " -",
		rmArgv,
	}
	if strings.Join(s.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(s.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestALoginUserCommandRunsWithoutTheSudoWrapper(t *testing.T) {
	b, s := newBackend(t)
	fixScratch(t)
	s.answer("transfer scratch:"+outFile+" -", "ubuntu\n")
	out, err := b.Output("scratch", backend.Login, "id", "-un")
	if err != nil || out != "ubuntu\n" {
		t.Fatalf("Output = %q, %v", out, err)
	}
	if got := s.calls[1]; !strings.Contains(got, `sh -c f=$1; shift; "$@" >"$f" 2>"$f.err" sh `) || strings.Contains(got, "agent") {
		t.Errorf("exec = %q, want the command itself redirected, no sudo", got)
	}
}

func TestAFailedCommandsStderrComesBackFromItsFile(t *testing.T) {
	b, s := newBackend(t)
	fixScratch(t)
	s.fails[`exec scratch --no-map-working-directory -- sh -c f=$1; shift; "$@" >"$f" 2>"$f.err" sh `+outFile+" cat /nope"] = ""
	s.answer("transfer scratch:"+outFile+" -", "")
	s.answer("transfer scratch:"+outFile+".err -", "cat: /nope: No such file or directory\n")

	_, err := b.Output("scratch", backend.Login, "cat", "/nope")
	if err == nil || !strings.Contains(err.Error(), "No such file") ||
		!strings.Contains(err.Error(), "multipass exec scratch --no-map-working-directory -- cat /nope") {
		t.Errorf("err = %v, want the guest's stderr and the invocation a person could retype", err)
	}
	if !strings.Contains(strings.Join(s.calls, "\n"), rmArgv) {
		t.Error("the files were not removed after a failure")
	}
}

func TestStreamAndPassthroughDeliverTheCapturedOutput(t *testing.T) {
	var shown strings.Builder
	b, s := newBackendWith(t, &shown)
	fixScratch(t)
	s.answer("transfer scratch:"+outFile+" -", "line 1\nline 2\n")
	s.answer("transfer scratch:"+outFile+" -", "  sudo removed          OK\n")

	var buf strings.Builder
	if err := b.Stream("scratch", backend.Login, &buf, "tail", "-n", "2", "/var/log/x"); err != nil || buf.String() != "line 1\nline 2\n" {
		t.Errorf("Stream = %q, %v", buf.String(), err)
	}
	if err := b.Passthrough("scratch", backend.Agent, "bash", "-lc", "verify"); err != nil || !strings.Contains(shown.String(), "sudo removed") {
		t.Errorf("Passthrough shown = %q, %v", shown.String(), err)
	}
}

func TestNothingACallerAsksForCrossesTheExecChannel(t *testing.T) {
	// The invariant behind the whole detour: the only execs that may carry
	// output are the ones this package issues for itself, whose answers it
	// knows to be small.
	b, s := newBackend(t)
	fixScratch(t)
	s.answer("transfer scratch:"+outFile+" -", strings.Repeat("x", 100000))
	out, err := b.Output("scratch", backend.Login, "cat", "/etc/squid/squid.conf")
	if err != nil || len(out) != 100000 {
		t.Errorf("a large output did not come back whole: %d bytes, %v", len(out), err)
	}
	for _, call := range s.calls {
		if strings.HasPrefix(call, "exec ") && !strings.Contains(call, `>"$f"`) &&
			!strings.HasPrefix(call, mkdirArgv) && !strings.HasPrefix(call, rmArgv) {
			t.Errorf("an exec that may carry the command's output: %q", call)
		}
	}
}

func TestSendTransfersThePayloadAndRedirectsItNeverOnAnArgv(t *testing.T) {
	b, s := newBackend(t)
	err := b.Send("scratch", backend.Agent, strings.NewReader("the-secret\n"), "bash", "-c", "cat >> ~/.profile")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 3 {
		t.Fatalf("calls = %v, want mkdir, transfer, exec", s.calls)
	}
	if s.calls[0] != "exec scratch --no-map-working-directory -- mkdir -p -m 0700 /home/ubuntu/.ptrbox" {
		t.Errorf("first call = %q, want the daemon user's private directory made", s.calls[0])
	}
	if !strings.HasPrefix(s.calls[1], "transfer - scratch:/home/ubuntu/.ptrbox/stdin-") {
		t.Errorf("second call = %q, want transfer from stdin into that directory", s.calls[1])
	}
	file := strings.TrimPrefix(s.calls[1], "transfer - scratch:")
	if len(s.stdins) != 1 || s.stdins[0] != "the-secret\n" {
		t.Errorf("stdins = %q, want the payload on transfer's stdin", s.stdins)
	}
	// The exec: as the login user, sh redirecting from the file into the
	// agent's sudo, removing the file whatever happens, and the payload
	// nowhere in it.
	run := s.calls[2]
	for _, want := range []string{
		"exec scratch --no-map-working-directory -- sh -c ",
		`sudo -n -u agent -H "$@" <"$f"`, `rm -f "$f"`, `exit $s`,
		" sh " + file + " bash -c cat >> ~/.profile",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("exec = %q\nmissing %q", run, want)
		}
	}
	if strings.Contains(strings.Join(s.calls, " "), "the-secret") {
		t.Error("the payload appeared on an argv")
	}
}

func TestSendAsTheLoginUserRunsTheCommandDirectly(t *testing.T) {
	b, s := newBackend(t)
	if err := b.Send("ptrbox-proxy", backend.Login, strings.NewReader("conf"), "sudo", "tee", "/etc/squid/squid.conf"); err != nil {
		t.Fatal(err)
	}
	run := s.calls[2]
	if strings.Contains(run, "-u agent") || !strings.Contains(run, `shift; "$@" <"$f"`) {
		t.Errorf("exec = %q, want the command run as the login user itself", run)
	}
}

func TestSendFailsIfTheTransferFails(t *testing.T) {
	b, s := newBackend(t)
	s.fails["exec scratch --no-map-working-directory -- mkdir -p -m 0700 /home/ubuntu/.ptrbox"] = "boom\n"
	if err := b.Send("scratch", backend.Agent, strings.NewReader("x"), "true"); err == nil {
		t.Error("Send succeeded with no directory to transfer into")
	}
	if len(s.stdins) != 0 {
		t.Error("the payload was sent after the directory could not be made")
	}
}

func TestShellIsALoginShellAsTheAgentInTheWorkspaceOnTheCallersStreams(t *testing.T) {
	b, s := newBackend(t)
	var out strings.Builder
	s.answer("exec scratch -d /workspace -- sudo -n -u agent -H bash -l", "agent@scratch:/workspace$ ")
	if err := b.Shell("scratch", strings.NewReader("exit\n"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 1 || !strings.Contains(out.String(), "/workspace$") {
		t.Errorf("calls = %v, out = %q", s.calls, out.String())
	}
}

// --- facts -------------------------------------------------------------------

func TestFacts(t *testing.T) {
	f := multipass.Backend{}.Facts()
	if f.Name != "multipass" || f.ProxyAddr != "172.31.255.2" || f.HostAddr != "172.31.255.1" {
		t.Errorf("addresses: %+v", f)
	}
	if f.ProxyReach != backend.DirectAddress {
		t.Error("the host dials the proxy at its own address; there is no loopback forward")
	}
	if strings.Join(f.ProxyClientSrc, " ") != "127.0.0.1 172.31.255.0/24" {
		t.Errorf("ProxyClientSrc = %v", f.ProxyClientSrc)
	}
	if f.HostServices {
		t.Error("host services are offered before anything has reached the PC through the switch's firewall profile")
	}
	if f.DaemonUser != "ubuntu" || f.HasSSHConfigLink {
		t.Errorf("DaemonUser %q, HasSSHConfigLink %v", f.DaemonUser, f.HasSSHConfigLink)
	}
	if f.SandboxTemplate != "vm/claude-repo.cloud-init.yaml" || f.ProxyTemplate != "vm/proxy.cloud-init.yaml" {
		t.Errorf("templates = %q, %q", f.SandboxTemplate, f.ProxyTemplate)
	}
	// Slots: the first proxy port is .16, the sixteenth .31; never .1 or .2.
	if got := f.GuestAddr(8889); got != "172.31.255.16" {
		t.Errorf("GuestAddr(8889) = %q", got)
	}
	if got := f.GuestAddr(8904); got != "172.31.255.31" {
		t.Errorf("GuestAddr(8904) = %q", got)
	}
	if got := f.ShellAdvice("demo"); got != "ptrbox shell demo" {
		t.Errorf("ShellAdvice = %q; `multipass shell` would land as the daemon user", got)
	}
	if f.ListHint != "multipass list" {
		t.Errorf("ListHint = %q", f.ListHint)
	}
	if got := f.ExecAdvice("ptrbox-proxy", "sudo", "systemctl", "status", "squid"); got != "multipass exec ptrbox-proxy -- sudo systemctl status squid" {
		t.Errorf("ExecAdvice = %q", got)
	}
	if got := f.DeleteAdvice("ptrbox-proxy"); got != "multipass delete --purge ptrbox-proxy" {
		t.Errorf("DeleteAdvice = %q", got)
	}
	if len(f.Deps) != 1 || f.Deps[0] != (backend.Dep{Tool: "multipass", Package: "Canonical.Multipass"}) {
		t.Errorf("Deps = %v, want multipass by its winget id", f.Deps)
	}
}

// --- preflight ---------------------------------------------------------------

// withSwitchAdapter makes the host hold the switch address, or not.
func withSwitchAdapter(t *testing.T, held bool) {
	t.Helper()
	real := multipass.InterfaceAddrs
	multipass.InterfaceAddrs = func() ([]net.Addr, error) {
		addrs := []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.1.20"), Mask: net.CIDRMask(24, 32)}}
		if held {
			addrs = append(addrs, &net.IPNet{IP: net.ParseIP("172.31.255.1"), Mask: net.CIDRMask(24, 32)})
		}
		return addrs, nil
	}
	t.Cleanup(func() { multipass.InterfaceAddrs = real })
}

func readyPC(t *testing.T, s *script) {
	t.Helper()
	s.answer("networks --format json", fixture(t, "networks.json"))
	s.answer("get local.privileged-mounts", "true\n")
	withSwitchAdapter(t, true)
}

func TestAReadyPCPassesPreflight(t *testing.T) {
	b, s := newBackend(t)
	readyPC(t, s)
	if err := b.Preflight(); err != nil {
		t.Errorf("Preflight = %v on a PC with the switch, the address and mounts on", err)
	}
}

func TestAMissingSwitchIsNamedWithTheTwoLinesThatCreateIt(t *testing.T) {
	b, s := newBackend(t)
	readyPC(t, s)
	s.outputs["networks --format json"] = []string{`{"list":[{"description":"Virtual Switch with internal networking","name":"Default Switch","type":"switch"}]}`}
	err := b.Preflight()
	if err == nil {
		t.Fatal("Preflight passed with no ptrbox switch")
	}
	for _, want := range []string{"New-VMSwitch -Name ptrbox -SwitchType Internal",
		`New-NetIPAddress -InterfaceAlias "vEthernet (ptrbox)" -IPAddress 172.31.255.1 -PrefixLength 24`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v\nmissing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "privileged-mounts") {
		t.Error("mounts were blamed when only the switch is missing")
	}
}

func TestASwitchWithoutTheHostAddressIsNamed(t *testing.T) {
	b, s := newBackend(t)
	readyPC(t, s)
	withSwitchAdapter(t, false)
	err := b.Preflight()
	if err == nil || !strings.Contains(err.Error(), "no address on it") || !strings.Contains(err.Error(), "New-NetIPAddress") {
		t.Errorf("err = %v, want the address line", err)
	}
}

func TestDisabledMountsAreNamedWithTheSettingAndItsCost(t *testing.T) {
	b, s := newBackend(t)
	readyPC(t, s)
	s.outputs["get local.privileged-mounts"] = []string{"false\n"}
	err := b.Preflight()
	if err == nil || !strings.Contains(err.Error(), "multipass set local.privileged-mounts=true") || !strings.Contains(err.Error(), "restarts the multipass daemon") {
		t.Errorf("err = %v, want the setting and that it restarts the daemon", err)
	}
	if strings.Contains(err.Error(), "New-VMSwitch") {
		t.Error("the switch was blamed when only mounts are off")
	}
}

func TestPreflightNeverRunsAnythingButTwoReadOnlyQueries(t *testing.T) {
	b, s := newBackend(t)
	readyPC(t, s)
	s.outputs["get local.privileged-mounts"] = []string{"false\n"}
	_ = b.Preflight()
	if got := strings.Join(s.calls, "\n"); got != "networks --format json\nget local.privileged-mounts" {
		t.Errorf("calls = %q; ptrbox never changes a host setting itself", got)
	}
}
