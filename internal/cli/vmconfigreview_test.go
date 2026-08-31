package cli

// install's review of ~/.config/ptrbox/vms/<name>.
//
// These files go stale exactly like the main settings file, and it costs more
// when they do: a per-VM file is read only by `ptrbox new`, so a key that
// stopped existing does not fail anything. It warns once, in the middle of a
// multi-minute create, and the sandbox comes up without whatever it asked for.
// Before this there was also no way to ask which of them were out of date
// short of re-creating every VM to find out.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
)

func writeVMFile(t *testing.T, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(config.VMDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.VMDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The failure this exists for: a sandbox asking for something it will not get.
func TestInstallNamesAPerVMFileThatSetsARetiredKey(t *testing.T) {
	h := newHarness(t)
	path := writeVMFile(t, "thesis", "PTRBOX_TOOLCHAIN=\"node uv\"\nPTRBOX_MEMORY=4GiB\n")
	h.mustRun("install")

	if !strings.Contains(h.stderr, path) {
		t.Errorf("install did not name the stale per-VM file:\n%s", h.stderr)
	}
	if !strings.Contains(h.stderr, "PTRBOX_TOOLCHAIN") {
		t.Errorf("install did not name the setting that no longer exists:\n%s", h.stderr)
	}
	// Reporting only - a file the user owns is not rewritten without --update.
	if got := readFile(t, path); !strings.Contains(got, "PTRBOX_TOOLCHAIN") {
		t.Error("install rewrote a per-VM file it was only asked to report on")
	}
}

// A current file says nothing, because re-running install has to stay a no-op.
func TestInstallIsQuietAboutAHealthyPerVMFile(t *testing.T) {
	h := newHarness(t)
	writeVMFile(t, "thesis", "PTRBOX_MEMORY=4GiB\nPTRBOX_GO=true\n")
	h.mustRun("install")

	for _, unwanted := range []string{"no longer a setting", "thesis"} {
		if strings.Contains(h.stderr, unwanted) {
			t.Errorf("install complained about a healthy per-VM file (%q):\n%s", unwanted, h.stderr)
		}
	}
}

// The documented style for this directory is "state only what differs". A
// sparse hand-written file must not be inflated into the full annotated
// example just because install ran.
func TestASparseHandWrittenPerVMFileIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	const sparse = "PTRBOX_MEMORY=4GiB\n"
	path := writeVMFile(t, "thesis", sparse)
	h.mustRun("install", "--update")

	if got := readFile(t, path); got != sparse {
		t.Errorf("--update rewrote a hand-written per-VM file:\n%s", got)
	}
}

// A file `ptrbox new` seeded from the example is a copy of shipped prose, so
// it goes stale the same way the main config does and gets the same treatment.
func TestUpdateBringsASeededPerVMFileUpToDateKeepingItsSettings(t *testing.T) {
	h := newHarness(t)
	// Shaped like one seeded by an older ptrbox: the real header, a couple of
	// keys, and no mention of anything added since.
	stale := fmt.Sprintf(vmConfigHeader, "thesis", config.Path(), "thesis", "thesis") +
		"# [vm] Language runtimes.\n#PTRBOX_NODE=false\nPTRBOX_MEMORY=4GiB\n"
	path := writeVMFile(t, "thesis", stale)
	h.mustRun("install", "--update")

	updated := readFile(t, path)
	if !strings.Contains(updated, "PTRBOX_MEMORY=4GiB") {
		t.Errorf("--update lost the setting in a per-VM file:\n%s", updated)
	}
	if !strings.Contains(updated, "#PTRBOX_PLAYWRIGHT=false") {
		t.Errorf("--update did not bring the current key list:\n%s", updated)
	}
	// Still addressed to this VM, not to whichever one was seeded first.
	if !strings.Contains(updated, `"thesis"`) {
		t.Errorf("the header no longer names the VM:\n%s", updated)
	}
	// And it still resolves as that VM's config.
	if _, err := config.Load(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	vm, err := cfg.Overlay("thesis")
	if err != nil {
		t.Fatalf("the updated per-VM file does not resolve: %v", err)
	}
	if vm.Memory != "4GiB" {
		t.Errorf("memory = %q, want the setting to still apply", vm.Memory)
	}
}

// README.txt lives in this directory and is not a config file. It has its own
// update path; the review must not read it as one.
func TestTheReadmeIsNotReviewedAsAPerVMConfig(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")

	if strings.Contains(h.stderr, "README.txt sets") {
		t.Errorf("the README was parsed as a per-VM config:\n%s", h.stderr)
	}
}
