package cli

// ptrbox logs - reading the egress proxy log, which lives inside the proxy VM.

import (
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
)

const accessLog = `1691000001.000 12 127.0.0.1 TCP_TUNNEL/200 5000 CONNECT pypi.org:443 - HIER_DIRECT/1.2.3.4 -
1691000002.000  1 127.0.0.1 TCP_DENIED/403 4000 CONNECT example.com:443 - HIER_NONE/- text/html
1691000003.000  9 127.0.0.1 TCP_TUNNEL/200 7000 CONNECT api.anthropic.com:443 - HIER_DIRECT/5.6.7.8 -
1691000004.000  1 127.0.0.1 TCP_DENIED/403 4000 CONNECT telemetry.example:443 - HIER_NONE/- text/html
`

// withLog gives the harness a running proxy VM with a populated access log.
func withLog(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.fake.AddVM(config.ProxyVM, lima.StatusRunning)
	h.fake.WriteFile(config.ProxyVM, "/var/log/squid/access.log", accessLog)
	return h
}

func TestLogsPrintsTheTailOfTheProxyLog(t *testing.T) {
	h := withLog(t)
	h.mustRun("logs")
	for _, want := range []string{"pypi.org:443", "example.com:443"} {
		if !strings.Contains(h.stdout, want) {
			t.Errorf("output is missing %q", want)
		}
	}
}

func TestLogsReadsViaLimactlShellNotAHostPath(t *testing.T) {
	h := withLog(t)
	h.mustRun("logs")
	h.assertCalled("shell ptrbox-proxy -- sudo tail")
}

func TestDeniedShowsOnlyBlockedRequests(t *testing.T) {
	h := withLog(t)
	h.mustRun("logs", "--denied")
	for _, want := range []string{"example.com:443", "telemetry.example:443"} {
		if !strings.Contains(h.stdout, want) {
			t.Errorf("output is missing %q", want)
		}
	}
	if strings.Contains(h.stdout, "TCP_TUNNEL") {
		t.Errorf("allowed requests leaked into --denied:\n%s", h.stdout)
	}
}

func TestDeniedExplainsHowToAllowADomain(t *testing.T) {
	h := withLog(t)
	h.mustRun("logs", "--denied")
	h.assertOutputContains("ptrbox allow")
}

func TestNLimitsHowFarBackItReads(t *testing.T) {
	h := withLog(t)
	h.mustRun("logs", "-n", "1")
	if !strings.Contains(h.stdout, "telemetry.example:443") {
		t.Error("the last line is missing")
	}
	if strings.Contains(h.stdout, "pypi.org:443") {
		t.Error("-n did not limit the range")
	}
}

func TestNoDenialsIsNotAnError(t *testing.T) {
	h := newHarness(t)
	h.fake.AddVM(config.ProxyVM, lima.StatusRunning)
	h.fake.WriteFile(config.ProxyVM, "/var/log/squid/access.log",
		"1691000001.000 12 127.0.0.1 TCP_TUNNEL/200 5000 CONNECT pypi.org:443 - HIER_DIRECT/1.2.3.4 -\n")

	h.mustRun("logs", "--denied")
	h.assertOutputContains("no matching lines")
}

func TestAMissingLogExplainsItself(t *testing.T) {
	h := newHarness(t)
	h.fake.AddVM(config.ProxyVM, lima.StatusRunning)
	err := h.run("logs")
	if err == nil || !strings.Contains(err.Error(), "no proxy log") {
		t.Errorf("err = %v", err)
	}
}

func TestAStoppedProxyVMIsTheDiagnosisNotACrash(t *testing.T) {
	h := withLog(t)
	h.fake.SetStatus(config.ProxyVM, "Stopped")
	err := h.run("logs")
	if err == nil || !strings.Contains(err.Error(), "proxy VM is not running") {
		t.Errorf("err = %v", err)
	}
}

func TestLogsRejectsAnUnknownOption(t *testing.T) {
	h := withLog(t)
	err := h.run("logs", "--tail-everything")
	if err == nil || !strings.Contains(err.Error(), "unknown option") {
		t.Errorf("err = %v", err)
	}
}

func TestLogsValidatesTheLineCount(t *testing.T) {
	h := withLog(t)
	for _, bad := range []string{"lots", "-5"} {
		if err := h.run("logs", "-n", bad); err == nil {
			t.Errorf("logs -n %q was accepted", bad)
		}
	}
	if err := h.run("logs", "-n"); err == nil {
		t.Error("logs -n with no argument was accepted")
	}
}

// The log path is fixed: it is a file inside a VM this project builds, so
// pointing ptrbox at another one was never a setting anyone needed.
func TestLogsReadsTheFixedInVMPath(t *testing.T) {
	h := newHarness(t)
	h.fake.AddVM(config.ProxyVM, lima.StatusRunning)
	h.fake.WriteFile(config.ProxyVM, config.SquidLog, "a line TCP_DENIED\n")

	h.mustRun("logs")
	if !strings.Contains(h.stdout, "a line") {
		t.Errorf("stdout = %q", h.stdout)
	}
}

func TestFollowStreamsAndFiltersLineByLine(t *testing.T) {
	// The fake does not block, so -f just emits the tail once - enough to
	// prove the filtering happens on the stream rather than after it.
	h := withLog(t)
	h.mustRun("logs", "--denied", "-f")
	h.assertCalled("sudo tail -n 50 -f /var/log/squid/access.log")
	if strings.Contains(h.stdout, "TCP_TUNNEL") {
		t.Errorf("the follow filter let allowed requests through:\n%s", h.stdout)
	}
	if !strings.Contains(h.stdout, "telemetry.example:443") {
		t.Error("the follow filter dropped a denial")
	}
}

func TestLogsHelpPrintsUsage(t *testing.T) {
	h := withLog(t)
	h.mustRun("logs", "--help")
	if !strings.Contains(h.stdout, "--denied") {
		t.Errorf("stdout = %q", h.stdout)
	}
	h.assertNotCalled("tail")
}
