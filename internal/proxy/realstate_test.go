package proxy_test

// The package may not touch the real machine's ptrbox state.
//
// harnesshygiene_test.go in internal/cli asserts this per harness: every path
// resolved UNDER a harness lands inside its temp root. That cannot see a test
// which resolves a path before any harness exists - and one did. seedFor
// wrote a per-VM config with no HOME override, no PTRBOX_CONFIG and no
// platform pin in force, so `go test ./internal/proxy` created
// ~/.config/ptrbox/vms/demo on the developer's own machine, every run, on
// every platform; on the Windows PC the same call landed in %APPDATA% beside
// the live sandboxes, which is the artifact that outlived item 87's fix and
// had no explanation until this test was written.
//
// So this asserts the property from the outside instead: whatever the tests
// do, the real config directory is exactly as they found it. It runs once per
// package rather than per test, which is the point - a new test helper that
// forgets to isolate itself fails here without having to be noticed.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
)

func TestMain(m *testing.M) {
	// Resolved before anything overrides HOME or PTRBOX_CONFIG, so this is
	// the real machine's directory and not a harness's.
	real := config.Dir()
	before := treeOf(real)

	code := m.Run()

	if after := treeOf(real); after != before {
		fmt.Fprintf(os.Stderr,
			"\nthe suite wrote the real ptrbox config directory %s\n"+
				"  before: %s\n  after:  %s\n"+
				"a test resolved a ptrbox path without isolating itself first - see isolate() in proxy_test.go\n",
			real, before, after)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// treeOf is every path under root, as one comparable string. Absent reads as
// empty, so a directory the suite CREATES is a difference too - which is the
// common case on a machine that has never run ptrbox install.
func treeOf(root string) string {
	var paths []string
	_ = filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is not this test's business
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)
	return strings.Join(paths, "\n")
}
