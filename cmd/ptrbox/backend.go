package main

import (
	"runtime"

	"github.com/PtrMzk/ptrbox/internal/backend"
	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/multipass"
	"github.com/PtrMzk/ptrbox/internal/narrate"
)

// hostBackend is the VM backend for the machine this runs on: Multipass on
// Windows, lima everywhere else. A switch on the OS at run time rather than a
// pair of build-tagged files, for the reason config.Host is one: the choice
// is ordinary code the suite can read on any machine. Both backends' output
// goes through the narrator, like every other informational line.
func hostBackend(narrator *narrate.Stream) backend.Backend {
	if runtime.GOOS == "windows" {
		// Multipass prints no log stream, only plain lines on stdout; every
		// one is shown verbatim, dimmed, and a failure replays them under
		// this name.
		narrator.Tool = multipass.Binary
		return multipass.Backend{Client: multipass.New(multipass.Exec(), narrator, narrator)}
	}
	narrator.Tool = lima.Binary
	return lima.Backend{Client: &lima.Client{Runner: lima.Exec{}, Stdout: narrator, Stderr: narrator}}
}
