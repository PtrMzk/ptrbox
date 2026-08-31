package cli

// A guard on the test harnesses themselves.
//
// vm/verify.sh is EXECUTED by this package, not linted, and it reads
// $HOME/.profile and $HOME/.claude/.credentials.json. A harness that runs it
// without owning HOME therefore inspects the developer's real credential
// paths - read-only and harmless in effect, wrong in principle, and precisely
// the direction this project spends its time preventing. It happened: two
// harnesses shipped without it.
//
// The same argument covers the setuid sweep, which walks a filesystem from a
// root it is handed. Left at the default that root is /, and on a Mac that is
// minutes of walking the developer's disk - the freeze that prompted this.
//
// Checked by reading the source rather than by running anything, because the
// failure is a harness forgetting to do something, and there is nothing to
// observe when it does.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const guardFile = "harnesshygiene_test.go"

// harnessFiles are the test files that write verify.sh into a temp dir and
// execute it. Anything doing that owes HOME.
func harnessFiles(t *testing.T) []string {
	t.Helper()
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	var harnesses []string
	for _, name := range names {
		// This file quotes the patterns it looks for, so it matches itself.
		if name == guardFile {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), `asset(t, "vm/verify.sh")`) {
			harnesses = append(harnesses, name)
		}
	}
	if len(harnesses) == 0 {
		t.Fatal("no harness runs verify.sh - this guard is looking for the wrong thing")
	}
	return harnesses
}

func TestEveryHarnessThatRunsVerifyOwnsHome(t *testing.T) {
	for _, name := range harnessFiles(t) {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `t.Setenv("HOME"`) {
			t.Errorf("%s executes vm/verify.sh without setting HOME, so it reads "+
				"the developer's real ~/.profile and ~/.claude", name)
		}
	}
}

func TestNoHarnessSweepsTheRealFilesystem(t *testing.T) {
	// verify.sh takes the sweep root as its second argument. A harness that
	// passes only the state directory gets the default, which is /.
	for _, name := range harnessFiles(t) {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"verify.sh"), `) {
			continue // writes verify.sh but never executes it
		}
		if strings.Contains(string(body), `"verify.sh"), state).CombinedOutput()`) {
			t.Errorf("%s runs vm/verify.sh without a sweep root, so it walks the "+
				"whole filesystem - a second in a guest, minutes on a Mac", name)
		}
	}
}
