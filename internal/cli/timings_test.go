package cli

import "testing"

// The records come from a shell trap in the guest, so the parser is asked to
// be forgiving about the file and exact about the lines it accepts.
func TestTimingsAreSummedByScriptInFirstSeenOrder(t *testing.T) {
	got := formatTimings(parseTimings(
		"10-base 100 176\n90-harden 176 176\n30-toolchain 176 240\n90-harden 300 302\n"))
	want := "10-base 1m16s, 90-harden 2s, 30-toolchain 1m04s"
	if got != want {
		t.Errorf("formatTimings = %q, want %q", got, want)
	}
}

func TestMalformedTimingLinesAreIgnored(t *testing.T) {
	got := formatTimings(parseTimings(
		"10-base 100 176\n" +
			"torn 100\n" + // a trap that died mid-write
			"words are not numbers\n" +
			"backwards 200 100\n" + // a clock that went the wrong way
			"\n"))
	if got != "10-base 1m16s" {
		t.Errorf("formatTimings = %q", got)
	}
}

func TestNoTimingsFormatsToNothing(t *testing.T) {
	if got := formatTimings(parseTimings("")); got != "" {
		t.Errorf("formatTimings = %q, want empty", got)
	}
	if got := formatTimings(nil); got != "" {
		t.Errorf("formatTimings(nil) = %q, want empty", got)
	}
}

func TestTimingDurationsReadLikeTheNarrators(t *testing.T) {
	for total, want := range map[int]string{0: "0s", 59: "59s", 60: "1m00s", 76: "1m16s", 3661: "61m01s"} {
		if got := seconds(total); got != want {
			t.Errorf("seconds(%d) = %q, want %q", total, got, want)
		}
	}
}
