package cli

import (
	"fmt"
	"net"
	"os"
	"os/exec"
)

// lookPath reports whether a command exists on PATH. A variable so tests can
// answer for a host that does not have the tool.
var lookPath = func(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}

// portInUse reports whether something already listens on 127.0.0.1:port.
//
// A bind attempt rather than shelling out to lsof: one fewer host dependency,
// and it answers the question that actually matters - could the proxy's port
// forward be taken by something else.
var portInUse = func(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	listener.Close()
	return false
}

// openEditor is the default Env.Editor: $VISUAL, else $EDITOR, else vi.
func openEditor(path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// DefaultEditor is exported so main can install it without knowing how it is
// resolved.
var DefaultEditor = openEditor
