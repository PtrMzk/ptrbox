package proxy_test

// A VM's allowlist follows its config across a re-create: the marked groups
// are restored or emptied to match, and every other line is left alone.
//
// The case that prompted this: a sandbox created with node, deleted, and
// re-created with node and uv came back with no PyPI, because the list
// outlived the VM and nothing re-read it. Item 42's "nothing filters a list
// that already exists" was right about the person's lines and wrong about the
// template's.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
)

// reconcileWorld installs the seed template as the host template, writes a
// per-VM config, and returns the harness. body, when set, is written as the
// VM's existing list.
func reconcileWorld(t *testing.T, vmConfig, body string) *harness {
	t.Helper()
	h := newHarness(t)
	for path, content := range map[string]string{
		config.AllowlistPath():                seedTemplate,
		filepath.Join(config.VMDir(), "demo"): vmConfig,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if body != "" {
		if err := os.MkdirAll(filepath.Dir(config.VMAllowlistPath("demo")), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.VMAllowlistPath("demo"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func reconciled(t *testing.T, h *harness) (string, []string) {
	t.Helper()
	changes, err := h.ReconcileVMAllowlist("demo")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(config.VMAllowlistPath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, c := range changes {
		texts = append(texts, c.Text)
	}
	return string(body), texts
}

// The case itself: seeded with node, re-created with node and uv.
func TestARecreateWithANewFeatureRestoresItsGroup(t *testing.T) {
	seeded := seedFor(t, seedTemplate, "PTRBOX_NODE=true\n")
	if strings.Contains(seeded, "pypi.org") {
		t.Fatal("the fixture is wrong: uv's group reached a node-only seed")
	}
	h := reconcileWorld(t, "PTRBOX_NODE=true\nPTRBOX_UV=true\n", seeded)

	body, changes := reconciled(t, h)
	if !strings.Contains(body, "# @requires uv\npypi.org\n# @end\n") {
		t.Errorf("the uv group was not restored in place:\n%s", body)
	}
	if strings.Contains(body, "(omitted: this VM has no uv)") {
		t.Errorf("the omitted note outlived the restore:\n%s", body)
	}
	if strings.Join(changes, "\n") != "allowlist: restored the uv group (pypi.org)" {
		t.Errorf("changes = %q", changes)
	}
	// The node group was already there and is untouched; the either-runtime
	// group was kept by node and stays.
	if strings.Count(body, "registry.npmjs.org") != 1 || strings.Count(body, "cdn.playwright.dev") != 1 {
		t.Errorf("groups that were already right were rewritten:\n%s", body)
	}
}

// The other direction: a feature turned off takes the template's entries
// with it, and only those - a line the person added inside the group stays.
func TestARecreateWithoutAFeatureEmptiesItsGroupButKeepsYourLines(t *testing.T) {
	seeded := seedFor(t, seedTemplate, "PTRBOX_NODE=true\nPTRBOX_UV=true\n")
	edited := strings.Replace(seeded, "pypi.org\n", "pypi.org\nmy-private-index.example.com\n", 1)
	h := reconcileWorld(t, "PTRBOX_NODE=true\n", edited)

	body, changes := reconciled(t, h)
	if strings.Contains(body, "pypi.org\n") {
		t.Errorf("pypi.org survived for a VM with no uv:\n%s", body)
	}
	if !strings.Contains(body, "# @requires uv\nmy-private-index.example.com\n# @end\n") {
		t.Errorf("the person's own line in the group was lost:\n%s", body)
	}
	if !strings.Contains(strings.Join(changes, "\n"), "removed pypi.org from the uv group - this VM has no uv") {
		t.Errorf("changes = %q", changes)
	}
	// Emptied entirely, the group gets the seed's note back, so a reader can
	// tell "left out" from "never existed".
	h2 := reconcileWorld(t, "PTRBOX_NODE=true\n", seeded)
	body2, _ := reconciled(t, h2)
	if !strings.Contains(body2, "# @requires uv\n# (omitted: this VM has no uv)\n# @end\n") {
		t.Errorf("the emptied group carries no note:\n%s", body2)
	}
}

// Reconciling a list that already matches changes nothing and says nothing -
// which is every create that is not a re-create with new features.
func TestAMatchingListIsLeftAlone(t *testing.T) {
	seeded := seedFor(t, seedTemplate, "PTRBOX_NODE=true\n")
	withYours := seeded + "yours.example.com\n"
	h := reconcileWorld(t, "PTRBOX_NODE=true\n", withYours)

	body, changes := reconciled(t, h)
	if body != withYours {
		t.Errorf("a matching list was rewritten:\n--- before\n%s--- after\n%s", withYours, body)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %q, want none", changes)
	}
}

// An enabled group is restored only when it is EMPTY. Pruning one entry from
// a group whose feature is still on is a decision, and it holds.
func TestAPrunedEntryInAnEnabledGroupIsNotPutBack(t *testing.T) {
	seeded := seedFor(t, seedTemplate, "PTRBOX_NODE=true\nPTRBOX_UV=true\n")
	template := strings.Replace(seedTemplate, "# @requires uv\npypi.org\n", "# @requires uv\npypi.org\nfiles.pythonhosted.org\n", 1)
	pruned := strings.Replace(seeded, "pypi.org\n", "pypi.org\n", 1) // seeded from the two-entry template below
	h := reconcileWorld(t, "PTRBOX_NODE=true\nPTRBOX_UV=true\n", pruned)
	if err := os.WriteFile(config.AllowlistPath(), []byte(template), 0o644); err != nil {
		t.Fatal(err)
	}

	body, changes := reconciled(t, h)
	if strings.Contains(body, "files.pythonhosted.org") {
		t.Errorf("an entry absent from an enabled group was added back:\n%s", body)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %q, want none", changes)
	}
}

// A list seeded before the markers existed has no groups to find. Its
// wanted features' entries are appended as a marked group, so the next
// reconcile has something to work with; entries it already holds unmarked
// are not duplicated.
func TestAnUnmarkedListGainsAMarkedGroupForANewFeature(t *testing.T) {
	old := "# an old list\nalways.example.com\nregistry.npmjs.org\n"
	h := reconcileWorld(t, "PTRBOX_NODE=true\nPTRBOX_UV=true\n", old)

	body, changes := reconciled(t, h)
	if !strings.Contains(body, "# @requires uv\n# (added: this VM now has uv)\npypi.org\n# @end\n") {
		t.Errorf("the uv group was not appended:\n%s", body)
	}
	if strings.Count(body, "registry.npmjs.org") != 1 {
		t.Errorf("an entry the list already held was duplicated:\n%s", body)
	}
	if !strings.Contains(strings.Join(changes, "\n"), "added the uv group (pypi.org)") {
		t.Errorf("changes = %q", changes)
	}
}

// And an unmarked entry for a feature the VM does not have is the person's
// line as far as anyone can tell, so it stays. (No features on, so nothing
// is wanted and nothing is appended either.)
func TestAnUnmarkedEntryIsNeverRemoved(t *testing.T) {
	old := "always.example.com\npypi.org\n"
	h := reconcileWorld(t, "# nothing on\n", old)
	body, changes := reconciled(t, h)
	if body != old || len(changes) != 0 {
		t.Errorf("an unmarked list was rewritten (%q):\n%s", changes, body)
	}
}

func TestAVMWithNoListIsNothingToReconcile(t *testing.T) {
	h := reconcileWorld(t, "PTRBOX_UV=true\n", "")
	changes, err := h.ReconcileVMAllowlist("demo")
	if err != nil || len(changes) != 0 {
		t.Errorf("changes = %v, err = %v", changes, err)
	}
	if _, err := os.Stat(config.VMAllowlistPath("demo")); err == nil {
		t.Error("reconciling created a list; that is the seed's job")
	}
}

// A group with no @end cannot be bounded, so it is reported and left alone
// rather than guessed at.
func TestABrokenGroupIsReportedNotRewritten(t *testing.T) {
	broken := "# @requires uv\n# (omitted: this VM has no uv)\nyours.example.com\n"
	h := reconcileWorld(t, "PTRBOX_UV=true\n", broken)
	changes, err := h.ReconcileVMAllowlist("demo")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(config.VMAllowlistPath("demo"))
	// The broken group is exactly as it was; the other groups are still
	// reconciled around it (here the either-runtime group is appended).
	if !strings.HasPrefix(string(body), broken) {
		t.Errorf("the broken group was rewritten:\n%s", body)
	}
	warned := false
	for _, c := range changes {
		if c.Warn && strings.Contains(c.Text, "no @end") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("changes = %+v, want a warning about the missing @end", changes)
	}
}
