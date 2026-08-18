package main

// The colour decision lives here because a terminal is a property of the
// process, not of a message - so this is where it is tested. Everything else
// in main is assembly, covered by the command tests.

import (
	"os"
	"reflect"
	"testing"
)

// tty stands in for "stderr is a terminal", which a test process's stderr is
// not. The rest of the decision is environment, and that is what these check.
func TestColorIsDeclinedWhateverTheTerminalSays(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"NO_COLOR", map[string]string{"NO_COLOR": "1", "TERM": "xterm-256color"}, nil},
		{"TERM=dumb", map[string]string{"TERM": "dumb"}, nil},
		{"TERM unset", map[string]string{"TERM": ""}, nil},
		{"--no-color", map[string]string{"TERM": "xterm-256color"}, []string{"--no-color"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if _, color := colorFlag(tc.args); color {
				t.Error("colour was enabled")
			}
		})
	}
}

func TestNoColorIsRemovedFromTheArguments(t *testing.T) {
	// No subcommand should have to know the flag exists - install parses its
	// own options strictly and would reject it.
	t.Setenv("TERM", "xterm-256color")
	args, _ := colorFlag([]string{"install", "--no-color", "--yes"})
	if want := []string{"install", "--yes"}; !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestTheArgumentsAreOtherwiseUntouched(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	in := []string{"new", "~/code/thing", "--color-me-impressed"}
	args, _ := colorFlag(in)
	if !reflect.DeepEqual(args, in) {
		t.Errorf("args = %v, want %v", args, in)
	}
}

func TestAPipedStderrIsNotStyled(t *testing.T) {
	// The test binary's stderr is not a character device, which is the same
	// answer a pipe or a CI log gives.
	t.Setenv("TERM", "xterm-256color")
	os.Unsetenv("NO_COLOR")
	if _, color := colorFlag(nil); color {
		t.Error("colour was enabled with stderr redirected")
	}
}
