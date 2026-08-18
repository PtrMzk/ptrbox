// Package narrate turns limactl's log into ptrbox's voice.
//
// Creating a VM, starting it, the reboot and the verification run all stream
// limactl's output straight to the terminal, so ptrbox's own narration is a
// few lines lost in someone else's log. The information in those lines is
// genuinely wanted - during a first boot that downloads an image, it is the
// only real progress signal there is - but it is phrased for lima's authors:
//
//	INFO[0049] [hostagent] Waiting for the essential requirement 2 of 5: "user session is ready for ssh"
//
// becomes
//
//	ptrbox: booting (2/5): user session is ready for ssh [49s]
//
// The rule this package is built on: it narrows what is LOUD, never what is
// VISIBLE. A line that matches no pattern is still printed, dimmed and
// verbatim, so a lima release that rewords its messages degrades to "shown
// as lima wrote it" rather than to a missing step or, worse, a wrong one.
// --verbose shows the whole stream untouched, and a failed invocation
// reprints its raw bytes whatever the mode.
//
// vm/verify.sh output is ptrbox's own and already reads as assertions, so it
// is rendered as check items rather than translated.
package narrate

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/ui"
)

// rawLimit caps how much of an invocation is kept for a failure replay. Two
// hundred lines is several times a normal `limactl start`; the cap exists so
// that a command which loops printing errors cannot grow this without bound.
const rawLimit = 200

// Stream is an io.Writer for limactl's output. It implements lima.Narrator,
// so the client tells it where one invocation ends and the next begins.
type Stream struct {
	Out ui.Printer

	// Verbose passes the stream through untranslated.
	Verbose bool

	// Image names the guest image in the download line, e.g. "debian13". Set
	// once the configuration is loaded; empty is fine and reads as "the VM
	// image".
	Image string

	partial  strings.Builder // bytes since the last newline
	raw      []string        // this invocation's lines, for a failure replay
	overflow bool
	failed   bool
}

// Begin starts a new invocation: whatever the last one printed stops being
// relevant to a failure.
func (s *Stream) Begin([]string) {
	s.raw, s.overflow, s.failed = nil, false, false
}

// End closes the invocation off, flushing a line limactl left unterminated
// and remembering whether it failed.
func (s *Stream) End(err error) {
	if rest := s.partial.String(); rest != "" {
		s.partial.Reset()
		s.emit(rest)
	}
	s.failed = err != nil
}

// Write consumes limactl's output a line at a time. Partial lines are held
// until their newline arrives, so a translation is never applied to half a
// sentence.
func (s *Stream) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != '\n' {
			s.partial.WriteByte(b)
			continue
		}
		line := s.partial.String()
		s.partial.Reset()
		s.emit(strings.TrimSuffix(line, "\r"))
	}
	return len(p), nil
}

// Replay reprints the raw output of the last invocation, if it failed. The
// translation is a convenience; a failure is when you want the bytes.
func (s *Stream) Replay() {
	if !s.failed || len(s.raw) == 0 {
		return
	}
	s.Out.Say("what limactl printed:")
	if s.overflow {
		s.Out.Detail("(earlier lines dropped)")
	}
	for _, line := range s.raw {
		s.Out.Raw(line)
	}
}

func (s *Stream) emit(line string) {
	s.keep(line)
	if s.Verbose {
		s.Out.Raw(line)
		return
	}
	s.render(line)
}

func (s *Stream) keep(line string) {
	if len(s.raw) >= rawLimit {
		s.raw = s.raw[1:]
		s.overflow = true
	}
	s.raw = append(s.raw, line)
}

// --- the patterns ------------------------------------------------------------
//
// Every one of these is lima's wording, not ptrbox's, which is why they are
// pinned by a recorded transcript in testdata rather than by anything written
// from memory. Add a pattern by adding a line to that transcript first.

var (
	// logrus's default text format: LEVEL[seconds] message.
	limaLine = regexp.MustCompile(`^([A-Z]{4})\[(\d+)\]\s+(.*)$`)

	// "[hostagent] ", "[limactl] " and friends prefix the message.
	limaComponent = regexp.MustCompile(`^\[[a-z]+\]\s+`)

	requirementWait = regexp.MustCompile(
		`^Waiting for the (essential|optional|final) requirement (\d+) of (\d+):\s*"?([^"]*)"?`)
	requirementDone = regexp.MustCompile(
		`^The (essential|optional|final) requirement (\d+) of (\d+) is satisfied`)

	// Narrow on purpose. lima downloads other things - the nerdctl archive,
	// for one - and "downloading the debian13 image" said about the wrong
	// download is exactly the wrong step this design is meant not to produce.
	// Anything else that starts with "Download" falls through and is shown.
	downloading = regexp.MustCompile(`^Download(ing|ed) (the )?image\b`)
	usingCache  = regexp.MustCompile(`^Using cache|^Using the existing instance`)
	starting    = regexp.MustCompile(`^Starting \w+`)
	ready       = regexp.MustCompile(`^READY\b`)

	// vm/verify.sh's own output: two leading spaces, a padded name, then the
	// verdict. Ours already, so it is re-rendered rather than translated.
	// One space is enough to separate them - the script pads names to a fixed
	// width, and a name that fills it exactly leaves exactly one.
	checkOK   = regexp.MustCompile(`^  (\S.*?)\s+OK\s*$`)
	checkFail = regexp.MustCompile(`^  (\S.*?)\s+FAIL - (.*)$`)
)

// render decides what one line becomes. The order is deliberate: ptrbox's own
// output is recognised first, then lima's, then everything else is shown as
// it arrived.
func (s *Stream) render(line string) {
	if m := checkOK.FindStringSubmatch(line); m != nil {
		s.Out.Check(m[1], true, "")
		return
	}
	if m := checkFail.FindStringSubmatch(line); m != nil {
		s.Out.Check(m[1], false, m[2])
		return
	}

	fields := limaLine.FindStringSubmatch(line)
	if fields == nil {
		s.Out.Dim(line)
		return
	}
	level, elapsed, message := fields[1], fields[2], limaComponent.ReplaceAllString(fields[3], "")

	// Anything lima considers a problem is shown as lima wrote it and is not
	// dimmed: a translation of an error is a worse error message.
	if level != "INFO" && level != "DEBU" && level != "TRAC" {
		s.Out.Raw(line)
		return
	}

	switch {
	case requirementWait.MatchString(message):
		m := requirementWait.FindStringSubmatch(message)
		s.Out.Say("%s (%s/%s): %s [%s]", phase(m[1]), m[2], m[3], m[4], seconds(elapsed))

	case requirementDone.MatchString(message):
		s.Out.Detail("ok at %s", seconds(elapsed))

	case downloading.MatchString(message):
		if strings.HasPrefix(message, "Downloaded") {
			s.Out.Detail("image downloaded at %s", seconds(elapsed))
			return
		}
		s.Out.Say("downloading the %s image (first boot only)", s.image())

	case usingCache.MatchString(message):
		s.Out.Detail("%s", lowerFirst(message))

	case starting.MatchString(message):
		s.Out.Say("booting the virtual machine")

	case ready.MatchString(message):
		s.Out.Detail("lima reports the instance ready at %s", seconds(elapsed))

	default:
		s.Out.Dim(line)
	}
}

func (s *Stream) image() string {
	if s.Image == "" {
		return "VM"
	}
	return s.Image
}

// phase is ptrbox's word for lima's requirement classes. "essential" and
// "final" are stages of the same wait as far as anyone watching is concerned;
// what differs is which end of the boot they are.
func phase(class string) string {
	switch class {
	case "final":
		return "finishing"
	case "optional":
		return "checking"
	default:
		return "booting"
	}
}

// seconds renders lima's own elapsed counter, which is more honest than a
// clock ptrbox starts: it is measured from when limactl began.
func seconds(field string) string {
	n, err := strconv.Atoi(field)
	if err != nil {
		return field + "s"
	}
	if n < 60 {
		return fmt.Sprintf("%ds", n)
	}
	return fmt.Sprintf("%dm%02ds", n/60, n%60)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

var _ io.Writer = (*Stream)(nil)
