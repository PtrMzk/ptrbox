package narrate

// The translator, against a `limactl start` transcript.
//
// The transcript is meant to be a real `limactl start` capture, because
// patterns written from memory tested against lines written from the same
// memory prove nothing. The checked-in copy is NOT one yet - it is
// reconstructed from lima's message shapes, and testdata/PROVENANCE says so
// and says how to replace it at the first `make smoke`.
//
// The properties asserted here are the ones the design rests on. Known shapes
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
	s, buf := newStream(t)
	feed(s, transcript(t), nil)
	out := buf.String()

	for _, want := range []string{
		"ptrbox: downloading the debian13 image (first boot only)",
		"ptrbox: booting (1/5): ssh [32s]",
		"ptrbox: booting (4/5): cloud-init to be completed [58s]",
		"ptrbox: checking (1/1): user probe 1/1 [3m33s]",
		"ptrbox: finishing (1/1): boot scripts must have finished [3m34s]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in the translation:\n%s", want, out)
		}
	}
}

func TestNothingIsDropped(t *testing.T) {
	// The rule the whole design rests on: the filter narrows what is loud,
	// never what is visible. One transcript line in, at least one line out.
	s, buf := newStream(t)
	text := transcript(t)
	feed(s, text, nil)

	in := len(strings.Split(strings.TrimSuffix(text, "\n"), "\n"))
	got := len(strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n"))
	if got < in {
		t.Errorf("%d lines in, %d out - the filter is hiding things:\n%s", in, got, buf.String())
	}
}

func TestARewordedLimaLineIsShownRatherThanGuessedAt(t *testing.T) {
	// A lima release that renames a requirement class must degrade to "shown
	// as lima wrote it", never to a step ptrbox invented.
	s, buf := newStream(t)
	line := `INFO[0032] [hostagent] Waiting for the crucial prerequisite 2 of 7: "something new"`
	feed(s, line+"\n", nil)

	out := buf.String()
	if !strings.Contains(out, "Waiting for the crucial prerequisite 2 of 7") {
		t.Errorf("the unrecognised line was not shown:\n%s", out)
	}
	if strings.Contains(out, "booting (2/7)") {
		t.Errorf("an unrecognised line was translated anyway:\n%s", out)
	}
}

func TestAnErrorFromLimaIsNotDimmedOrReworded(t *testing.T) {
	s, buf := newStream(t)
	feed(s, "FATA[0004] failed to create instance \"demo\": disk is full\n", errors.New("start failed"))
	if !strings.Contains(buf.String(), `FATA[0004] failed to create instance "demo": disk is full`) {
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
	for _, line := range strings.Split(strings.TrimSuffix(transcript(t), "\n"), "\n") {
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
	feed(s, "INFO[0001] something from the run before\n", errors.New("failed"))
	feed(s, "INFO[0002] the run that matters\n", errors.New("failed"))
	buf.Reset()

	s.Replay()
	if strings.Contains(buf.String(), "the run before") {
		t.Errorf("the replay carried lines from an earlier invocation:\n%s", buf.String())
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
	feed(s, "INFO[0002] no newline at the end", nil)
	if !strings.Contains(buf.String(), "no newline at the end") {
		t.Errorf("the trailing partial line was swallowed:\n%q", buf.String())
	}
}

func TestTheImageNameFallsBackToSomethingTrue(t *testing.T) {
	buf := &bytes.Buffer{}
	s := &Stream{Out: ui.Printer{W: buf}}
	feed(s, `INFO[0000] Downloading the image from "https://example.test/x.qcow2"`+"\n", nil)
	if !strings.Contains(buf.String(), "downloading the VM image") {
		t.Errorf("no usable download line without a distro:\n%s", buf.String())
	}
}

func TestTheReplayIsBoundedButSaysSo(t *testing.T) {
	s, buf := newStream(t)
	var flood strings.Builder
	for i := 0; i < rawLimit+50; i++ {
		flood.WriteString("INFO[0000] line\n")
	}
	feed(s, flood.String(), errors.New("failed"))
	buf.Reset()

	s.Replay()
	if !strings.Contains(buf.String(), "earlier lines dropped") {
		t.Errorf("the replay was truncated silently:\n%s", buf.String()[:200])
	}
}

func TestOnlyTheVMImageDownloadIsCalledTheVMImageDownload(t *testing.T) {
	// lima downloads other things. Claiming one of them is the guest image
	// would be the wrong step rather than a missing one, which is the failure
	// mode this whole design is arranged against.
	s, buf := newStream(t)
	feed(s, "INFO[0031] Downloading the nerdctl archive from \"https://example.test/n.tgz\"\n", nil)
	if strings.Contains(buf.String(), "downloading the debian13 image") {
		t.Errorf("an unrelated download was translated as the guest image:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "Downloading the nerdctl archive") {
		t.Errorf("the line was not shown at all:\n%s", buf.String())
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
