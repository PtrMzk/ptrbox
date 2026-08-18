package cli

// Getting the conversation out of a disposable box, without giving the box a
// host path to write to.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
)

// fakeArchive stands in for the tar the guest would stream out.
var fakeArchive = []byte("\x1f\x8b\x08\x00fake gzipped tar of .claude/projects")

func (h *harness) giveTranscripts(vm string, body []byte) {
	if h.fake.Transcripts == nil {
		h.fake.Transcripts = map[string][]byte{}
	}
	h.fake.Transcripts[vm] = body
}

// archives lists what ended up in the transcript directory.
func (h *harness) archives() []string {
	h.t.Helper()
	entries, err := os.ReadDir(config.TranscriptDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestSaveWritesAnArchiveAndPrintsThePath(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.giveTranscripts("demo", fakeArchive)

	h.mustRun("save", "demo")

	names := h.archives()
	if len(names) != 1 {
		t.Fatalf("archives = %v, want exactly one", names)
	}
	// The host names the file, never the guest: nothing is written to a path
	// the sandbox chose.
	if names[0] != "demo-20260817-204500.tar.gz" {
		t.Errorf("archive is named %q", names[0])
	}
	if !strings.Contains(h.stdout, names[0]) {
		t.Errorf("the path was not printed: %q", h.stdout)
	}

	body, err := os.ReadFile(filepath.Join(config.TranscriptDir(), names[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, fakeArchive) {
		t.Error("the archive does not match what the VM streamed out")
	}
}

func TestTheArchiveIsStoredNeverExtracted(t *testing.T) {
	// A tar from a sandbox can carry ../.. paths and symlinks. Storing it
	// whole means there is nothing for those to act on.
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.giveTranscripts("demo", fakeArchive)
	h.mustRun("save", "demo")

	var found []string
	filepath.WalkDir(config.StateDir(), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if len(found) != 1 || !strings.HasSuffix(found[0], ".tar.gz") {
		t.Errorf("expected exactly one archive file, got %v", found)
	}
}

func TestArchivePermissionsKeepTranscriptsPrivate(t *testing.T) {
	// A transcript records everything the agent was shown - a superset of the
	// repo, and not something to leave group-readable.
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.giveTranscripts("demo", fakeArchive)
	h.mustRun("save", "demo")

	dir, err := os.Stat(config.TranscriptDir())
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("transcript dir is %o, want 700", dir.Mode().Perm())
	}
	file, err := os.Stat(filepath.Join(config.TranscriptDir(), h.archives()[0]))
	if err != nil {
		t.Fatal(err)
	}
	if file.Mode().Perm() != 0o600 {
		t.Errorf("archive is %o, want 600", file.Mode().Perm())
	}
}

func TestSaveWithNoTranscriptsWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("save", "demo")

	h.assertOutputContains("no Claude transcripts yet")
	if names := h.archives(); len(names) != 0 {
		// An empty file would be a worse answer than none at all.
		t.Errorf("an empty archive was written: %v", names)
	}
}

func TestSaveNeedsARunningVM(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.fake.SetStatus("demo", "Stopped")

	err := h.run("save", "demo")
	if err == nil || !strings.Contains(err.Error(), "ptrbox start demo") {
		t.Errorf("err = %v", err)
	}
}

func TestSaveRefusesTheProxyVMAndUnknownVMs(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")

	if err := h.run("save", config.ProxyVM); err == nil ||
		!strings.Contains(err.Error(), "no transcripts") {
		t.Errorf("proxy err = %v", err)
	}
	if err := h.run("save", "nosuchvm"); err == nil ||
		!strings.Contains(err.Error(), "no VM named") {
		t.Errorf("unknown err = %v", err)
	}
	if err := h.run("save"); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("no-arg err = %v", err)
	}
}

// --- rm ----------------------------------------------------------------------

func TestRmArchivesBeforeDestroyingAnything(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.giveTranscripts("demo", fakeArchive)
	h.fake.Reset()

	h.mustRun("rm", "demo")

	// Order is the whole point: after delete the disk is gone.
	h.assertOrder(`shell demo -- bash -c`, `delete -f demo`)
	if len(h.archives()) != 1 {
		t.Errorf("archives = %v, want one", h.archives())
	}
}

func TestRmNoArchiveSkipsThePull(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.giveTranscripts("demo", fakeArchive)
	h.fake.Reset()

	h.mustRun("rm", "--no-archive", "demo")
	h.assertCalled(`delete -f demo`)
	h.assertNotCalled(`bash -c`)
	if names := h.archives(); len(names) != 0 {
		t.Errorf("--no-archive still archived: %v", names)
	}
}

func TestRmOfAStoppedVMSaysWhatCannotBeSaved(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.fake.SetStatus("demo", "Stopped")

	h.mustRun("rm", "demo")
	h.assertOutputContains("cannot be archived")
	h.assertOutputContains("ptrbox start demo")
	h.assertCalled(`delete -f demo`)
}

func TestRmWithNothingToArchiveStillRemoves(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("rm", "demo")
	h.assertOutputContains("no Claude transcripts to archive")
	h.assertCalled(`delete -f demo`)
}

// --- the size cap ------------------------------------------------------------

func TestCapWriterRefusesMoreThanItsBudget(t *testing.T) {
	// An agent that filled its own disk with transcripts should not get to
	// fill the host's.
	var sink bytes.Buffer
	capped := &capWriter{W: &sink, Remaining: 10}

	if _, err := capped.Write([]byte("12345")); err != nil {
		t.Fatalf("a write inside the budget failed: %v", err)
	}
	if _, err := capped.Write([]byte("123456")); err == nil {
		t.Fatal("a write past the budget was accepted")
	}
	if capped.Written != 5 {
		t.Errorf("Written = %d, want 5", capped.Written)
	}
	if sink.String() != "12345" {
		t.Errorf("sink = %q", sink.String())
	}
}
