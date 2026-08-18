package ui

// Two paths, tested separately.
//
// The plain one is what every other test in the repo reads, what CI logs, and
// what a pipe gets - so it is pinned line by line, and no method may put an
// escape byte in it. The styled one has no test suite reading it, so what is
// asserted is the property that keeps it honest: the same message, with
// escapes around it.

import (
	"bytes"
	"strings"
	"testing"
)

func plainPrinter() (Printer, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return Printer{W: buf}, buf
}

func colorPrinter() (Printer, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return Printer{W: buf, Color: true}, buf
}

func TestThePlainLinesAreExactly(t *testing.T) {
	p, buf := plainPrinter()
	p.Say("provisioning %s", "demo")
	p.Warn("no Keychain entry %q", "claude")
	p.Fail("verification FAILED")
	p.Done("host setup complete")
	p.Detail("brew install lima")
	p.Raw("squid: FATAL: Bungled config")
	p.Prompt("symlink ptrbox into ~/bin?")

	want := "ptrbox: provisioning demo\n" +
		"ptrbox: warning: no Keychain entry \"claude\"\n" +
		"ptrbox: error: verification FAILED\n" +
		"ptrbox: host setup complete\n" +
		"    brew install lima\n" +
		"squid: FATAL: Bungled config\n" +
		"ptrbox: symlink ptrbox into ~/bin? [y/N] "
	if buf.String() != want {
		t.Errorf("plain output is\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestNoMethodWritesAnEscapeWithColorOff(t *testing.T) {
	// The one property the rest of the suite depends on: with Color false
	// there is nothing in the stream a test would have to parse around.
	p, buf := plainPrinter()
	p.Say("say")
	p.Warn("warn")
	p.Fail("fail")
	p.Done("done")
	p.Detail("detail")
	p.Detail("")
	p.Raw("raw")
	p.Prompt("prompt?")
	p.Summary("headline", "detail", "", "another")
	p.Plan(3).Next("step")

	if strings.ContainsRune(buf.String(), 0x1b) {
		t.Errorf("an escape sequence reached plain output:\n%q", buf.String())
	}
}

func TestStepsAreNumberedAgainstTheirTotal(t *testing.T) {
	p, buf := plainPrinter()
	steps := p.Plan(3)
	steps.Next("first")
	steps.Next("second %s", "thing")

	want := "ptrbox: [1/3] first\nptrbox: [2/3] second thing\n"
	if buf.String() != want {
		t.Errorf("steps read\n%q\nwant\n%q", buf.String(), want)
	}
	if steps.Taken() != 2 || steps.Total() != 3 {
		t.Errorf("Taken/Total = %d/%d, want 2/3", steps.Taken(), steps.Total())
	}
}

func TestASummaryIsSetOffFromTheProgressAboveIt(t *testing.T) {
	p, buf := plainPrinter()
	p.Say("verifying")
	p.Summary("VM \"demo\" is ready", "ssh lima-demo", "", "distro   debian13")

	want := "ptrbox: verifying\n" +
		"\n" +
		"ptrbox: VM \"demo\" is ready\n" +
		"    ssh lima-demo\n" +
		"\n" +
		"    distro   debian13\n"
	if buf.String() != want {
		t.Errorf("summary reads\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestAPercentInASummaryIsNotAVerb(t *testing.T) {
	// Summary takes finished strings, not formats: a repo path or a domain
	// with a % in it must not turn into %!d(MISSING).
	p, buf := plainPrinter()
	p.Summary("done 100% of it", "cd /workspace/100%")
	if strings.Contains(buf.String(), "%!") {
		t.Errorf("the summary reformatted its own arguments:\n%s", buf.String())
	}
}

// --- the styled path ---------------------------------------------------------

func TestStyledOutputIsThePlainMessageWithEscapesAroundIt(t *testing.T) {
	p, buf := colorPrinter()
	p.Say("provisioning demo")
	p.Plan(5).Next("verifying sandbox properties")
	p.Detail("ssh lima-demo")

	styled := buf.String()
	if !strings.ContainsRune(styled, 0x1b) {
		t.Fatalf("nothing was styled:\n%q", styled)
	}
	stripped := Plain(styled)
	want := "ptrbox: provisioning demo\nptrbox: [1/5] verifying sandbox properties\n    ssh lima-demo\n"
	if stripped != want {
		t.Errorf("stripped styled output is\n%q\nwant\n%q", stripped, want)
	}
}

func TestAGlyphStandsInForTheWordItReplaces(t *testing.T) {
	// Colour and a glyph say what "warning:" and "error:" say in plain text.
	// Printing both would be the same signal twice.
	for _, tc := range []struct {
		name  string
		call  func(Printer)
		glyph string
		word  string
	}{
		{"warn", func(p Printer) { p.Warn("something") }, "!", "warning:"},
		{"fail", func(p Printer) { p.Fail("something") }, "✗", "error:"},
		{"done", func(p Printer) { p.Done("something") }, "✓", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, buf := colorPrinter()
			tc.call(p)
			styled := Plain(buf.String())
			if !strings.Contains(styled, tc.glyph) {
				t.Errorf("no %q glyph in styled output: %q", tc.glyph, styled)
			}
			if tc.word != "" && strings.Contains(styled, tc.word) {
				t.Errorf("styled output says %q as well as showing the glyph: %q", tc.word, styled)
			}
		})
	}
}

func TestPlainStripsOnlyTheEscapes(t *testing.T) {
	if got := Plain("\x1b[2mptrbox:\x1b[0m hello"); got != "ptrbox: hello" {
		t.Errorf("Plain = %q", got)
	}
	if got := Plain("nothing to strip"); got != "nothing to strip" {
		t.Errorf("Plain = %q", got)
	}
}
