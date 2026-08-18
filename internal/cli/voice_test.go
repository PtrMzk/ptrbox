package cli

// What a command sounds like.
//
// The harness builds a Printer with Color false, which is the path CI, a pipe
// and every other test in this package read - so these assert the plain shape:
// numbered steps that reach their declared total, a closing summary, and not
// one escape byte anywhere.

import (
	"strings"
	"testing"
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
