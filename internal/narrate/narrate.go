// Package narrate turns limactl's log into ptrbox's voice.
//
// Creating a VM, starting it, the reboot and the verification run all stream
// limactl's output straight to the terminal, so ptrbox's own narration is a
// few lines lost in someone else's log. The information in those lines is
// genuinely wanted - during a first boot that downloads an image, it is the
// only real progress signal there is - but it is phrased for lima's authors:
//
//	time="2026-08-19T12:51:10-04:00" level=info msg="[hostagent] Waiting for the final requirement 1 of 1: `boot scripts must have finished`"
//
// becomes
//
//	ptrbox: finishing (1/1): boot scripts must have finished [1m50s]
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
//
// # The format, and why it is pinned
//
// The first version of this package was written on Linux against a faked
// limactl, from a recollection of logrus's INFO[0049] default text format.
// Lima 2.2.0 does not emit that: every line is logrus in key=value form, msg
// is a Go-quoted string, quoting inside a message is backticks, and there is
// no elapsed counter at all - so not one line matched, every line fell
// through to "shown verbatim", and the suite stayed green because the fixture
// had been invented in the same sitting as the patterns. Hence the rule that
// outlived it: a pattern is added by adding a line to
// testdata/limactl-start.log first, and that file is a capture. Shapes lima
// might emit but did not emit there - "Using the existing instance", say -
// are deliberately absent below; guessing at them is what produced a
// translator that never ran.
//
// Elapsed times are differences between lima's own timestamps rather than a
// clock this package starts. That keeps them honest across a slow start, and
// it keeps the tests deterministic: the numbers come out of the fixture.
package narrate

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

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

	// began is the timestamp of the first line of this invocation - the
	// origin every "[1m50s]" is measured from, since lima 2.2.0 prints wall
	// clock rather than an elapsed counter.
	began    time.Time
	hasBegan bool

	// waiting is when the requirement now in progress started, so that
	// "satisfied" can report how long it actually took. This is the number
	// that matters: the three essential requirements land in about a second,
	// while the toolchain probe and the boot scripts are minutes.
	waiting    time.Time
	hasWaiting bool
}

// Begin starts a new invocation: whatever the last one printed stops being
// relevant to a failure, and its clock stops being the one to measure from.
func (s *Stream) Begin([]string) {
	s.raw, s.overflow, s.failed = nil, false, false
	s.began, s.hasBegan = time.Time{}, false
	s.waiting, s.hasWaiting = time.Time{}, false
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
// Every one of these is lima's wording, taken from the recorded transcript in
// testdata. Add a pattern by adding a line to that transcript first.

var (
	// "[hostagent] ", "[limactl] " and friends prefix the message inside msg.
	limaComponent = regexp.MustCompile(`^\[[A-Za-z]+\]\s+`)

	// The requirement name is backtick-quoted in lima 2.2.0 and was
	// double-quoted before it, so the name is taken as "the rest of the line"
	// and unwrapped afterwards rather than matched inside a quote character
	// that keeps changing.
	requirementWait = regexp.MustCompile(
		`^Waiting for the (essential|optional|final) requirement (\d+) of (\d+):\s*(.*)$`)
	requirementDone = regexp.MustCompile(
		`^The (essential|optional|final) requirement (\d+) of (\d+) is satisfied`)

	// This one carries no "N of M" - it is not one of the numbered
	// requirements, which is why it needs its own pattern rather than a
	// looser version of the one above.
	guestAgentWait = regexp.MustCompile(`^Waiting for the guest agent\b`)

	// The download is announced from the bare progress line - which is not
	// logrus at all - because that is the only one of lima's download
	// messages that means a download is happening. "Attempting to download
	// the image" is logged before the cache is consulted, so on the common
	// path (an image already in ~/Library/Caches) it is followed by "Using
	// cache" and nothing is fetched. Announcing on the attempt claimed a
	// several-minute download that never ran, seen in the first smoke run
	// against a warm cache; that is a wrong step rather than a missing one,
	// which is the failure this whole design is arranged against. The
	// attempt line still shows, dimmed, and it is what carries the URL.
	//
	// Narrow for the same reason: lima downloads other things, and
	// "downloading the debian13 image" said about the nerdctl archive would
	// be the same kind of wrong.
	imageProgress   = regexp.MustCompile(`^Downloading the image \(`)
	downloadedImage = regexp.MustCompile(`^Downloaded the image\b`)

	startingInstance = regexp.MustCompile(`^Starting the instance\b`)
	ready            = regexp.MustCompile(`^READY\b`)

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

	e, ok := parseEntry(line)
	if !ok {
		// Not a log entry. The image download's progress line arrives this
		// way; anything else is shown as it came.
		if imageProgress.MatchString(line) {
			s.Out.Say("downloading the %s image (first boot only)", s.image())
			return
		}
		s.Out.Dim(line)
		return
	}
	if e.hasWhen && !s.hasBegan {
		s.began, s.hasBegan = e.when, true
	}

	switch e.level {
	case "info", "debug", "trace":
		s.translate(line, e)
	case "warning", "warn":
		// A healthy boot logs four of these: the host already has something
		// on port 5355, so lima cannot forward it. Shown, because a warning
		// is information, and quiet, because a warning ptrbox cannot act on
		// must not read like the run went wrong.
		s.Out.Dim(line)
	default:
		// error, fatal, panic - and any level a future lima invents. A
		// translation of an error is a worse error message, and an unknown
		// level errs towards loud.
		s.Out.Raw(line)
	}
}

// translate renders one info-level entry. line is passed alongside the parsed
// entry because the fallback is the line exactly as lima wrote it, structured
// fields and all - reprinting a reconstruction would quietly drop whatever
// lima had put in a field this package does not know about.
func (s *Stream) translate(line string, e entry) {
	message := limaComponent.ReplaceAllString(e.msg, "")

	switch {
	case requirementWait.MatchString(message):
		m := requirementWait.FindStringSubmatch(message)
		s.waiting, s.hasWaiting = e.when, e.hasWhen
		s.Out.Say("%s (%s/%s): %s%s", phase(m[1]), m[2], m[3], unquote(m[4]), s.elapsed(e))

	case requirementDone.MatchString(message):
		s.Out.Detail("ok%s", s.took(e))

	case guestAgentWait.MatchString(message):
		s.Out.Detail("waiting for the guest agent%s", s.elapsed(e))

	case downloadedImage.MatchString(message):
		s.Out.Detail("image downloaded%s", s.elapsed(e))

	case startingInstance.MatchString(message):
		s.Out.Say("booting the virtual machine")

	case ready.MatchString(message):
		s.Out.Detail("lima reports the instance ready%s", s.elapsed(e))

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

// elapsed is how far into the invocation a line arrived, as " [1m50s]", or
// "" when there is nothing to measure from.
func (s *Stream) elapsed(e entry) string {
	if !s.hasBegan || !e.hasWhen {
		return ""
	}
	return " [" + duration(e.when.Sub(s.began)) + "]"
}

// took is how long the requirement that just finished took, as " after
// 1m16s". Empty for anything under a second, which is where the three
// essential requirements live: "ok" says everything there is to say about a
// wait nobody noticed.
func (s *Stream) took(e entry) string {
	if !s.hasWaiting || !e.hasWhen {
		return ""
	}
	d := e.when.Sub(s.waiting)
	if d < time.Second {
		return ""
	}
	return " after " + duration(d)
}

// phase is ptrbox's word for lima's requirement classes. The names matter
// more than they look: the essential three are the ssh handshake and land in
// about a second, while lima's "optional" is where ptrbox's own toolchain
// probe waits out the package install, and "final" is cloud-init's boot
// scripts. Those last two are the minutes, so they are named for what is
// happening rather than for how lima classifies them.
func phase(class string) string {
	switch class {
	case "essential":
		return "connecting"
	case "optional":
		return "provisioning"
	case "final":
		return "finishing"
	default:
		return "waiting"
	}
}

// duration renders a wait the way a person waiting would say it.
func duration(d time.Duration) string {
	total := int(d.Seconds())
	if total < 0 {
		total = 0
	}
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	return fmt.Sprintf("%dm%02ds", total/60, total%60)
}

// unquote strips one layer of lima's quoting from a requirement name.
// 2.2.0 uses backticks; earlier releases used double quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	for _, quote := range []string{"`", `"`} {
		if len(s) >= 2 && strings.HasPrefix(s, quote) && strings.HasSuffix(s, quote) {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// --- lima's log format -------------------------------------------------------
//
// logrus's key=value text format, which is what lima 2.2.0 writes when its
// output is not a terminal - and ptrbox always pipes it, so this is the only
// shape that reaches here:
//
//	time="2026-08-19T12:49:20-04:00" level=info msg="Attempting to download the image" arch=aarch64 digest= location="https://..."
//
// Values are either bare or Go-quoted, and messages really do carry escaped
// quotes, so the quoted form is unquoted with strconv rather than read to the
// next double quote.

// entry is one parsed log line. Only the three fields anything renders from
// are kept: the rest of the line is still parsed - all-or-nothing is the
// point - but a line ptrbox has no pattern for is shown as lima wrote it,
// fields and all, so there is nothing for a translation to carry.
type entry struct {
	level   string
	msg     string
	when    time.Time
	hasWhen bool
}

// parseEntry reports whether the line is a log entry at all. Parsing is
// all-or-nothing: half a line understood would be a translation of something
// other than what lima said, and the fallback - shown verbatim - is a good
// outcome.
func parseEntry(line string) (entry, bool) {
	fields := parseFields(line)
	if fields == nil {
		return entry{}, false
	}
	level, hasLevel := fields["level"]
	msg, hasMsg := fields["msg"]
	if !hasLevel || !hasMsg {
		return entry{}, false
	}
	e := entry{level: level, msg: msg}
	if when, err := time.Parse(time.RFC3339, fields["time"]); err == nil {
		e.when, e.hasWhen = when, true
	}
	return e, true
}

// parseFields splits a logrus line into its key=value pairs, or returns nil
// if any part of it is not one.
func parseFields(line string) map[string]string {
	fields := map[string]string{}
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i >= len(line) || line[i] != '=' || i == start {
			return nil
		}
		key := line[start:i]
		value, next, ok := parseValue(line, i+1)
		if !ok {
			return nil
		}
		fields[key] = value
		i = next
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// parseValue reads one value starting at i, returning it and the index just
// past it. A quoted value is a Go string literal - lima's messages contain
// escaped quotes, so scanning to the next double quote would cut them short.
func parseValue(line string, i int) (string, int, bool) {
	if i < len(line) && line[i] == '"' {
		end := i + 1
		for end < len(line) && line[end] != '"' {
			if line[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(line) {
			return "", 0, false
		}
		value, err := strconv.Unquote(line[i : end+1])
		if err != nil {
			return "", 0, false
		}
		return value, end + 1, true
	}
	end := i
	for end < len(line) && line[end] != ' ' {
		end++
	}
	return line[i:end], end, true
}

var _ io.Writer = (*Stream)(nil)
