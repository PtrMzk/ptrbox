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
	"path/filepath"
	"strings"
	"testing"
)

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
