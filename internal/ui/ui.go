// Package ui is ptrbox's output voice.
//
// Everything informational goes to stderr so command output - a log tail, the
// allowlist - stays pipeable. Errors are returned rather than printed here;
// only main decides that a run is over.
//
// Styling is decided once, here, from the Color field main sets. No call site
// anywhere else in ptrbox emits an escape sequence, which is what keeps the
// decision reviewable: there is one answer to "is this terminal capable", not
// one per message.
//
// With Color false - a pipe, a CI log, NO_COLOR, TERM=dumb, --no-color - the
// output is the plain "ptrbox: ..." lines this package has always written.
// That is the path the test suite runs against, so the styled path is
// deliberately a thin layer over it: colour and a glyph, never a different
// message. Where a glyph appears with colour on, the word it stands for
// appears with colour off ("warning:", "error:") - the same signal in the
// alphabet the reader has.
package ui

import (
	"fmt"
	"io"
	"strings"
)

// The escape sequences, kept in this file and nowhere else.
const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

// Printer writes ptrbox's progress notes.
type Printer struct {
	W io.Writer

	// Color is set by main from the terminal it found. The zero value - no
	// colour - is the safe one: a Printer assembled somewhere that has not
	// thought about it writes plain text.
	Color bool
}

// paint wraps s in an escape sequence, or returns it untouched when colour is
// off. Every styled byte ptrbox writes goes through here.
func (p Printer) paint(code, s string) string {
	if !p.Color || s == "" {
		return s
	}
	return code + s + reset
}

// mark is a leading glyph, or "" with colour off - see the package comment on
// why the glyph is not carried into plain output.
func (p Printer) mark(code, glyph string) string {
	if !p.Color {
		return ""
	}
	return p.paint(code, glyph) + " "
}

// line writes one prefixed line: "ptrbox: " and then whatever was built.
func (p Printer) line(body string) {
	fmt.Fprintf(p.W, "%s %s\n", p.paint(dim, "ptrbox:"), body)
}

// Say reports something that happened.
func (p Printer) Say(format string, a ...any) {
	p.line(fmt.Sprintf(format, a...))
}

// Done reports that something finished, and finished well. The closing line
// of a command that worked.
func (p Printer) Done(format string, a ...any) {
	p.line(p.mark(green, "✓") + fmt.Sprintf(format, a...))
}

// Warn reports something worth knowing that is not fatal.
func (p Printer) Warn(format string, a ...any) {
	p.line(p.mark(yellow, "!") + p.word("warning: ") + fmt.Sprintf(format, a...))
}

// Fail reports the thing that ended the run. main calls this and nothing
// else does: a command explains itself by returning an error.
func (p Printer) Fail(format string, a ...any) {
	p.line(p.mark(red, "✗") + p.word("error: ") + fmt.Sprintf(format, a...))
}

// word is the plain-text half of a glyph: dropped when the glyph is there to
// say it instead.
func (p Printer) word(s string) string {
	if p.Color {
		return ""
	}
	return s
}

// Detail is a line under whatever was just said - a command to copy, a path,
// a value. Unprefixed and indented, because the thing you might paste should
// not have "ptrbox: " in front of it, and dimmed when there is colour.
func (p Printer) Detail(format string, a ...any) {
	body := fmt.Sprintf(format, a...)
	if body == "" {
		fmt.Fprintln(p.W)
		return
	}
	fmt.Fprintln(p.W, p.paint(dim, "    "+body))
}

// Summary closes a command: a blank line, a headline, and the lines you might
// act on. The blank line is the point - it stops the summary reading as more
// progress, at the moment when progress is what the last several minutes have
// been.
func (p Printer) Summary(headline string, details ...string) {
	fmt.Fprintln(p.W)
	p.Done("%s", headline)
	for _, detail := range details {
		p.Detail("%s", detail)
	}
}

// Prompt asks a question, leaving the cursor on the same line. Bold, because
// it is the one line waiting for the reader rather than telling them
// something.
func (p Printer) Prompt(question string) {
	fmt.Fprintf(p.W, "%s %s %s ",
		p.paint(dim, "ptrbox:"), p.paint(bold, question), p.paint(dim, "[y/N]"))
}

// Raw writes text through unchanged - for output that came from somewhere
// else, like squid's complaint about a config it refused.
func (p Printer) Raw(s string) { fmt.Fprintln(p.W, s) }

// Dim writes a line from somewhere else verbatim, turned down. For output
// ptrbox is relaying rather than saying: it stays visible, it stops
// competing. With colour off it is exactly Raw, which is the point - nothing
// is hidden, only quietened.
func (p Printer) Dim(s string) { fmt.Fprintln(p.W, p.paint(dim, s)) }

// Check renders one assertion's verdict - a line of vm/verify.sh's output.
//
// With colour off it is reproduced byte for byte in the guest script's own
// layout, because that layout is already a readable check list and because
// those bytes are what the test suite and every past transcript contain.
// Colour turns the verdict into a glyph.
func (p Printer) Check(name string, ok bool, detail string) {
	if !p.Color {
		if ok {
			fmt.Fprintf(p.W, "  %-22s OK\n", name)
			return
		}
		fmt.Fprintf(p.W, "  %-22s FAIL - %s\n", name, detail)
		return
	}
	if ok {
		fmt.Fprintf(p.W, "  %s %s\n", p.paint(green, "✓"), name)
		return
	}
	fmt.Fprintf(p.W, "  %s %s %s\n", p.paint(red, "✗"), name, p.paint(dim, "- "+detail))
}

// --- numbered steps ----------------------------------------------------------

// Plan starts a numbered sequence of steps. The counter is what gives a
// multi-minute command a shape: "provisioning" on its own could be the first
// of two things or the first of ten.
func (p Printer) Plan(total int) *Steps { return &Steps{out: p, total: total} }

// Steps hands out the numbers. It is a pointer because it counts, and it
// counts in one place so that adding a step is one edit rather than a
// renumbering.
type Steps struct {
	out   Printer
	total int
	n     int
}

// Next announces the next step.
func (s *Steps) Next(format string, a ...any) {
	s.n++
	s.out.line(s.out.paint(cyan, fmt.Sprintf("[%d/%d]", s.n, s.total)) + " " +
		s.out.paint(bold, fmt.Sprintf(format, a...)))
}

// Taken is how many steps have been announced, so a test can hold the
// declared total to the count actually used.
func (s *Steps) Taken() int { return s.n }

// Total is the count the plan was declared with.
func (s *Steps) Total() int { return s.total }

// --- helpers -----------------------------------------------------------------

// Plain strips ptrbox's escape sequences from s, for tests that want to read
// styled output as text. Only SGR sequences are handled, because those are
// the only ones this package emits - anything else would be a bug rather than
// something to parse around.
func Plain(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		for i < len(s) && s[i] != 'm' {
			i++
		}
	}
	return b.String()
}
