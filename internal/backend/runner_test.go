package backend_test

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/backend"
)

func TestErrorNamesTheBinaryAndWhatItSaid(t *testing.T) {
	cause := errors.New("exit status 1")
	err := &backend.Error{
		Binary: "limactl",
		Args:   []string{"shell", "box", "--", "true"},
		Stderr: "  instance \"box\" does not exist\n",
		Err:    cause,
	}
	want := `limactl shell box -- true: exit status 1: instance "box" does not exist`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("the cause is not reachable through Unwrap")
	}
}

func TestErrorWithNothingOnStderrIsJustTheStatus(t *testing.T) {
	err := &backend.Error{Binary: "multipass", Args: []string{"list"}, Stderr: " \n", Err: errors.New("exit status 2")}
	if got, want := err.Error(), "multipass list: exit status 2"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestExecRunnerReportsAMissingBinary(t *testing.T) {
	if (backend.ExecRunner{Binary: "ptrbox-no-such-binary"}).Available() {
		t.Error("a binary that is not on PATH reported available")
	}
}

// The package sits under everything that talks to a VM, so it may depend on
// none of it: config, lima and cli import backend, never the reverse. An
// import cycle would say so eventually; this says so at the line that adds it.
func TestThePackageImportsNothingFromPtrbox(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no source files found: the test is looking in the wrong place")
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			if strings.Contains(imp.Path.Value, "PtrMzk/ptrbox") {
				t.Errorf("%s imports %s", file, imp.Path.Value)
			}
		}
	}
}
