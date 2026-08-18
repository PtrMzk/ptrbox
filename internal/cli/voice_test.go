package cli

// What a command sounds like.
//
// The harness builds a Printer with Color false, which is the path CI, a pipe
// and every other test in this package read - so these assert the plain shape:
// numbered steps that reach their declared total, a closing summary, and not
// one escape byte anywhere.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/ui"
)

func TestNewCountsItsStepsToTheDeclaredTotal(t *testing.T) {
	// A counter that stops at [3/5] is worse than no counter: it reads as a
	// command that gave up. This holds the plan to the steps actually taken.
	h := newHarness(t)
	h.mustRun("new", "demo")
	for _, want := range []string{"[1/5]", "[2/5]", "[3/5]", "[4/5]", "[5/5]"} {
		h.assertOutputContains(want)
	}
	if strings.Contains(h.output(), "[6/5]") {
		t.Errorf("more steps ran than were planned:\n%s", h.output())
	}
}

func TestInstallCountsItsStepsToTheDeclaredTotal(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	for _, want := range []string{"[1/5]", "[5/5]"} {
		h.assertOutputContains(want)
	}
	if strings.Contains(h.output(), "[6/5]") {
		t.Errorf("more steps ran than were planned:\n%s", h.output())
	}
}

func TestNewClosesWithSomethingYouCanActOn(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	for _, want := range []string{
		`VM "demo" is ready`,
		"ssh lima-demo",
		"cd /workspace && claude",
		"debian13",
		"mounted at /workspace",
	} {
		h.assertOutputContains(want)
	}
}

func TestInstallClosesWithSomethingYouCanActOn(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	for _, want := range []string{
		"host setup complete",
		"next: ptrbox new <repo>",
		"ptrbox-proxy, reached at 127.0.0.1:8888",
	} {
		h.assertOutputContains(want)
	}
}

func TestNothingStylesItsOwnOutput(t *testing.T) {
	// Colour is one decision, made in internal/ui from the Color field main
	// sets. A call site that reached for an escape itself would be styling
	// output that might be going into a file.
	sources := goSources(t, "../..", "ui.go")
	for _, escape := range []string{`\033[`, `\x1b[`, string(rune(0x1b))} {
		if strings.Contains(sources, escape) {
			t.Errorf("an escape sequence (%q) appears outside internal/ui", escape)
		}
	}
}

func TestAPlainRunHasNoEscapesInIt(t *testing.T) {
	// Whatever a command does, with colour off its output is text. Run
	// through several, including one that fails, because the summary and the
	// step counter are the newest paths and the ones a stray escape would
	// ride in on.
	h := newHarness(t)
	for _, args := range [][]string{
		{"install"},
		{"new", "demo"},
		{"allow", "example.test"},
		{"stop", "demo"},
		{"new"}, // usage error
	} {
		_ = h.run(args...)
		if strings.ContainsRune(h.output(), 0x1b) {
			t.Errorf("ptrbox %s put an escape in plain output:\n%q",
				strings.Join(args, " "), h.output())
		}
	}
}

// --- limactl's stream --------------------------------------------------------
//
// The bytes come from internal/narrate/testdata, replayed by limafake, so
// these exercise the same transcript the translator is tested against - only
// through the whole command this time.

func limaTranscript(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "narrate", "testdata", "limactl-start.log"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestNewTranslatesTheBootIntoPtrboxsVoice(t *testing.T) {
	h := newHarness(t)
	h.fake.StartOutput = limaTranscript(t)

	h.mustRun("new", "demo")
	for _, want := range []string{
		"downloading the debian13 image (first boot only)",
		"booting (1/5): ssh",
		"finishing (1/1): boot scripts must have finished",
	} {
		h.assertOutputContains(want)
	}
	// ...and lima's own phrasing is gone from the loud path.
	if strings.Contains(h.output(), "Waiting for the essential requirement") {
		t.Errorf("lima's wording survived the translation:\n%s", h.output())
	}
}

func TestVerboseShowsTheStreamAsLimaWroteIt(t *testing.T) {
	h := newHarness(t)
	h.verbose = true
	h.fake.StartOutput = limaTranscript(t)

	h.mustRun("new", "demo")
	if !strings.Contains(h.output(), "INFO[0032] [hostagent] Waiting for the essential requirement 1 of 5") {
		t.Errorf("--verbose did not show the raw stream:\n%s", h.output())
	}
}

func TestAFailedStartReprintsWhatLimactlSaid(t *testing.T) {
	// main replays the raw bytes of a failed invocation whatever the mode:
	// the translation is a convenience, and a failure is when you want what
	// was actually said.
	h := newHarness(t)
	h.fake.StartOutput = limaTranscript(t)
	h.fake.StartFails = true

	if err := h.run("new", "demo"); err == nil {
		t.Fatal("new succeeded with a limactl that would not start")
	}
	replayed := &bytes.Buffer{}
	h.narrator.Out = ui.Printer{W: replayed}
	h.narrator.Replay()
	if !strings.Contains(replayed.String(), "Waiting for the essential requirement 1 of 5") {
		t.Errorf("the raw stream was not kept for the failure:\n%s", replayed.String())
	}
}
