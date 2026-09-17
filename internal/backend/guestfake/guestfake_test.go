package guestfake_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/backend/guestfake"
	"github.com/PtrMzk/ptrbox/internal/config"
)

// These run Exec with no backend in front of it. The behaviours themselves are
// exercised at length through limafake; what is pinned here is the contract a
// second fake builds on - a guest argv in, with no `shell <vm> --` around it.

func TestFilesWrittenInAGuestAreReadBackFromThatGuestOnly(t *testing.T) {
	var g guestfake.Guest
	if err := g.Exec("one", []string{"sudo", "tee", "/etc/x"}, strings.NewReader("body\n"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := g.Exec("one", []string{"sudo", "cat", "/etc/x"}, nil, &out, io.Discard); err != nil || out.String() != "body\n" {
		t.Fatalf("cat = %q, %v", out.String(), err)
	}

	var stderr bytes.Buffer
	if err := g.Exec("two", []string{"sudo", "cat", "/etc/x"}, nil, io.Discard, &stderr); err == nil {
		t.Error("a file written in one VM was readable from another")
	}
	if !strings.Contains(stderr.String(), "No such file") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestStdinIsLoggedAndNeverAnArgument(t *testing.T) {
	var g guestfake.Guest
	err := g.Exec("box", []string{"bash", "-c", "cat >> ~/.profile"}, strings.NewReader("secret\n"), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Stdins) != 1 || g.Stdins[0] != "secret\n" {
		t.Errorf("Stdins = %q", g.Stdins)
	}
	g.Reset()
	if len(g.Stdins) != 0 {
		t.Error("Reset kept the stdin log")
	}
}

func TestWhichVerificationFailsIsDecidedByTheVM(t *testing.T) {
	g := guestfake.Guest{ProxyVerifyFails: true}
	verify := []string{"bash", "-lc", "#!/bin/bash\n..."}

	if err := g.Exec(config.ProxyVM, verify, nil, io.Discard, io.Discard); err == nil {
		t.Error("the proxy's verification passed with ProxyVerifyFails set")
	}
	if err := g.Exec("sandbox", verify, nil, io.Discard, io.Discard); err != nil {
		t.Errorf("a sandbox's verification failed on the proxy's flag: %v", err)
	}

	g = guestfake.Guest{VerifyFails: true}
	if err := g.Exec("sandbox", verify, nil, io.Discard, io.Discard); err == nil {
		t.Error("a sandbox's verification passed with VerifyFails set")
	}
	if err := g.Exec(config.ProxyVM, verify, nil, io.Discard, io.Discard); err != nil {
		t.Errorf("the proxy's verification failed on the sandbox's flag: %v", err)
	}
}

func TestTheLogKeepsScriptsOutOfTheCallLine(t *testing.T) {
	var g guestfake.Guest
	g.Record([]string{"shell", "box", "--", "bash", "-lc", "line one\nline two"})
	g.Record([]string{"stop", "box"})

	if got, want := g.CallLog(), "shell box -- bash -lc <script:1>\nstop box"; got != want {
		t.Errorf("CallLog = %q, want %q", got, want)
	}
	if len(g.Scripts) != 1 || g.Scripts[0] != "line one\nline two" {
		t.Errorf("Scripts = %q", g.Scripts)
	}
	if !g.InOrder("^shell box", "^stop box") || g.InOrder("^stop box", "^shell box") {
		t.Error("InOrder is wrong")
	}
	if g.Called("^delete") {
		t.Error("Called matched a call that was never made")
	}
}
