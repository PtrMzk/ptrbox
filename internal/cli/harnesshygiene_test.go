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

	"github.com/PtrMzk/ptrbox/internal/config"
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

// Stub executables are built once per test binary, not once per test. A
// per-test stub directory means a newly created executable that has never been
// run, and macOS screens each of those on first exec: it is the difference
// between a one-second package and a thirty-second one there, invisible on
// Linux where fresh executables cost nothing.
func TestNoHarnessBuildsItsStubsPerTest(t *testing.T) {
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == guardFile {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), `filepath.Join(dir, "stubs")`) {
			t.Errorf("%s builds a stub directory per test; use sharedStubs so the "+
				"executables are created once per binary", name)
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

// The harness owns every path ptrbox resolves, not just HOME.
//
// This is the write side of the guard above, and it is not hypothetical: run
// natively on a Windows PC - which became possible the day Go was installed on
// one - `go test ./...` wrote fifteen port allocations, a rendered VM config
// and a per-VM settings file into the developer's LIVE ptrbox state, because
// the generated dir comes from %LOCALAPPDATA% there rather than from HOME, and
// a sixteen-slot port range full of test VMs is a `ptrbox new` that fails for
// no visible reason. newHarness pins config.Host for that reason; this asserts
// the property rather than the mechanism, so a path that starts resolving
// somewhere new fails here instead of on somebody's machine.
func TestTheHarnessOwnsEveryPathPtrboxResolves(t *testing.T) {
	h := newHarness(t)
	// The harness's temp root: its home and its config file both live here.
	root := filepath.Dir(h.home)
	for _, tc := range []struct{ name, path string }{
		{"config dir", config.Dir()},
		{"per-VM config dir", config.VMDir()},
		{"per-VM allowlists", config.VMAllowlistDir()},
		{"generated dir", config.GeneratedDir()},
		{"state dir", config.StateDir()},
		{"transcripts", config.TranscriptDir()},
	} {
		if tc.path != root && !strings.HasPrefix(tc.path, root+string(filepath.Separator)) {
			t.Errorf("the %s resolves to %s, outside the harness root %s - the suite "+
				"is reading and writing the real machine's ptrbox state", tc.name, tc.path, root)
		}
	}
}
