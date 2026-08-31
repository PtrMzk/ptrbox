package cli

// Carrying settings across a config update.
//
// The property that matters: after an update, every setting the user had made
// is still made. This code exists because `ptrbox install --update` replaced
// the settings file wholesale and a real upgrade silently emptied a real
// config - the .bak was there, but nothing said "you have just lost
// PTRBOX_GO".

import (
	"strings"
	"testing"
)

// shipped is the shape of the annotated example: every key present, every one
// commented out, prose between them.
const shipped = `# ptrbox configuration.

# Where repos go.
#PTRBOX_REPO_ROOT="$HOME/code"

# [vm] Language runtimes, all off.
#PTRBOX_GO=false
#PTRBOX_NODE=false

# [vm] What nvm install gets.
#PTRBOX_NODE_VERSION=lts

# Sizing.
#PTRBOX_CPUS=4
`

func TestAnUpdateKeepsEverySettingTheUserHadMade(t *testing.T) {
	current := `# ptrbox configuration.
PTRBOX_GO=true
#PTRBOX_NODE=false
PTRBOX_CPUS=8
`
	result, ok := mergeSettings([]byte(current), []byte(shipped))
	if !ok {
		t.Fatal("the merge declined a file that parses")
	}
	merged := string(result.merged)

	for _, want := range []string{"PTRBOX_GO=true", "PTRBOX_CPUS=8"} {
		if !strings.Contains(merged, want) {
			t.Errorf("%q did not survive the update:\n%s", want, merged)
		}
	}
	// The default it replaced is gone, not left above it as a second value.
	if strings.Contains(merged, "#PTRBOX_GO=false") {
		t.Errorf("the shipped default is still there alongside the user's value:\n%s", merged)
	}
	// A key the user left commented stays commented - "I did not set this" is
	// itself a choice, and uncommenting it would invent one.
	if !strings.Contains(merged, "#PTRBOX_NODE=false") {
		t.Errorf("a key the user had not set was turned on:\n%s", merged)
	}
	// Reported by the name the user typed, prefix and all, so the message
	// names something they can find in the file.
	if got := strings.Join(result.carried, " "); got != "PTRBOX_GO PTRBOX_CPUS" {
		t.Errorf("carried = %q, want the two keys in file order", got)
	}
}

// The new file's text is the point of updating: its wording, its ordering, and
// any key it has gained.
func TestAnUpdateBringsTheNewFilesProseAndNewKeys(t *testing.T) {
	current := "PTRBOX_CPUS=8\n"
	result, _ := mergeSettings([]byte(current), []byte(shipped))
	merged := string(result.merged)

	if !strings.Contains(merged, "# [vm] Language runtimes, all off.") {
		t.Errorf("the new prose is missing:\n%s", merged)
	}
	// A key that did not exist in the old file arrives, commented.
	if !strings.Contains(merged, "#PTRBOX_NODE_VERSION=lts") {
		t.Errorf("a key new in this version is missing:\n%s", merged)
	}
}

// Verbatim, not reserialised. The user wrote $HOME because they meant the
// variable; writing back the path it expands to today would quietly freeze it.
func TestAnUpdateCopiesTheUsersLineNotItsMeaning(t *testing.T) {
	current := `PTRBOX_REPO_ROOT="$HOME/elsewhere"` + "\n"
	result, _ := mergeSettings([]byte(current), []byte(shipped))

	if !strings.Contains(string(result.merged), `PTRBOX_REPO_ROOT="$HOME/elsewhere"`) {
		t.Errorf("the line was rewritten rather than carried:\n%s", result.merged)
	}
}

// A setting with no matching key must not evaporate. It is the one thing an
// upgrade can take away without anyone noticing.
func TestASettingWithNoCurrentKeyIsKeptAndReported(t *testing.T) {
	current := "PTRBOX_TOOLCHAIN=\"node uv\"\nSCRATCH=hello\nPTRBOX_CPUS=8\n"
	result, ok := mergeSettings([]byte(current), []byte(shipped))
	if !ok {
		t.Fatal("the merge declined a file that parses")
	}
	merged := string(result.merged)

	if got := strings.Join(result.orphans, " "); got != "PTRBOX_TOOLCHAIN SCRATCH" {
		t.Errorf("orphans = %q, want both the retired key and the scratch variable", got)
	}
	for _, want := range []string{"PTRBOX_TOOLCHAIN=", "SCRATCH=hello", "carried over from your previous"} {
		if !strings.Contains(merged, want) {
			t.Errorf("%q is not in the merged file:\n%s", want, merged)
		}
	}
	// Commented, so the result still parses as a config file.
	for _, line := range strings.Split(merged, "\n") {
		if strings.HasPrefix(line, "PTRBOX_TOOLCHAIN") || strings.HasPrefix(line, "SCRATCH") {
			t.Errorf("an orphan was carried over live, so the file may not load: %q", line)
		}
	}
}

// Shell semantics: the file is read top to bottom, so a key set twice means
// the last one.
func TestTheLastAssignmentWins(t *testing.T) {
	current := "PTRBOX_CPUS=2\nPTRBOX_CPUS=16\n"
	result, _ := mergeSettings([]byte(current), []byte(shipped))

	if !strings.Contains(string(result.merged), "PTRBOX_CPUS=16") {
		t.Errorf("the later value did not win:\n%s", result.merged)
	}
	if strings.Contains(string(result.merged), "PTRBOX_CPUS=2") {
		t.Errorf("the earlier value survived too:\n%s", result.merged)
	}
}

// A config that has stopped parsing is not something to rewrite by guesswork.
func TestAnUnparsableFileDeclinesTheMerge(t *testing.T) {
	if _, ok := mergeSettings([]byte("this is not a config file\n"), []byte(shipped)); ok {
		t.Error("the merge accepted a file it cannot read")
	}
}

// Nothing set is a legitimate state, and the update is then just the new text.
func TestAFileWithNoSettingsMergesToTheShippedFile(t *testing.T) {
	result, ok := mergeSettings([]byte("# I changed only a comment\n"), []byte(shipped))
	if !ok {
		t.Fatal("the merge declined a comment-only file")
	}
	if string(result.merged) != shipped {
		t.Errorf("merged =\n%s\nwant the shipped file unchanged", result.merged)
	}
	if describeMerge(result) != "you had set nothing in it" {
		t.Errorf("describeMerge = %q", describeMerge(result))
	}
}
