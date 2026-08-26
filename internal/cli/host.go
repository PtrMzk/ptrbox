package cli

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
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

// How long waitForPort keeps asking, and how often. The deadline is generous
// on purpose: it is only spent while the port is down, and the false negative
// it prevents fails a whole install.
const (
	forwardDeadline = 15 * time.Second
	forwardPoll     = 250 * time.Millisecond
)

// sleep is a variable so tests that hold the port down do not spend the
// deadline in real time.
var sleep = time.Sleep

// waitForPort is portInUse with patience: true as soon as something listens,
// false only once the deadline has passed with nothing there.
//
// The patience is for Lima. A static port forward is published only while the
// guest port has a listener, so restarting the service that owns it - which
// Ensure does whenever the pushed squid config changed - tears the host
// forward down and re-publishes it a beat later, once squid is back up and
// the guest agent's event has reached the hostagent. A one-shot probe right
// after the restart races that beat, and the first smoke run to actually
// change the config lost it: a healthy proxy, failed for being checked a
// second too early.
func waitForPort(port int, deadline time.Duration) bool {
	for waited := time.Duration(0); ; waited += forwardPoll {
		if portInUse(port) {
			return true
		}
		if waited >= deadline {
			return false
		}
		sleep(forwardPoll)
	}
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
