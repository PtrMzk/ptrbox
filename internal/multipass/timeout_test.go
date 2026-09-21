package multipass_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/PtrMzk/ptrbox/internal/backend"
	"github.com/PtrMzk/ptrbox/internal/multipass"
)

// A wedged daemon hangs every client call. The runner kills the call at its
// deadline and says how to recover, rather than hanging ptrbox with it.
func TestACallPastItsDeadlineIsKilledAndNamesTheRecovery(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not available")
	}
	r := multipass.TimedRunner{Binary: "sleep", Timeout: func([]string) time.Duration { return 50 * time.Millisecond }}
	start := time.Now()
	err := r.Run(backend.Cmd{Args: []string{"30"}})
	if err == nil {
		t.Fatal("a call past its deadline returned no error")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the call was not killed at its deadline: took %s", time.Since(start))
	}
	for _, want := range []string{"did not return within", "Stop-Process -Name multipassd -Force", "Start-Service Multipass"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v\nmissing %q", err, want)
		}
	}
}

func TestACallWithinItsDeadlineIsUntouched(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true is not available")
	}
	r := multipass.TimedRunner{Binary: "true", Timeout: func([]string) time.Duration { return time.Minute }}
	if err := r.Run(backend.Cmd{Args: []string{}}); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestTheRealRunnerHasADeadlineForEveryVerb(t *testing.T) {
	r := multipass.Exec()
	if r.Binary != "multipass" || r.Timeout == nil {
		t.Fatalf("Exec() = %+v", r)
	}
	// A launch is boot-1 provisioning and may take longest; a listing only
	// talks to the daemon and must fail fastest, since that is where a wedge
	// shows first.
	launch, list := r.Timeout([]string{"launch", "--name", "x"}), r.Timeout([]string{"list", "--format", "json"})
	if !(launch > list) || list > 5*time.Minute || launch < 20*time.Minute {
		t.Errorf("launch %s, list %s", launch, list)
	}
	if r.Timeout([]string{"launch"}) < 1200*time.Second {
		t.Error("the launch deadline is shorter than launch's own --timeout")
	}
}
