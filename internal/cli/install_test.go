package cli

// ptrbox install against the fakes.
//
// Install's job since the proxy moved into a VM: seed the host-side allowlist,
// provision/start the ptrbox-proxy VM, push a validated squid config into it,
// and wire up ssh + PATH. The security-relevant parts are the ones about the
// pushed config: it is validated as a candidate before activation, a rejected
// config never lands, and the user's allowlist is never overwritten.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
)

// --- fresh install -----------------------------------------------------------

func TestFreshInstallSeedsTheAllowlistAndProvisionsTheProxyVM(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	if !h.exists(config.AllowlistPath()) {
		t.Error("no host allowlist")
	}
	h.assertCalled("start --name ptrbox-proxy")
	if h.fake.VMStatus(config.ProxyVM) != lima.StatusRunning {
		t.Error("the proxy is not running")
	}
}

func TestInstallPushesTheRenderedSquidConfigIntoTheProxyVM(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	conf := h.proxyFile("/etc/squid/squid.conf")
	if !strings.Contains(conf, "http_port 8888") {
		t.Error("no http_port in the pushed config")
	}
	if !strings.Contains(conf, "/etc/squid/allowed_domains.txt") {
		t.Error("the pushed config does not reference the allowlist")
	}
	if matches(conf, `__[A-Z][A-Z0-9_]*__`) {
		t.Error("the pushed config still has placeholders")
	}
}

func TestInstallPushesTheHostAllowlistIntoTheProxyVM(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	if h.proxyFile("/etc/squid/allowed_domains.txt") != h.allowlist() {
		t.Error("the VM allowlist differs from the host's")
	}
	if !strings.Contains(h.allowlist(), "api.anthropic.com") {
		t.Error("the seeded allowlist has no Claude API entry")
	}
}

func TestInstallCreatesTheDirectoriesPtrboxNeeds(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	for _, dir := range []string{
		h.repos,
		config.GeneratedDir(),
		config.Dir(),
		config.VMDir(),
		filepath.Join(h.home, ".ssh", "config.d"),
	} {
		if !h.exists(dir) {
			t.Errorf("%s was not created", dir)
		}
	}
}

// --- the config directory ----------------------------------------------------

// Present is not the same as ready. Anything install leaves you to assemble by
// hand - mkdir this, copy that - is a setup step with no test, done from a doc,
// and the first thing a new user gets wrong.
func TestInstallLeavesTheConfigDirectoryReadyToEdit(t *testing.T) {
	h := newHarness(t)
	h.noConfigFile()
	h.mustRun("install")

	for _, path := range []string{
		config.Path(),
		config.VMDir(),
		filepath.Join(config.VMDir(), "README.txt"),
	} {
		if !h.exists(path) {
			t.Errorf("%s was not created", path)
		}
	}
	// And it says where, so the summary is the answer to "where do I change
	// this" rather than a doc search.
	if !strings.Contains(h.stderr, config.Path()) {
		t.Errorf("the summary does not name the config file:\n%s", h.stderr)
	}
	if !strings.Contains(h.stderr, config.VMDir()) {
		t.Errorf("the summary does not name the per-VM directory:\n%s", h.stderr)
	}
}

// What lands is the annotated example, byte for byte - which is what makes it
// editable: every key present, every line commented. The config package
// asserts those two properties of the example itself.
func TestTheSeededConfigFileIsTheShippedExample(t *testing.T) {
	h := newHarness(t)
	h.noConfigFile()
	h.mustRun("install")

	want, err := fs.ReadFile(ptrbox.Assets, "config/ptrbox.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, config.Path()); got != string(want) {
		t.Error("the seeded config file is not the shipped example")
	}
	// Inert as seeded: a fresh install must resolve to the built-in defaults.
	if cfg := h.mustLoadConfig(); cfg.CPUs != 4 || cfg.Distro != "debian13" {
		t.Errorf("the seeded config changed the defaults: CPUs=%d distro=%s",
			cfg.CPUs, cfg.Distro)
	}
}

// install is idempotent and people re-run it after every rebuild. Overwriting
// settings would make it the one command that silently discards your work.
func TestInstallNeverOverwritesAnExistingConfigFile(t *testing.T) {
	h := newHarness(t)
	const mine = "PTRBOX_MEMORY=16GiB\n"
	writeFile(t, config.Path(), mine)
	writeFile(t, filepath.Join(config.VMDir(), "README.txt"), "mine too\n")

	h.mustRun("install")

	if got := readFile(t, config.Path()); got != mine {
		t.Errorf("install rewrote the config file:\n%s", got)
	}
	if got := readFile(t, filepath.Join(config.VMDir(), "README.txt")); got != "mine too\n" {
		t.Errorf("install rewrote the per-VM README:\n%s", got)
	}
}

func TestTheSeededConfigDirIsRecordedInTheManifest(t *testing.T) {
	h := newHarness(t)
	h.noConfigFile()
	h.mustRun("install", "--yes")

	manifest := readFile(t, filepath.Join(config.Dir(), "install-manifest"))
	for _, path := range []string{config.Path(), filepath.Join(config.VMDir(), "README.txt")} {
		if !strings.Contains(manifest, "wrote "+path) {
			t.Errorf("%s is not in the manifest:\n%s", path, manifest)
		}
	}
}

// The README sits in the directory ptrbox looks up per-VM config files in, so
// it must be impossible to read as one - on every filesystem. The first
// construction ("VMName lowercases, so no VM can be called README") held only
// where names are case-sensitive: on the Mac, APFS case-folds, vms/readme
// resolved to vms/README, and a sandbox legitimately called readme choked on
// prose as its config - which is how this test failed there and why the name
// now carries a dot. VMName strips dots, so no legal VM name contains one,
// and no amount of case folding puts one back.
func TestTheVMsREADMEIsUnreachableAsAPerVMConfig(t *testing.T) {
	h := newHarness(t)
	h.noConfigFile()
	h.mustRun("install")

	const readme = "README.txt"
	if !h.exists(filepath.Join(config.VMDir(), readme)) {
		t.Fatal("the README this test is about was not written")
	}
	// The load-bearing property, filesystem-independent: the name a VM would
	// need to reach this file - its lowercased form, which is what a
	// case-insensitive lookup treats as the same file - is not a name VMName
	// can produce.
	name, err := config.VMName(readme)
	if err != nil {
		t.Fatal(err)
	}
	if name == strings.ToLower(readme) {
		t.Fatalf("VMName(%q) = %q: a VM of that name would read the README as its config", readme, name)
	}
	cfg := h.mustLoadConfig()
	if _, err := cfg.Overlay(readme); err == nil {
		t.Error("Overlay accepted README.txt as a VM name")
	}
	// And the VM you actually get from the argument "README.txt" reads a
	// different file, which does not exist - so it resolves to the defaults.
	overlaid, err := cfg.Overlay(name)
	if err != nil {
		t.Fatal(err)
	}
	if overlaid.Memory != cfg.Memory {
		t.Errorf("the README leaked into VM %q: memory %q", name, overlaid.Memory)
	}
}

func TestTheGeneratedProxyConfigHasNoMountsAndALoopbackForward(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	body := h.generated(config.ProxyVM)
	if !strings.Contains(body, "\nmounts: []") {
		t.Error("the proxy VM declares mounts")
	}
	if !strings.Contains(body, `hostIP: "127.0.0.1"`) {
		t.Error("the proxy VM's forward is not loopback-bound")
	}
}

func TestInstallRestartsSquidAfterPushingANewConfig(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	h.assertCalled("sudo systemctl restart squid")
}

// --- idempotence -------------------------------------------------------------

func TestASecondInstallChangesNothingAndSaysSo(t *testing.T) {
	h := newHarness(t)
	h.noConfigFile()
	h.mustRun("install")
	h.fake.Reset()

	h.mustRun("install")
	h.assertOutputContains("already set up")
	h.assertNotCalled("^start")
	h.assertNotCalled("systemctl restart")
	h.assertNotCalled("squid -k reconfigure")
	// The seeded files are announced on the run that writes them and never
	// again - re-running install after a rebuild is the common case.
	if strings.Contains(h.stderr, "installed "+config.Path()) {
		t.Errorf("the second install re-announced the config file:\n%s", h.stderr)
	}
}

// --- validation --------------------------------------------------------------

func TestInstallValidatesTheCandidateConfigBeforeActivatingIt(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	// -f names the candidate: the live path must never be the thing under test.
	h.assertCalled("sudo squid -f /etc/squid/squid.conf.ptrbox-new -k parse")
	h.assertOrder(`squid -f .* -k parse`, `sudo mv /etc/squid/squid.conf.ptrbox-new /etc/squid/squid.conf`)
	h.assertOrder(`sudo mv .*squid.conf`, `sudo systemctl restart squid`)
}

func TestAConfigSquidRejectsIsNeverActivated(t *testing.T) {
	h := newHarness(t)
	h.fake.SquidParseFails = true

	err := h.run("install")
	if err == nil || !strings.Contains(err.Error(), "squid rejected") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := h.fake.ReadFile(config.ProxyVM, "/etc/squid/squid.conf"); ok {
		t.Error("a rejected config was activated")
	}
	// And no half-pushed candidate left lying around in the VM.
	if _, ok := h.fake.ReadFile(config.ProxyVM, "/etc/squid/squid.conf.ptrbox-new"); ok {
		t.Error("the candidate was left behind")
	}
	h.assertNotCalled("systemctl restart")
}

// --- the egress path ---------------------------------------------------------
//
// Install's success message is a claim about egress, so it is gated on the
// same kind of assertions vm/verify.sh makes about a sandbox. Parsing a config
// is not evidence that traffic moves.

func TestInstallVerifiesTheEgressPathBeforeClaimingSuccess(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	h.assertCalled(`shell ptrbox-proxy -- bash -lc`)
	h.assertOrder(`sudo systemctl restart squid`, `shell ptrbox-proxy -- bash -lc`)
	h.assertOutputContains("host setup complete")
}

func TestAProxyThatFailsVerificationFailsTheInstall(t *testing.T) {
	// The hole this closes: squid parses its config, dies on start, and
	// install reports success. Nothing downstream notices until an agent has
	// no network.
	h := newHarness(t)
	h.fake.ProxyVerifyFails = true

	err := h.run("install")
	if err == nil {
		t.Fatal("install succeeded with a proxy that is not serving egress")
	}
	if !strings.Contains(err.Error(), "no network") {
		t.Errorf("the error does not say what it costs the user: %v", err)
	}
	if strings.Contains(h.output(), "setup complete") {
		t.Errorf("install claimed success anyway:\n%s", h.output())
	}
}

func TestVerificationFailsBeforeInstallAsksForAnything(t *testing.T) {
	// A broken install has nothing to offer a PATH symlink for, and a prompt
	// is the worst place to learn the thing you are configuring does not work.
	h := newHarness(t)
	h.fake.ProxyVerifyFails = true

	if err := h.run("install", "--yes"); err == nil {
		t.Fatal("install succeeded")
	}
	if h.exists(filepath.Join(h.home, "bin", "ptrbox")) {
		t.Error("install linked ptrbox onto PATH despite failing verification")
	}
}

func TestADeadPortForwardFailsTheInstall(t *testing.T) {
	// squid can be perfectly healthy inside the VM while Lima never published
	// the forward - and the sandboxes reach squid only through the forward.
	// This is the half of the path the VM cannot see.
	h := newHarness(t)
	h.portInUse = func(int) bool { return false }

	err := h.run("install")
	if err == nil {
		t.Fatal("install succeeded with no port forward")
	}
	for _, want := range []string{"127.0.0.1:8888", "port forward is not up"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(h.output(), "setup complete") {
		t.Error("install claimed success anyway")
	}
}

func TestAForwardStillReappearingAfterTheSquidRestartIsWaitedFor(t *testing.T) {
	// Ensure restarts squid whenever the pushed config changed, and Lima
	// re-publishes the forward only once squid is listening again - so right
	// after the restart, the honest answer at the host port is "not yet".
	// That must read as a beat of patience, not a failed install: the
	// one-shot version of this check failed a healthy proxy on the first
	// smoke run whose config actually changed.
	h := newHarness(t)
	// Per port, not per call: preflight asks about every port in the block
	// once, and only the verification polls one port repeatedly - which is
	// the behaviour under test.
	calls := map[int]int{}
	h.portInUse = func(port int) bool { calls[port]++; return calls[port] > 3 }

	h.mustRun("install")
	if calls[8888] <= 3 {
		t.Fatalf("portInUse answered %d times for the base port, so the wait was never exercised", calls[8888])
	}
}

func TestAnInstallWithNothingToDoStillVerifies(t *testing.T) {
	// "already set up" is a claim about the egress path too, and the run that
	// changes nothing is the one people make when something is wrong.
	h := newHarness(t)
	h.mustRun("install")
	h.fake.Reset()
	h.fake.ProxyVerifyFails = true

	if err := h.run("install"); err == nil {
		t.Fatal("a no-op install skipped verification")
	}
	h.assertCalled(`shell ptrbox-proxy -- bash -lc`)
	if strings.Contains(h.output(), "already set up") {
		t.Error("install reported a healthy setup it had not checked")
	}
}

// --- preflight ---------------------------------------------------------------
//
// What install can know before it provisions anything, said before it does.

func TestAForeignListenerOnTheProxyPortStopsTheInstall(t *testing.T) {
	// With the proxy VM down, nothing of ptrbox's holds that port - so
	// whatever does would end up receiving every sandbox's egress.
	h := newHarness(t)
	h.portInUse = func(int) bool { return true }

	err := h.run("install")
	if err == nil {
		t.Fatal("install proceeded over a foreign listener on the proxy port")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:8888") {
		t.Errorf("the error does not name the port: %v", err)
	}
	// And it stopped before spending minutes provisioning a VM whose forward
	// could not bind.
	h.assertNotCalled("^start")
}

func TestTheProxysOwnForwardIsNotMistakenForAConflict(t *testing.T) {
	// The same observation means the opposite thing once the proxy is up,
	// which is why the question is only asked beforehand. A second install
	// must not trip over the forward the first one created.
	h := newHarness(t)
	h.mustRun("install")
	if h.fake.VMStatus(config.ProxyVM) != lima.StatusRunning {
		t.Fatal("the proxy is not running, so this proves nothing")
	}
	h.mustRun("install")
}

func TestInstallNoLongerReportsItsOwnPortForwardAsANote(t *testing.T) {
	// It used to say "something is listening on port 8888 (expected: the
	// ptrbox-proxy port forward)" - true on every healthy run, and therefore
	// silent on a broken one.
	h := newHarness(t)
	h.mustRun("install")
	if strings.Contains(h.output(), "something is listening") {
		t.Errorf("install still reports the expected case:\n%s", h.output())
	}
}

func TestAMissingKeychainEntryIsReportedBeforeProvisioning(t *testing.T) {
	// The warning is only useful in time to act on it. Proved by breaking the
	// provisioning: whatever install says before that point, it said early.
	h := newHarness(t)
	h.keychain.token = ""
	h.fake.StartFails = true

	if err := h.run("install"); err == nil {
		t.Fatal("install succeeded with a proxy VM that would not start")
	}
	h.assertOutputContains("no Keychain entry")
	h.assertOutputContains("claude setup-token")
}

func TestAHostWithNoKeychainIsReportedBeforeProvisioning(t *testing.T) {
	h := newHarness(t)
	h.keychain.available = false
	h.fake.StartFails = true

	if err := h.run("install"); err == nil {
		t.Fatal("install succeeded with a proxy VM that would not start")
	}
	h.assertOutputContains("no macOS Keychain")
}

// --- the allowlist -----------------------------------------------------------

func TestAnExistingAllowlistIsNeverOverwrittenByInstall(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	if err := os.WriteFile(config.AllowlistPath(), []byte("my.private.registry\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.mustRun("install")
	if h.allowlist() != "my.private.registry\n" {
		t.Errorf("the allowlist was rewritten: %q", h.allowlist())
	}
	h.assertOutputContains("differs from the shipped allowlist")
	// ...and the user's version is what reaches the proxy VM.
	if !containsLine(h.proxyFile("/etc/squid/allowed_domains.txt"), "my.private.registry") {
		t.Error("the user's allowlist did not reach the VM")
	}
}

func TestInstallNamesTheShippedEntriesYouAreMissing(t *testing.T) {
	// The shipped list is embedded, so there is no file to diff against; the
	// point of the old diff hint was seeing what an upgrade added.
	h := newHarness(t)
	h.mustRun("install")
	if err := os.WriteFile(config.AllowlistPath(), []byte("my.private.registry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.mustRun("install")
	h.assertOutputContains("shipped entries yours does not have:")
	h.assertOutputContains("api.anthropic.com")
}

func TestAnAllowlistOnlyChangeReloadsRatherThanRestartingSquid(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	appendLine(t, config.AllowlistPath(), "my.private.registry")
	h.fake.Reset()

	h.mustRun("install")
	// A restart severs every live VM tunnel; reconfigure drops nothing.
	h.assertCalled("sudo squid -k reconfigure")
	h.assertNotCalled("systemctl restart")
}

// --- ssh ---------------------------------------------------------------------

func TestInstallAddsTheSSHIncludeLine(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	if !strings.Contains(readFile(t, filepath.Join(h.home, ".ssh", "config")), "Include config.d/*") {
		t.Error("no Include line")
	}
}

func TestTheSSHIncludeIsAddedExactlyOnce(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	h.mustRun("install")
	body := readFile(t, filepath.Join(h.home, ".ssh", "config"))
	if n := strings.Count(body, "Include config.d/*"); n != 1 {
		t.Errorf("the Include line appears %d times", n)
	}
}

func TestTheSSHIncludeSurvivesAnExistingConfigAndGoesFirst(t *testing.T) {
	h := newHarness(t)
	write(t, filepath.Join(h.home, ".ssh", "config"), "Host example\n  User me\n")
	h.mustRun("install")

	body := readFile(t, filepath.Join(h.home, ".ssh", "config"))
	if !strings.HasPrefix(body, "Include config.d/*\n") {
		t.Errorf("the Include line is not first:\n%s", body)
	}
	if !strings.Contains(body, "Host example") {
		t.Error("the existing config was lost")
	}
}

func TestTheSSHIncludeHandlesAnEmptyConfigFile(t *testing.T) {
	h := newHarness(t)
	write(t, filepath.Join(h.home, ".ssh", "config"), "")
	h.mustRun("install")
	if !strings.Contains(readFile(t, filepath.Join(h.home, ".ssh", "config")), "Include config.d/*") {
		t.Error("no Include line")
	}
}

// --- PATH symlink ------------------------------------------------------------

func TestYesSymlinksPtrboxOntoPath(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install", "--yes")
	target := filepath.Join(h.home, "bin", "ptrbox")
	got, err := os.Readlink(target)
	if err != nil {
		t.Fatalf("no symlink at %s: %v", target, err)
	}
	if got != h.exe {
		t.Errorf("symlink points at %q, want %q", got, h.exe)
	}
}

func TestTheSymlinkIsNotCreatedWithoutConsent(t *testing.T) {
	// No tty in tests, so the prompt declines by default.
	h := newHarness(t)
	h.mustRun("install")
	if h.exists(filepath.Join(h.home, "bin", "ptrbox")) {
		t.Error("a symlink was created without consent")
	}
	h.assertOutputContains("symlink ptrbox into")
}

func TestAnExistingCorrectSymlinkIsNotRelinked(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install", "--yes")
	h.mustRun("install", "--yes")
	if n := h.manifestLinks(); n != 1 {
		t.Errorf("the symlink was recorded %d times, want 1 - the second install re-did it", n)
	}
	// Not re-done is not the same as not reported: the step still says what
	// it found, so it cannot read as a step that was skipped.
	h.assertOutputContains("already links to this ptrbox")
}

// --- the PATH entry ----------------------------------------------------------
//
// The link and the PATH entry are two separate facts. ptrbox arranges both -
// it already prepends an Include to ~/.ssh/config unprompted, so printing a
// line for the user to paste into the other dotfile was an inconsistency
// rather than a boundary - but the second one is asked for first, and the
// re-exec at the end is nobody's to do but the user's.

func (h *harness) zshrc() string {
	h.t.Helper()
	body, err := os.ReadFile(filepath.Join(h.home, ".zshrc"))
	if err != nil {
		return ""
	}
	return string(body)
}

func TestYesPutsTheBinDirOnPathForRealNotAsAdvice(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install", "--yes")

	want := `export PATH="` + filepath.Join(h.home, "bin") + `:$PATH"`
	if !containsLine(h.zshrc(), want) {
		t.Errorf("~/.zshrc does not contain %q:\n%s", want, h.zshrc())
	}
	// The one step a child process genuinely cannot take for you.
	h.assertOutputContains("exec zsh")
}

func TestThePathLineIsAddedExactlyOnce(t *testing.T) {
	// Idempotent like the ssh Include: a second install must not append a
	// second copy. And because the file is only read by shells started after
	// it was written, the re-run's news is "restart your shell", not "added".
	h := newHarness(t)
	h.mustRun("install", "--yes")
	h.mustRun("install", "--yes")

	want := `export PATH="` + filepath.Join(h.home, "bin") + `:$PATH"`
	if n := strings.Count(h.zshrc(), want); n != 1 {
		t.Errorf("the PATH line appears %d times, want 1:\n%s", n, h.zshrc())
	}
	h.assertOutputContains("but not in this shell")
}

func TestTheAppendDoesNotLandOnSomebodysLastLine(t *testing.T) {
	h := newHarness(t)
	write(t, filepath.Join(h.home, ".zshrc"), "alias ll='ls -l'") // no trailing newline
	h.mustRun("install", "--yes")

	if !containsLine(h.zshrc(), "alias ll='ls -l'") {
		t.Errorf("the existing last line was damaged:\n%q", h.zshrc())
	}
	if !containsLine(h.zshrc(), `export PATH="`+filepath.Join(h.home, "bin")+`:$PATH"`) {
		t.Errorf("no PATH line:\n%q", h.zshrc())
	}
}

func TestAStartupFileIsNotTouchedWithoutConsent(t *testing.T) {
	// The link already exists, so the PATH offer is the only question left -
	// and with no tty it declines, printing the line for the user to place
	// themselves. A shell startup file is somewhere a bad edit costs you
	// every new terminal, so it is asked for like the symlink is.
	h := newHarness(t)
	link := filepath.Join(h.home, "bin", "ptrbox")
	mkdir(t, filepath.Dir(link))
	if err := os.Symlink(h.exe, link); err != nil {
		t.Fatal(err)
	}

	h.mustRun("install")
	if h.exists(filepath.Join(h.home, ".zshrc")) {
		t.Error("install wrote to ~/.zshrc without consent")
	}
	h.assertOutputContains(`export PATH="` + filepath.Join(h.home, "bin") + `:$PATH"`)
}

func TestAShellPtrboxCannotEditGetsToldRatherThanEdited(t *testing.T) {
	// fish would not even parse an export line, and has its own command for
	// this. Guessing at a file for a shell ptrbox does not know is how you
	// leave inert cruft in somebody's config.
	h := newHarness(t)
	t.Setenv("SHELL", "/opt/homebrew/bin/fish")

	h.mustRun("install", "--yes")
	h.assertOutputContains("fish_add_path " + filepath.Join(h.home, "bin"))
	if h.exists(filepath.Join(h.home, ".zshrc")) {
		t.Error("install wrote a zsh file for a fish user")
	}
}

func TestBashGetsBashProfile(t *testing.T) {
	// macOS terminals start login shells, which read .bash_profile and never
	// .bashrc - a line in the wrong one is inert and looks done.
	h := newHarness(t)
	t.Setenv("SHELL", "/bin/bash")

	h.mustRun("install", "--yes")
	if !strings.Contains(readFile(t, filepath.Join(h.home, ".bash_profile")), "export PATH=") {
		t.Error("no PATH line in ~/.bash_profile")
	}
	if h.exists(filepath.Join(h.home, ".bashrc")) {
		t.Error("install wrote .bashrc, which a login shell never reads")
	}
}

func TestABinDirOnPathIsNotNaggedAbout(t *testing.T) {
	// The offer has to stay silent when there is nothing wrong, or the run
	// where something IS wrong says nothing new.
	h := newHarness(t)
	binDir := filepath.Join(h.tmp, "on-path")
	t.Setenv("PTRBOX_BIN_DIR", binDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	h.mustRun("install", "--yes")
	h.mustRun("install", "--yes")
	if strings.Contains(h.output(), "not on your PATH") {
		t.Errorf("install complained about a directory that is on PATH:\n%s", h.output())
	}
	if h.exists(filepath.Join(h.home, ".zshrc")) {
		t.Error("install edited a startup file it had no reason to")
	}
}

func TestAnAlreadyInstalledBinaryStillGetsItsDirectoryOnPath(t *testing.T) {
	// `go install` puts ptrbox in a bin dir already, so there is nothing to
	// link - but whether that dir is searched is the same open question.
	h := newHarness(t)
	t.Setenv("PTRBOX_BIN_DIR", filepath.Dir(h.exe))

	h.mustRun("install", "--yes")
	h.assertOutputContains("already installed at")
	if !containsLine(h.zshrc(), `export PATH="`+filepath.Dir(h.exe)+`:$PATH"`) {
		t.Errorf("the installed binary's directory was not put on PATH:\n%s", h.zshrc())
	}
}

func TestTheStartupFileEditIsRecordedInTheManifest(t *testing.T) {
	// Everything ptrbox writes outside its own directories goes in the
	// manifest, so a future uninstall does not have to guess.
	h := newHarness(t)
	h.mustRun("install", "--yes")

	manifest := readFile(t, filepath.Join(config.Dir(), "install-manifest"))
	if !strings.Contains(manifest, filepath.Join(h.home, ".zshrc")) {
		t.Errorf("the startup file edit is not in the manifest:\n%s", manifest)
	}
}

func TestAForeignFileAtTheTargetIsNotClobberedWithoutConsent(t *testing.T) {
	h := newHarness(t)
	target := filepath.Join(h.home, "bin", "ptrbox")
	write(t, target, "#!/bin/sh\necho someone elses ptrbox\n")

	h.mustRun("install", "--no-input")
	if readFile(t, target) != "#!/bin/sh\necho someone elses ptrbox\n" {
		t.Error("the foreign file was replaced")
	}
	h.assertOutputContains("already exists and is not this ptrbox")
}

func TestYesReplacesAForeignFileAtTheTarget(t *testing.T) {
	h := newHarness(t)
	target := filepath.Join(h.home, "bin", "ptrbox")
	write(t, target, "#!/bin/sh\n")

	h.mustRun("install", "--yes")
	if _, err := os.Readlink(target); err != nil {
		t.Errorf("the target is not a symlink: %v", err)
	}
}

func TestTheSymlinkTargetHonoursBinDir(t *testing.T) {
	h := newHarness(t)
	t.Setenv("PTRBOX_BIN_DIR", filepath.Join(h.tmp, "somewhere", "bin"))
	h.mustRun("install", "--yes")
	if _, err := os.Readlink(filepath.Join(h.tmp, "somewhere", "bin", "ptrbox")); err != nil {
		t.Errorf("no symlink in the configured bin dir: %v", err)
	}
}

func TestABinDirThatIsNotOnPathIsCalledOut(t *testing.T) {
	h := newHarness(t)
	t.Setenv("PTRBOX_BIN_DIR", filepath.Join(h.tmp, "not-on-path"))
	h.mustRun("install", "--yes")
	h.assertOutputContains("not on your PATH")
}

func TestAnAlreadyInstalledBinaryIsNotLinkedToItself(t *testing.T) {
	// `go install` puts ptrbox in a bin dir already; offering to link it to
	// where it already is would be a loop, not a favour.
	h := newHarness(t)
	t.Setenv("PTRBOX_BIN_DIR", filepath.Dir(h.exe))
	h.mustRun("install", "--yes")

	// The harm this prevents is concrete: linking the target to itself means
	// removing the binary and leaving a symlink pointing at nothing.
	info, err := os.Lstat(h.exe)
	if err != nil {
		t.Fatalf("the binary is gone: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("ptrbox replaced itself with a symlink to itself")
	}
	if n := h.manifestLinks(); n != 0 {
		t.Errorf("ptrbox recorded %d links, want 0", n)
	}
}

// --- dependencies ------------------------------------------------------------

func TestAMissingDependencyStopsTheInstallAndNamesTheFormula(t *testing.T) {
	h := newHarness(t)
	h.missing["limactl"] = true

	err := h.run("install")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v, want ErrReported (the message is already on screen)", err)
	}
	h.assertOutputContains("missing dependencies: limactl")
	// Said once, not once by the command and again by main.
	if n := strings.Count(h.output(), "missing dependencies"); n != 1 {
		t.Errorf("the diagnosis appears %d times, want 1:\n%s", n, h.output())
	}
	// ptrbox never installs packages itself - it prints the command and stops.
	h.assertOutputContains("brew install lima")
	if h.exists(config.AllowlistPath()) {
		t.Error("install got as far as seeding the allowlist")
	}
}

func TestHostSquidIsNoLongerADependency(t *testing.T) {
	// The proxy VM apt-installs its own squid; requiring one on the Mac would
	// make people set up a daemon nothing uses.
	h := newHarness(t)
	h.mustRun("install")
	if strings.Contains(h.output(), "missing dependencies") {
		t.Errorf("install reported missing dependencies:\n%s", h.output())
	}
}

func TestEveryDeclaredDependencyIsActuallyUsed(t *testing.T) {
	// A dependency nobody calls is a gratuitous install failure: preflight is
	// a hard blocker, so listing a tool ptrbox never runs stops people setting
	// up for no reason.
	// install.go itself is excluded: the declaration must not count as a use.
	sources := goSources(t, "..", "install.go")
	for _, dep := range deps {
		if !strings.Contains(sources, `"`+dep.tool+`"`) {
			t.Errorf("%s is declared in deps but never called", dep.tool)
		}
	}
}

// --- manifest ----------------------------------------------------------------

func TestInstallRecordsWhatItTouched(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install", "--yes")
	manifest := readFile(t, filepath.Join(config.Dir(), "install-manifest"))
	if !strings.Contains(manifest, "wrote "+config.AllowlistPath()) {
		t.Errorf("the seeded allowlist is not in the manifest:\n%s", manifest)
	}
	if !strings.Contains(manifest, "linked "+filepath.Join(h.home, "bin", "ptrbox")) {
		t.Errorf("the symlink is not in the manifest:\n%s", manifest)
	}
}

// --- helpers -----------------------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// goSources concatenates every non-test .go file under dir, for assertions
// about the code itself. Named files are skipped.
func goSources(t *testing.T, dir string, skip ...string) string {
	t.Helper()
	skipped := map[string]bool{}
	for _, name := range skip {
		skipped[name] = true
	}
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") || skipped[d.Name()] {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.Write(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestTheSSHConfigIsNotLeftWorldReadable(t *testing.T) {
	// ssh is particular about this one, and an existing file keeps its mode
	// through a rewrite unless something says otherwise.
	h := newHarness(t)
	path := filepath.Join(h.home, ".ssh", "config")
	write(t, path, "Host example\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	h.mustRun("install")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("~/.ssh/config is mode %o, want 600", info.Mode().Perm())
	}
}
