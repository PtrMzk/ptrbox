package cli

// Git hooks on the host clone.
//
// `ptrbox new` points the repo's core.hooksPath at /dev/null, because
// .git/hooks is inside the one mount and a hook executes on the MAC when you
// run git there - the shortest path from "the agent wrote a file" to "code ran
// outside the sandbox".
//
// It is a redirect, not a deletion: core.hooksPath changes which directory git
// looks in, /dev/null is a character device so every lookup misses, and git
// treats a missing hook as no hook. The files are untouched and `git config
// --unset core.hooksPath` restores all of them. These tests hold that line,
// because "we disabled your hooks" and "we deleted your hooks" are very
// different sentences and only one of them is true.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/proxy"
)

// gitConfigOrEmpty is gitConfig for a key that may legitimately be unset -
// `git config <key>` exits non-zero when it is, which the shared helper
// treats as a fatal error because its callers are asserting a value.
func gitConfigOrEmpty(t *testing.T, dir, key string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "config", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// withHook plants an executable hook in a repo the harness will sandbox, and
// returns the path so a test can prove it survived.
func withHook(t *testing.T, h *harness, repo, name string) string {
	t.Helper()
	dir := filepath.Join(h.repos, repo, ".git", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho fired\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The property the whole approach rests on.
func TestNeutralisingHooksDoesNotDeleteThem(t *testing.T) {
	h := newHarness(t)
	// git init first, so .git/hooks exists to plant into.
	h.mustRun("new", "demo")
	path := withHook(t, h, "demo", "pre-commit")
	before := readFile(t, path)

	// A second create against the same repo re-runs the neutralising.
	h.mustRun("rm", "demo")
	h.mustRun("new", "demo")

	if got := readFile(t, path); got != before {
		t.Errorf("the hook file changed:\n%s", got)
	}
	if got := gitConfig(t, filepath.Join(h.repos, "demo"), "core.hooksPath"); got != "/dev/null" {
		t.Errorf("core.hooksPath = %q, want /dev/null", got)
	}
}

// A repo with hooks says so, names them, and says how to undo it. Silence here
// is the actual complaint: a pre-commit setup stops firing and nothing tells
// you, and `pre-commit install` afterwards looks like it worked.
func TestNewNamesTheHooksItIsSwitchingOff(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	withHook(t, h, "demo", "pre-commit")
	withHook(t, h, "demo", "pre-push")
	h.mustRun("rm", "demo")
	h.stderr = ""
	h.mustRun("new", "demo")

	for _, want := range []string{"pre-commit", "pre-push", "kept", "--unset core.hooksPath"} {
		if !strings.Contains(h.stderr, want) {
			t.Errorf("the plan does not mention %q:\n%s", want, h.stderr)
		}
	}
}

// A fresh repo ships .sample templates and nothing else. Reporting those would
// make the line noise on every first create, which is how a warning stops
// being read.
func TestAFreshRepoSaysNothingAboutHooks(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")

	if strings.Contains(h.stderr, "hooks ") {
		t.Errorf("a fresh repo was reported as having hooks:\n%s", h.stderr)
	}
}

func TestOnlyExecutableNonSampleHooksCount(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	repoDir := filepath.Join(h.repos, "demo")
	hooks := filepath.Join(repoDir, ".git", "hooks")

	// A sample, and a non-executable leftover. git runs neither.
	if err := os.WriteFile(filepath.Join(hooks, "pre-rebase.sample"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "notes"), []byte("just a file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := activeHooks(repoDir); len(got) != 0 {
		t.Errorf("activeHooks = %v, want none - samples and unexecutable files do not run", got)
	}

	withHook(t, h, "demo", "commit-msg")
	if got := activeHooks(repoDir); len(got) != 1 || got[0] != "commit-msg" {
		t.Errorf("activeHooks = %v, want just commit-msg", got)
	}
}

// A repo that is not a git repo, or a .git that is a file (worktrees,
// submodules), must not blow up the plan.
func TestActiveHooksIsQuietWhenThereIsNoHooksDirectory(t *testing.T) {
	if got := activeHooks(t.TempDir()); got != nil {
		t.Errorf("activeHooks = %v, want nil for a directory with no .git", got)
	}
}

// The control has to hold continuously, not once. Before this, `new` set
// core.hooksPath at create and nothing touched it again - so one `git config
// --unset` inside the mount disabled the protection permanently and silently.
// 90-harden.sh is the pattern: deliberately unguarded, re-asserting the sudo
// removal on every boot.
func TestStartReAssertsTheHooksRedirect(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	repoDir := filepath.Join(h.repos, "demo")

	// The agent, or anything else with write access to the mount, undoes it.
	gitRun(t, repoDir, "config", "--unset", "core.hooksPath")
	if got := gitConfigOrEmpty(t, repoDir, "core.hooksPath"); got != "" {
		t.Fatalf("the setting was not actually removed, got %q", got)
	}

	h.mustRun("start", "demo")

	if got := gitConfig(t, repoDir, "core.hooksPath"); got != "/dev/null" {
		t.Errorf("core.hooksPath = %q after start, want it re-asserted", got)
	}
}

// The mapping start depends on: a VM name back to the host directory it
// mounts. Whichever record answers, it must be the repo - and never the image
// URL, which is the other line of that shape in a lima config.
func TestTheMountedRepoIsReadBack(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")

	got, ok := mountedRepo("demo")
	if !ok {
		t.Fatal("could not read the mounted repo back")
	}
	if want := filepath.Join(h.repos, "demo"); got != want {
		t.Errorf("mountedRepo = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, "https:") {
		t.Error("the image URL was mistaken for the mount")
	}
}

func TestMountedRepoIsAbsentForAVMThatWasNeverCreated(t *testing.T) {
	newHarness(t)
	if _, ok := mountedRepo("nothing-here"); ok {
		t.Error("mountedRepo answered for a VM with no generated config")
	}
}

// Since the sidecar, that config is the FALLBACK: the record `new` writes is
// its own one-line file, because what a rendered config looks like is the
// backend's business and only lima's has a mounts: block to read.
func TestTheMountedRepoComesFromTheSidecarWhateverTheConfigSays(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	want := filepath.Join(h.repos, "demo")

	if got := strings.TrimSpace(readFile(t, config.RepoFile("demo"))); got != want {
		t.Fatalf("the sidecar holds %q, want %q", got, want)
	}

	// A config with no mount in it at all - what a backend that takes its
	// mount as an argument renders.
	write(t, config.GeneratedConfig("demo"), "#cloud-config\nusers: [default]\n")
	if got, ok := mountedRepo("demo"); !ok || got != want {
		t.Errorf("mountedRepo = %q, %v; want %q from the sidecar", got, ok, want)
	}
}

func TestAVMFromBeforeTheSidecarIsStillMappedThroughItsLimaConfig(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	if err := os.Remove(config.RepoFile("demo")); err != nil {
		t.Fatal(err)
	}

	if got, ok := mountedRepo("demo"); !ok || got != filepath.Join(h.repos, "demo") {
		t.Errorf("mountedRepo = %q, %v; want the mount read out of the lima config", got, ok)
	}
}

func TestADamagedSidecarIsNoSidecar(t *testing.T) {
	// It names a directory `start` is about to run git in. Anything but one
	// absolute path falls through to the config, never to somewhere else.
	h := newHarness(t)
	h.mustRun("new", "demo")
	want := filepath.Join(h.repos, "demo")

	for _, body := range []string{"", "\n", "relative/path\n", "/one\n/two\n"} {
		write(t, config.RepoFile("demo"), body)
		if got, ok := mountedRepo("demo"); !ok || got != want {
			t.Errorf("sidecar %q: mountedRepo = %q, %v; want the fallback's %q", body, got, ok, want)
		}
	}
}

func TestRmRemovesTheSidecar(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("rm", "demo")
	if h.exists(config.RepoFile("demo")) {
		t.Error("the repo sidecar survived rm: a later VM of that name would inherit a stale mapping")
	}
}

func TestTheSidecarIsNotCountedAsASandbox(t *testing.T) {
	// The generated dir doubles as the registry of sandboxes. A file there
	// that the idle check mistook for a VM would keep the proxy up forever;
	// one the port allocator mistook for an allocation would burn a slot.
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("stop", "demo")
	h.assertOutputContains("stopped the proxy VM")

	ports, err := proxy.PortAllocations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 {
		t.Errorf("port allocations = %v, want demo's and nothing else", ports)
	}
}

// --- the escape hatch ----------------------------------------------------------

// PTRBOX_HOST_HOOKS is the one setting whose "on" means agent-writable code
// runs outside the sandbox. It exists because somebody with a real hook-based
// workflow on a repo they trust should be able to say so once, in a file,
// rather than be argued with on every create. A recorded decision beats a
// prompt answered under time pressure - which is why this is a config key and
// NOT a question `ptrbox new` asks.
func TestHostHooksLeavesTheRepoAlone(t *testing.T) {
	h := newHarness(t)
	h.writeVMConfig("demo", "PTRBOX_HOST_HOOKS=true\n")
	h.mustRun("new", "demo")

	repoDir := filepath.Join(h.repos, "demo")
	if got := gitConfigOrEmpty(t, repoDir, "core.hooksPath"); got != "" {
		t.Errorf("core.hooksPath = %q, want it left unset", got)
	}
	if !strings.Contains(h.stderr, "run on your Mac") {
		t.Errorf("the decision was not said out loud:\n%s", h.stderr)
	}
}

// Turning it on for a repo ptrbox has already clamped has to give the hooks
// back. Skipping the set would leave the old /dev/null in place and the
// setting would appear to do nothing.
func TestHostHooksUndoesAnExistingRedirect(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	repoDir := filepath.Join(h.repos, "demo")
	if got := gitConfigOrEmpty(t, repoDir, "core.hooksPath"); got != "/dev/null" {
		t.Fatalf("setup: core.hooksPath = %q", got)
	}

	// The owner decides this repo's hooks are theirs, and starts the VM.
	h.writeVMConfig("demo", "PTRBOX_HOST_HOOKS=true\n")
	h.mustRun("start", "demo")

	if got := gitConfigOrEmpty(t, repoDir, "core.hooksPath"); got != "" {
		t.Errorf("core.hooksPath = %q after enabling host hooks, want it removed", got)
	}
}

// It is per-VM, so allowing hooks for one repo must not allow them anywhere
// else - and start must read that VM's answer rather than the host default.
func TestHostHooksAppliesOnlyToTheVMThatAskedForIt(t *testing.T) {
	h := newHarness(t)
	h.writeVMConfig("demo", "PTRBOX_HOST_HOOKS=true\n")
	h.mustRun("new", "demo")
	h.mustRun("new", "other")

	if got := gitConfigOrEmpty(t, filepath.Join(h.repos, "other"), "core.hooksPath"); got != "/dev/null" {
		t.Errorf("another repo's hooks path = %q, want it still clamped", got)
	}
	// And starting the permitted one does not re-clamp it.
	h.mustRun("start", "demo")
	if got := gitConfigOrEmpty(t, filepath.Join(h.repos, "demo"), "core.hooksPath"); got != "" {
		t.Errorf("start re-clamped a repo that had opted out: %q", got)
	}
}
