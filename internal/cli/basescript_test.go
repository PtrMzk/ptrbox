package cli

// vm/provision/10-base.sh's Playwright branch and vm/verify.sh's reading of
// what it records, run for real - the third guest script the suite executes
// rather than lints.
//
// The question worth executing: PTRBOX_PLAYWRIGHT decides whether ~20 GTK,
// X11 and font packages land in a sandbox, and that decision is invisible
// from the host. A rendered `if` that installed them anyway, or a record
// verify.sh could not read back, would both look exactly like this working.
//
// The stubs are the ones next door in guestscript_test.go: a fake apt-get
// that logs every invocation, and a dpkg-query that answers for what is
// installed. Nothing here touches the network.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/render"
)

// baseScript renders 10-base.sh with the given flag and puts a stub world
// around it. absent, when set, is a package dpkg-query reports as missing -
// the "apt said yes but it is not there" case.
func baseScript(t *testing.T, playwright bool, absent string) (dir, state string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	dir = t.TempDir()
	state = filepath.Join(dir, "state")

	// Rendered the way `ptrbox new` renders it, from the same literal the
	// host resolves: substituting the placeholder some other way would prove
	// nothing about the real script.
	var buf strings.Builder
	value := "false"
	if playwright {
		value = "true"
	}
	err := render.Render(&buf, ptrbox.Assets, "vm/provision/10-base.sh", "vm",
		render.Values{"PLAYWRIGHT": value})
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(dir, "base.sh"), buf.String())
	writeScript(t, filepath.Join(dir, "verify.sh"), asset(t, "vm/verify.sh"))

	stubs := filepath.Join(dir, "stubs")
	if err := os.MkdirAll(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(stubs, "apt-get"), `#!/bin/bash
printf '%s\n' "$*" >>"$APT_LOG"
exit 0
`)
	writeScript(t, filepath.Join(stubs, "dpkg-query"), `#!/bin/bash
name="${!#}"
if [ "$name" = "$APT_ABSENT" ]; then
  echo "dpkg-query: no packages found matching $name" >&2
  exit 1
fi
printf 'install ok installed'
`)
	// verify.sh's other checks are not what these cases are about, and a test
	// host is not a sandbox: without stubs the egress probes would spend
	// half a minute discovering they have no proxy.
	for _, name := range []string{"curl", "sudo", "systemctl", "ss"} {
		writeScript(t, filepath.Join(stubs, name), "#!/bin/sh\nexit 1\n")
	}
	writeScript(t, filepath.Join(stubs, "mount"), "#!/bin/sh\nexit 0\n")

	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("APT_LOG", filepath.Join(dir, "apt.log"))
	t.Setenv("APT_ABSENT", absent)
	return dir, state
}

// verifyLineIfAny is verifyLine for the case where the line is expected to be
// ABSENT. The shared helper fatals when a check does not appear, which is what
// its callers want; here the missing line is the assertion, because a VM
// without Playwright has nothing to report on and verify.sh stays quiet about
// features that were never asked for.
func verifyLineIfAny(t *testing.T, dir, state, check string) string {
	t.Helper()
	out, _ := exec.Command("bash", filepath.Join(dir, "verify.sh"), state).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, check) {
			return line
		}
	}
	return ""
}

// provisionBase runs the rendered 10-base.sh against the temp state dir.
func provisionBase(t *testing.T, dir, state string) (string, bool) {
	t.Helper()
	out, err := exec.Command("bash", filepath.Join(dir, "base.sh"), state).CombinedOutput()
	return string(out), err == nil
}

// The default, and the reason the flag exists: a sandbox that will never open
// a browser does not carry a browser's dependency tree.
func TestWithoutPlaywrightNoChromiumPackagesAreInstalled(t *testing.T) {
	dir, state := baseScript(t, false, "")

	out, ok := provisionBase(t, dir, state)
	if !ok {
		t.Fatalf("provisioning failed:\n%s", out)
	}
	apt := logOf(t, dir, "apt.log")
	// The base set still lands - this gates the browser libraries, nothing else.
	if !strings.Contains(apt, "curl git build-essential") {
		t.Errorf("the base packages were not installed:\n%s", apt)
	}
	for _, unwanted := range []string{"libgtk-3-0t64", "libnss3", "fonts-liberation"} {
		if strings.Contains(apt, unwanted) {
			t.Errorf("%s was installed in a VM that did not ask for Playwright:\n%s", unwanted, apt)
		}
	}
	// No record, so verify.sh has nothing to assert and says nothing.
	if _, err := os.Stat(filepath.Join(state, "playwright-packages")); err == nil {
		t.Error("a record was written for a VM with Playwright off")
	}
	if line := verifyLineIfAny(t, dir, state, "playwright"); line != "" {
		t.Errorf("verify.sh reported on Playwright in a VM without it: %q", line)
	}
}

// With the flag on, the packages are installed AND the record verify.sh reads
// is written - the two halves that make this an assertion rather than a hope.
func TestPlaywrightInstallsTheChromiumPackagesAndRecordsThem(t *testing.T) {
	dir, state := baseScript(t, true, "")

	out, ok := provisionBase(t, dir, state)
	if !ok {
		t.Fatalf("provisioning failed:\n%s", out)
	}
	apt := logOf(t, dir, "apt.log")
	for _, want := range []string{"libgtk-3-0t64", "libnss3", "fonts-liberation"} {
		if !strings.Contains(apt, want) {
			t.Errorf("%s was not installed for a Playwright VM:\n%s", want, apt)
		}
	}
	// One package per line, which is what verify.sh reads back.
	body, err := os.ReadFile(filepath.Join(state, "playwright-packages"))
	if err != nil {
		t.Fatalf("no record of the requested packages: %v", err)
	}
	lines := strings.Fields(string(body))
	if len(lines) != 18 {
		t.Errorf("the record holds %d packages, want the 18 in the script:\n%s", len(lines), body)
	}
	if strings.Contains(string(body), " ") && !strings.Contains(string(body), "\n") {
		t.Errorf("the record is not one package per line:\n%s", body)
	}
	if line := verifyLine(t, dir, state, "playwright"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
}

// Requested, apt exited zero, and the package still is not there. The record
// is written before the install precisely so this is a red line rather than a
// VM that looks ready and fails the first time something opens a browser.
func TestAPlaywrightPackageThatDidNotArriveFailsVerification(t *testing.T) {
	dir, state := baseScript(t, true, "libgtk-3-0t64")

	if out, ok := provisionBase(t, dir, state); !ok {
		t.Fatalf("provisioning failed:\n%s", out)
	}
	line := verifyLine(t, dir, state, "playwright")
	if !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want a failure", line)
	}
	if !strings.Contains(line, "libgtk-3-0t64") {
		t.Errorf("verify.sh = %q, want it to name the missing package", line)
	}
}
