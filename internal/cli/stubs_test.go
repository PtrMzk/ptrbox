package cli

// Stub executables, built once per test binary and shared.
//
// Every guest-script harness needs a directory of stubs on PATH so that
// verify.sh and the provision scripts reach fakes rather than the developer's
// curl, apt-get and systemctl. Built per-test, that is about six newly created
// executables times forty tests: ~240 files that have never been run before.
//
// On Linux that is free and the package runs in a second. On macOS it is most
// of a thirty-second run, because a newly written executable is screened on
// first exec - ~50ms a time, which is an order of magnitude more than fork and
// exec cost. Sharing the directory makes it six screenings instead of 240, and
// every later run of a stub is the same inode at the same path.
//
// Sharing is safe because the stubs are pure: everything that varies between
// tests arrives through environment variables the test still sets for itself
// (APT_LOG, APT_KNOWN, APT_ABSENT, CURL_LOG). The one exception is
// toolchainscript_test.go, whose curl stub WRITES into the stub directory -
// that is how it produces a node and a uv on PATH - so it keeps a private one
// and is deliberately not converted.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// stubRoot holds every shared stub set for the lifetime of the test binary.
var stubRoot string

type stubSet struct {
	once sync.Once
	dir  string
}

var stubSets sync.Map // name -> *stubSet

func TestMain(m *testing.M) {
	var err error
	stubRoot, err = os.MkdirTemp("", "ptrbox-stubs")
	if err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(stubRoot)
	os.Exit(code)
}

// sharedStubs returns the directory holding the named stub set, building it on
// first use. Callers put it FIRST on PATH; the returned directory is shared, so
// nothing may write into it.
func sharedStubs(t *testing.T, name string, build func(dir string)) string {
	t.Helper()
	value, _ := stubSets.LoadOrStore(name, &stubSet{})
	set := value.(*stubSet)
	set.once.Do(func() {
		dir := filepath.Join(stubRoot, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		build(dir)
		set.dir = dir
	})
	if set.dir == "" {
		t.Fatalf("stub set %q was not built", name)
	}
	return set.dir
}

// quietStubs are the ones every harness needs: verify.sh's egress probes and
// privilege checks, answered without a network or a real systemd. curl exiting
// non-zero is what makes "direct egress blocked" pass in a second rather than
// spending three five-second timeouts discovering there is no proxy.
func quietStubs(dir string) {
	for _, name := range []string{"curl", "sudo", "systemctl", "ss"} {
		mustWriteScript(filepath.Join(dir, name), "#!/bin/sh\nexit 1\n")
	}
	mustWriteScript(filepath.Join(dir, "mount"), "#!/bin/sh\nexit 0\n")
}

// mustWriteScript is writeScript without a *testing.T, for use inside a
// sync.Once where the T that happens to be first is not the T that matters.
func mustWriteScript(path, body string) {
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		panic(err)
	}
}
