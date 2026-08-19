package narrate

// The translator, against a `limactl start` transcript.
//
// The transcript is a real capture - lima 2.2.0, a cold image cache, an
// instance that did not exist yet - because patterns written from memory
// tested against lines written from the same memory prove nothing, which is
// exactly how the first version of this package shipped as a no-op. See
// testdata/PROVENANCE.
//
// So the expectations here are quoted from that file rather than composed:
// the elapsed times are differences between its timestamps, the counts are
// its counts, and a test that wants a shape lima emits asks the fixture for
// the line rather than writing one out.
//
// The properties asserted are the ones the design rests on. Known shapes
// become ptrbox's vocabulary; everything else survives verbatim; nothing is
// ever dropped.

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/ui"
)

func transcript(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("testdata/limactl-start.log")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// transcriptLine returns the one captured line containing needle. Tests use
// it instead of quoting lima's wording inline, so a re-capture that reworded
// something fails here rather than testing bytes lima no longer writes.
func transcriptLine(t *testing.T, needle string) string {
	t.Helper()
	for _, line := range lines(transcript(t)) {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no line in the transcript contains %q", needle)
	return ""
}

func lines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func newStream(t *testing.T) (*Stream, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	return &Stream{Out: ui.Printer{W: buf}, Image: "debian13"}, buf
}

// feed writes s through the stream and closes the invocation off.
func feed(s *Stream, text string, err error) {
	s.Begin([]string{"start", "--name", "demo"})
	s.Write([]byte(text))
	s.End(err)
}

func TestTheBootIsTranslatedIntoPtrboxsVocabulary(t *testing.T) {
	// Every elapsed time below is a difference between two timestamps in the
	// fixture, measured from its first line (12:49:20).
	s, buf := newStream(t)
	feed(s, transcript(t), nil)
	out := buf.String()

	for _, want := range []string{
		"ptrbox: booting the virtual machine",
		"ptrbox: downloading the debian13 image (first boot only)",
		"ptrbox: connecting (1/3): ssh [33s]",
		"ptrbox: connecting (2/3): user session is ready for ssh [34s]",
		"ptrbox: connecting (3/3): Explicitly start ssh ControlMaster [34s]",
		"ptrbox: provisioning (1/1): toolchain ready [34s]",
		"ptrbox: finishing (1/1): boot scripts must have finished [1m50s]",
		"lima reports the instance ready [2m54s]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in the translation:\n%s", want, out)
		}
	}
	// lima's own phrasing for the things ptrbox now says itself.
	for _, gone := range []string{"Waiting for the essential requirement", "level=info msg=\"READY"} {
		if strings.Contains(out, gone) {
			t.Errorf("lima's wording for %q survived the translation:\n%s", gone, out)
		}
	}
}

func TestTheLongWaitIsTheOneThatIsTimed(t *testing.T) {
	// The point of the requirement narration. In the capture the three
	// essential requirements are satisfied within a second of being waited
	// on, while the toolchain probe takes 1m16s and the boot scripts 1m04s -
	// so a duration is reported where there was a wait, and "ok" alone where
	// there was not.
	s, buf := newStream(t)
	feed(s, transcript(t), nil)
	out := buf.String()

	for _, want := range []string{"ok after 1m16s", "ok after 1m04s"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in the translation:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ok after 0s") {
		t.Errorf("a wait nobody noticed was reported as a duration:\n%s", out)
	}
	if got := strings.Count(out, "    ok\n"); got != 2 {
		t.Errorf("%d instant requirements reported as plain ok, want 2:\n%s", got, out)
	}
}

func TestNothingIsDropped(t *testing.T) {
	// The rule the whole design rests on: the filter narrows what is loud,
	// never what is visible. One transcript line in, at least one line out.
	s, buf := newStream(t)
	text := transcript(t)
	feed(s, text, nil)

	in := len(lines(text))
	got := len(lines(buf.String()))
	if got < in {
		t.Errorf("%d lines in, %d out - the filter is hiding things:\n%s", in, got, buf.String())
	}
}

func TestTheChatterStaysAndStaysVerbatim(t *testing.T) {
	// Most of a boot is port-forward and time-sync lines. They are not
	// collapsed, not counted and not summarised - they are turned down, which
	// with colour off means they are exactly what lima wrote.
	s, buf := newStream(t)
	feed(s, transcript(t), nil)
	out := buf.String()

	for _, needle := range []string{"Not forwarding UDP", "Time sync: guest clock adjusted"} {
		want := strings.Count(transcript(t), needle)
		if want == 0 {
			t.Fatalf("the transcript no longer contains %q", needle)
		}
		if got := strings.Count(out, needle); got != want {
			t.Errorf("%d %q lines in, %d out", want, needle, got)
		}
	}
	line := transcriptLine(t, "Not forwarding UDP 192.168.5.15:68")
	if !strings.Contains(out, line+"\n") {
		t.Errorf("a relayed line was not reproduced verbatim:\n%s", out)
	}
}

func TestABenignWarningIsShownWithoutSoundingLikeAFailure(t *testing.T) {
	// A healthy boot logs four of these: something on the Mac already holds
	// port 5355. They must stay visible - and must not be dressed up as
	// ptrbox reporting a problem.
	line := transcriptLine(t, "bind: address already in use")
	s, buf := newStream(t)
	feed(s, line+"\n", nil)

	if got := buf.String(); got != line+"\n" {
		t.Errorf("the warning reads\n%q\nwant it verbatim:\n%q", got, line+"\n")
	}
	if strings.Contains(buf.String(), "ptrbox:") {
		t.Errorf("lima's warning was reported as ptrbox's:\n%s", buf.String())
	}
}

func TestARewordedLimaLineIsShownRatherThanGuessedAt(t *testing.T) {
	// A lima release that renames a requirement class must degrade to "shown
	// as lima wrote it", never to a step ptrbox invented. The line is made up
	// on purpose: it is the shape lima does NOT emit.
	s, buf := newStream(t)
	line := `time="2026-08-19T12:49:53-04:00" level=info msg="[hostagent] Waiting for the crucial prerequisite 2 of 7: ` +
		"`something new`" + `"`
	feed(s, line+"\n", nil)

	out := buf.String()
	if !strings.Contains(out, "Waiting for the crucial prerequisite 2 of 7") {
		t.Errorf("the unrecognised line was not shown:\n%s", out)
	}
	if strings.Contains(out, "(2/7)") {
		t.Errorf("an unrecognised line was translated anyway:\n%s", out)
	}
}

func TestALineThatIsNotALogEntryIsLeftAlone(t *testing.T) {
	s, buf := newStream(t)
	feed(s, "something from a tool that is not lima\n", nil)
	if got := buf.String(); got != "something from a tool that is not lima\n" {
		t.Errorf("a non-log line was rewritten: %q", got)
	}
}

func TestAnErrorFromLimaIsNotDimmedOrReworded(t *testing.T) {
	s, buf := newStream(t)
	line := `time="2026-08-19T12:49:24-04:00" level=fatal msg="failed to create instance \"demo\": disk is full"`
	feed(s, line+"\n", errors.New("start failed"))
	if buf.String() != line+"\n" {
		t.Errorf("lima's own error did not survive:\n%s", buf.String())
	}
}

func TestVerifyOutputIsRenderedAsCheckItems(t *testing.T) {
	// vm/verify.sh's output is ptrbox's already and reads as assertions, so
	// it is re-rendered rather than translated - and with colour off that
	// rendering is the guest script's own layout, byte for byte.
	s, buf := newStream(t)
	feed(s, "  sudo removed           OK\n  firewall active        FAIL - not active\n", nil)

	want := "  sudo removed           OK\n  firewall active        FAIL - not active\n"
	if buf.String() != want {
		t.Errorf("check items read\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestVerboseIsTheStreamUntouched(t *testing.T) {
	text := transcript(t)
	buf := &bytes.Buffer{}
	s := &Stream{Out: ui.Printer{W: buf}, Verbose: true}
	feed(s, text, nil)
	if buf.String() != text {
		t.Errorf("--verbose changed the stream:\n%s", buf.String())
	}
}

func TestAFailedInvocationReplaysItsRawBytes(t *testing.T) {
	s, buf := newStream(t)
	feed(s, transcript(t), errors.New("start failed"))
	buf.Reset()

	s.Replay()
	replayed := buf.String()
	if !strings.Contains(replayed, "what limactl printed") {
		t.Errorf("the replay is not introduced:\n%s", replayed)
	}
	for _, line := range lines(transcript(t)) {
		if !strings.Contains(replayed, line) {
			t.Fatalf("the replay is missing %q:\n%s", line, replayed)
		}
	}
}

func TestASuccessfulInvocationReplaysNothing(t *testing.T) {
	s, buf := newStream(t)
	feed(s, transcript(t), nil)
	buf.Reset()

	s.Replay()
	if buf.Len() != 0 {
		t.Errorf("a successful run replayed anyway:\n%s", buf.String())
	}
}

func TestANewInvocationForgetsTheLastOne(t *testing.T) {
	s, buf := newStream(t)
	feed(s, `level=info msg="something from the run before"`+"\n", errors.New("failed"))
	feed(s, `level=info msg="the run that matters"`+"\n", errors.New("failed"))
	buf.Reset()

	s.Replay()
	if strings.Contains(buf.String(), "the run before") {
		t.Errorf("the replay carried lines from an earlier invocation:\n%s", buf.String())
	}
}

func TestANewInvocationRestartsTheClock(t *testing.T) {
	// `ptrbox new` starts the VM twice - creation, then the reboot. Elapsed
	// time is measured from the first line of an invocation, so the second
	// one counts from zero again rather than from an hour ago.
	s, buf := newStream(t)
	feed(s, transcript(t), nil)
	feed(s, transcript(t), nil)

	if got := strings.Count(buf.String(), "connecting (1/3): ssh [33s]"); got != 2 {
		t.Errorf("the second invocation timed itself from the first (%d matches):\n%s", got, buf.String())
	}
}

func TestALineSplitAcrossWritesIsStillOneLine(t *testing.T) {
	// A pipe hands over whatever has arrived, which is not whole lines.
	// Translating half a sentence would be a translation of the wrong thing.
	whole, wholeBuf := newStream(t)
	feed(whole, transcript(t), nil)

	byByte, byteBuf := newStream(t)
	byByte.Begin(nil)
	for _, b := range []byte(transcript(t)) {
		byByte.Write([]byte{b})
	}
	byByte.End(nil)

	if byteBuf.String() != wholeBuf.String() {
		t.Errorf("byte-at-a-time output differs:\n%s\n---\n%s", byteBuf.String(), wholeBuf.String())
	}
}

func TestAnUnterminatedLastLineIsStillPrinted(t *testing.T) {
	s, buf := newStream(t)
	feed(s, `level=info msg="no newline at the end"`, nil)
	if !strings.Contains(buf.String(), "no newline at the end") {
		t.Errorf("the trailing partial line was swallowed:\n%q", buf.String())
	}
}

func TestTheImageNameFallsBackToSomethingTrue(t *testing.T) {
	buf := &bytes.Buffer{}
	s := &Stream{Out: ui.Printer{W: buf}}
	feed(s, transcriptLine(t, "Downloading the image (")+"\n", nil)
	if !strings.Contains(buf.String(), "downloading the VM image") {
		t.Errorf("no usable download line without a distro:\n%s", buf.String())
	}
}

func TestTheReplayIsBoundedButSaysSo(t *testing.T) {
	s, buf := newStream(t)
	var flood strings.Builder
	for i := 0; i < rawLimit+50; i++ {
		flood.WriteString(`level=info msg="line"` + "\n")
	}
	feed(s, flood.String(), errors.New("failed"))
	buf.Reset()

	s.Replay()
	if !strings.Contains(buf.String(), "earlier lines dropped") {
		t.Errorf("the replay was truncated silently:\n%s", buf.String()[:200])
	}
}

func TestTheImageDownloadIsOneHeadingAndKeepsItsURL(t *testing.T) {
	// lima logs the download three times over: an attempt carrying the URL, a
	// bare progress line, and a completion. One of them is the step; the
	// attempt is shown as it came, which is what keeps the URL.
	s, buf := newStream(t)
	feed(s, transcript(t), nil)
	out := buf.String()

	if got := strings.Count(out, "downloading the debian13 image"); got != 1 {
		t.Errorf("the download was announced %d times, want once:\n%s", got, out)
	}
	if !strings.Contains(out, transcriptLine(t, "Attempting to download the image")+"\n") {
		t.Errorf("the attempt line, which is where the URL is, was not shown:\n%s", out)
	}
	if !strings.Contains(out, "    image downloaded [26s]\n") {
		t.Errorf("the download was not closed off:\n%s", out)
	}
}

func TestAnAttemptToDownloadIsNotADownload(t *testing.T) {
	// The one the first smoke run caught. lima logs "Attempting to download
	// the image" BEFORE it consults the cache, so on the common path it is
	// followed by "Using cache" and nothing is fetched - and ptrbox announced
	// a several-minute download that never happened. Only the bare progress
	// line means bytes are moving.
	line := transcriptLine(t, "Attempting to download the image")
	s, buf := newStream(t)
	feed(s, line+"\n", nil)

	if strings.Contains(buf.String(), "downloading the") {
		t.Errorf("an attempt was announced as a download:\n%s", buf.String())
	}
	if buf.String() != line+"\n" {
		t.Errorf("the attempt line was not shown as it came:\n%q", buf.String())
	}
}

func TestOnlyTheVMImageDownloadIsCalledTheVMImageDownload(t *testing.T) {
	// lima downloads other things. Claiming one of them is the guest image
	// would be the wrong step rather than a missing one, which is the failure
	// mode this whole design is arranged against. Invented line, and safe to
	// invent: the assertion is that it is NOT recognised.
	s, buf := newStream(t)
	line := `time="2026-08-19T12:49:31-04:00" level=info msg="Attempting to download the nerdctl archive" location="https://example.test/n.tgz"`
	feed(s, line+"\n", nil)
	if strings.Contains(buf.String(), "downloading the debian13 image") {
		t.Errorf("an unrelated download was translated as the guest image:\n%s", buf.String())
	}
	if buf.String() != line+"\n" {
		t.Errorf("the line was not shown as it arrived:\n%s", buf.String())
	}
}

func TestACheckNameThatFillsItsColumnStillReadsAsACheck(t *testing.T) {
	// verify.sh pads names to 22 characters, so a name exactly that long is
	// followed by a single space. Requiring two would drop the longest check
	// in the suite back to untranslated output.
	s, buf := newStream(t)
	feed(s, "  allowed domain tunnels OK\n", nil)
	if got := buf.String(); got != "  allowed domain tunnels OK\n" {
		t.Errorf("check item reads %q", got)
	}
	if !checkOK.MatchString("  allowed domain tunnels OK") {
		t.Error("a full-width check name is not recognised as a check")
	}
}

// --- lima's log format -------------------------------------------------------

func TestAMessageIsAGoQuotedStringNotJustAQuotedOne(t *testing.T) {
	// The one that a "read to the next double quote" parser gets wrong: this
	// message contains escaped quotes, and cutting it at the first of them
	// would leave the rest of the line to be parsed as fields.
	line := transcriptLine(t, "Converting qcow2 image")
	if !strings.Contains(line, `\"`) {
		t.Fatal("the captured line no longer contains an escaped quote")
	}

	e, ok := parseEntry(line)
	if !ok {
		t.Fatalf("the line did not parse as a log entry:\n%s", line)
	}
	if !strings.Contains(e.msg, `cache: "/Users/you/`) {
		t.Errorf("the escaped quotes did not survive unquoting:\n%s", e.msg)
	}
	if !strings.HasSuffix(e.msg, `raw"`) {
		t.Errorf("the message was cut short:\n%s", e.msg)
	}
}

func TestTheStructuredFieldsOfALineAreRead(t *testing.T) {
	// Bare values, an empty one and a quoted one. Nothing renders from these
	// today, but parsing is all-or-nothing: if `digest=` defeated the parser,
	// the whole line would drop to verbatim - a silent loss of exactly the
	// kind this package exists to notice.
	line := transcriptLine(t, "Attempting to download the image")
	fields := parseFields(line)
	if fields == nil {
		t.Fatalf("the download line did not parse:\n%s", line)
	}
	for key, want := range map[string]string{
		"level":    "info",
		"arch":     "aarch64",
		"digest":   "",
		"location": "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2",
	} {
		if got, ok := fields[key]; !ok || got != want {
			t.Errorf("field %q is %q (present: %v), want %q", key, got, ok, want)
		}
	}
	if e, ok := parseEntry(line); !ok || !e.hasWhen {
		t.Error("the timestamp did not parse")
	}
}

func TestEveryCapturedLineIsEitherALogEntryOrKnownNotToBe(t *testing.T) {
	// The parser is all-or-nothing on purpose, so this is the check that the
	// fixture and the parser still agree about which lines are which. Exactly
	// one captured line is not logrus: the download progress line.
	var plain []string
	for _, line := range lines(transcript(t)) {
		if _, ok := parseEntry(line); !ok {
			plain = append(plain, line)
		}
	}
	if len(plain) != 1 || !strings.HasPrefix(plain[0], "Downloading the image (") {
		t.Errorf("the non-log lines of the capture are %q, want just the download progress line", plain)
	}
}

func TestALineWithoutATimestampIsStillTranslated(t *testing.T) {
	// Elapsed times come from lima's clock, so a line that carries none is
	// narrated without one rather than with a number ptrbox made up.
	s, buf := newStream(t)
	feed(s, "level=info msg=\"[hostagent] Waiting for the essential requirement 1 of 3: `ssh`\"\n", nil)
	if got := buf.String(); got != "ptrbox: connecting (1/3): ssh\n" {
		t.Errorf("reads %q", got)
	}
}
