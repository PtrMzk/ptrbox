package narrate

// The translator against what multipass prints - the Windows backend's
// captures (testdata/PROVENANCE, the multipass-* files). Multipass writes no
// log stream: plain lines on stdout, no timestamps, no levels. Nothing in
// them matches a lima pattern and nothing should, so the whole of the
// behaviour under test is the designed degradation - every line shown
// verbatim, dimmed, none dropped, none mistaken for a step - plus the check
// lines of the guest's own verification, which are backend-neutral and must
// still render as check items behind them.
//
// No multipass pattern exists, and this file is where one would be justified
// first: by a line in a captured transcript, never by memory.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func multipassTranscripts(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"launch":         fixture(t, "multipass-launch.log"),
		"launch-nomount": fixture(t, "multipass-launch-nomount.log"),
		"start-failed":   fixture(t, "multipass-start-failed.log"),
		"start-warnings": fixture(t, "multipass-start.log"),
	}
}

func TestEveryMultipassLineIsShownVerbatimAndNoneBecomesAStep(t *testing.T) {
	for name, text := range multipassTranscripts(t) {
		t.Run(name, func(t *testing.T) {
			s, buf := newStream(t)
			s.Tool = "multipass"
			s.Begin([]string{"launch", "--name", "demo"})
			s.Write([]byte(text))
			s.End(nil)

			// With colour off, Dim is Raw: the output IS the transcript.
			if got := buf.String(); got != text {
				t.Errorf("output is not the transcript verbatim:\n%s\nwant:\n%s", got, text)
			}
			for _, line := range lines(buf.String()) {
				if strings.HasPrefix(line, "ptrbox:") {
					t.Errorf("a multipass line was translated into a step: %q", line)
				}
			}
		})
	}
}

func TestAFailedMultipassInvocationReplaysUnderItsOwnName(t *testing.T) {
	s, buf := newStream(t)
	s.Tool = "multipass"
	text := fixture(t, "multipass-start-failed.log")
	s.Begin([]string{"start", "scratch2"})
	s.Write([]byte(text))
	s.End(errors.New("exit status 2"))
	buf.Reset()
	s.Replay()

	out := buf.String()
	if !strings.Contains(out, "what multipass printed:") {
		t.Errorf("the replay names the wrong tool:\n%s", out)
	}
	if strings.Contains(out, "limactl") {
		t.Errorf("lima's name in a multipass replay:\n%s", out)
	}
	for _, line := range lines(text) {
		if !strings.Contains(out, line) {
			t.Errorf("the replay dropped %q:\n%s", line, out)
		}
	}
}

func TestTheReplayNamesLimactlWhenNoToolIsSet(t *testing.T) {
	// Every existing caller and pattern is lima's; an unset Tool must read
	// exactly as it always did.
	s, buf := newStream(t)
	s.Begin([]string{"start", "demo"})
	s.Write([]byte("FATA[0001] boom\n"))
	s.End(errors.New("exit status 1"))
	buf.Reset()
	s.Replay()
	if !strings.Contains(buf.String(), "what limactl printed:") {
		t.Errorf("replay = %q", buf.String())
	}
}

func TestVerificationCheckLinesStillRenderBehindMultipassOutput(t *testing.T) {
	// The check lines are verify.sh's, backend-neutral; a launch transcript
	// ahead of them must not change how they read.
	s, buf := newStream(t)
	s.Tool = "multipass"
	s.Begin([]string{"exec", "demo", "--", "bash", "-lc", "..."})
	s.Write([]byte(fixture(t, "multipass-launch.log")))
	// Spelled with verify.sh's own printf formats, so a padding change there
	// is a failure here rather than a line that quietly stops matching.
	ok := fmt.Sprintf("  %-22s OK\n", "sudo removed")
	fail := fmt.Sprintf("  %-22s FAIL - %s\n", "setuid stripped", "unexpected setuid/setgid: /usr/bin/sudo")
	s.Write([]byte(ok + fail))
	s.End(nil)
	out := buf.String()
	for _, want := range []string{ok, fail} {
		if !strings.Contains(out, want) {
			t.Errorf("a check line did not render:\n%s", out)
		}
	}
}
