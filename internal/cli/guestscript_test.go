package cli

// vm/provision/15-extra-packages.sh and vm/verify.sh's reading of what it
// leaves behind, run for real.
//
// Everywhere else the guest is a fake, which proves ptrbox renders the script
// and pipes verify.sh in - not that either one asks the right question. The
// question here is new: whether the packages a user named actually exist on
// this distro. The host can only check the shape of a name, so the assertion
// lives in the guest, and a guest assertion nothing executes is a comment.
//
// So: a real bash, a stub apt-get and dpkg-query on PATH, and the state
// directory both scripts take as an argument pointed at a temp dir. The stubs
// answer the way apt does - resolution and installation are separate verbs
// that fail separately - which is enough to exercise the parts that matter:
// that nothing is installed when a name does not resolve, that every way of
// failing leaves a marker naming the package, and that verify.sh turns that
// marker into a red line.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/render"
)

// aptStub is what the fake apt-get knows about the world.
type aptStub struct {
	known      []string // names `apt-get install --simulate` resolves
	installFor string   // a name whose real install fails; "" for none
	absent     string   // a name dpkg-query reports as not installed
}

// guestScripts renders 15-extra-packages.sh with the given package list and
// writes both it and verify.sh, plus the stubs, into a temp dir.
func guestScripts(t *testing.T, packages string, apt aptStub) (dir, state string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	dir = t.TempDir()
	state = filepath.Join(dir, "state")

	// Rendered exactly the way `ptrbox new` renders it: the list is a literal
	// fixed on the host, and the test would prove nothing about the real
	// script if it substituted the placeholder some other way.
	var buf strings.Builder
	err := render.Render(&buf, ptrbox.Assets, "vm/provision/15-extra-packages.sh", "vm",
		render.Values{"EXTRA_PACKAGES": packages})
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(dir, "extra-packages.sh"), buf.String())
	writeScript(t, filepath.Join(dir, "verify.sh"), asset(t, "vm/verify.sh"))

	// apt-get: `install --simulate <pkg>` resolves only known names; a real
	// `install` fails only for installFor. Nothing here touches the network,
	// which is the property the reboot depends on.
	stubs := filepath.Join(dir, "stubs")
	if err := os.MkdirAll(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(stubs, "apt-get"), `#!/bin/bash
# Log every invocation so a test can see what was, and was not, attempted.
printf '%s\n' "$*" >>"$APT_LOG"
simulate=""
pkgs=()
for arg in "$@"; do
  case "$arg" in
    --simulate|-s) simulate=1 ;;
    -*|install) ;;
    *) pkgs+=("$arg") ;;
  esac
done
for pkg in "${pkgs[@]}"; do
  if [ -n "$simulate" ]; then
    case " $APT_KNOWN " in *" $pkg "*) ;; *) echo "E: Unable to locate package $pkg" >&2; exit 100 ;; esac
  elif [ -n "$APT_INSTALL_FAILS" ] && [ "$pkg" = "$APT_INSTALL_FAILS" ]; then
    echo "E: install failed for $pkg" >&2
    exit 100
  fi
done
exit 0
`)
	writeScript(t, filepath.Join(stubs, "dpkg-query"), `#!/bin/bash
# Only the -W -f='${db:Status-Status}' <name> form ptrbox uses.
name="${!#}"
if [ "$name" = "$APT_ABSENT" ]; then
  echo "dpkg-query: no packages found matching $name" >&2
  exit 1
fi
printf 'installed'
`)
	// verify.sh's other checks are not what these cases are about, and a test
	// host is not a sandbox: without stubs the curl probes would spend 35
	// seconds discovering they have no proxy.
	for _, name := range []string{"curl", "sudo", "systemctl"} {
		writeScript(t, filepath.Join(stubs, name), "#!/bin/sh\nexit 1\n")
	}
	writeScript(t, filepath.Join(stubs, "mount"), "#!/bin/sh\nexit 0\n")

	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("APT_LOG", filepath.Join(dir, "apt.log"))
	t.Setenv("APT_KNOWN", strings.Join(apt.known, " "))
	t.Setenv("APT_INSTALL_FAILS", apt.installFor)
	t.Setenv("APT_ABSENT", apt.absent)
	return dir, state
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func asset(t *testing.T, name string) string {
	t.Helper()
	body, err := ptrbox.Assets.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// provision runs the rendered 15-extra-packages.sh against the temp state dir.
func provision(t *testing.T, dir, state string) (string, bool) {
	t.Helper()
	out, err := exec.Command("bash", filepath.Join(dir, "extra-packages.sh"), state).CombinedOutput()
	return string(out), err == nil
}

// verifyLine returns vm/verify.sh's verdict line for the named check.
func verifyLine(t *testing.T, dir, state, check string) string {
	t.Helper()
	// The exit status is deliberately ignored: on a test host the egress and
	// privilege checks fail, and the line under test is printed regardless.
	out, _ := exec.Command("bash", filepath.Join(dir, "verify.sh"), state).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, check) {
			return line
		}
	}
	t.Fatalf("verify.sh printed no %q line:\n%s", check, out)
	return ""
}

func aptLog(t *testing.T, dir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "apt.log"))
	if err != nil {
		return "" // apt-get was never called
	}
	return string(body)
}

func TestPackagesThatExistAreInstalledAndVerified(t *testing.T) {
	dir, state := guestScripts(t, "ripgrep sqlite3", aptStub{known: []string{"ripgrep", "sqlite3"}})

	out, ok := provision(t, dir, state)
	if !ok {
		t.Fatalf("provisioning failed for packages that exist:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(state, "extra-packages.done")); err != nil {
		t.Errorf("no done marker after a clean run: %v", err)
	}
	if !strings.Contains(aptLog(t, dir), "install -y ripgrep sqlite3") {
		t.Errorf("the packages were never really installed:\n%s", aptLog(t, dir))
	}
	if line := verifyLine(t, dir, state, "extra packages"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
}

func TestATypoedPackageFailsVerificationAndNamesItself(t *testing.T) {
	// The whole point of the item: `apt-get install -y ripgred` fails on boot
	// 1 inside cloud-init, where nothing is watching. The marker is what
	// carries the news out.
	dir, state := guestScripts(t, "ripgrep ripgred", aptStub{known: []string{"ripgrep"}})

	out, ok := provision(t, dir, state)
	if ok {
		t.Fatalf("provisioning passed with a package that does not exist:\n%s", out)
	}
	line := verifyLine(t, dir, state, "extra packages")
	if !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want FAIL", line)
	}
	if !strings.Contains(line, "ripgred") {
		t.Errorf("verify.sh = %q, does not name the bad package", line)
	}
	if strings.Contains(line, "ripgrep ") {
		t.Errorf("verify.sh = %q, blames a package that was fine", line)
	}
}

func TestNothingIsInstalledWhenOneNameDoesNotResolve(t *testing.T) {
	// Resolution happens for the whole list before any of it is installed, so
	// a typo in the second package does not leave the first one installed in
	// a VM that is about to be declared broken.
	dir, state := guestScripts(t, "ripgrep ripgred", aptStub{known: []string{"ripgrep"}})
	provision(t, dir, state)

	for _, line := range strings.Split(aptLog(t, dir), "\n") {
		if line != "" && !strings.Contains(line, "--simulate") {
			t.Errorf("apt-get really installed something after a failed resolve: %q", line)
		}
	}
}

func TestAFailedInstallOfARealPackageFailsVerification(t *testing.T) {
	// Resolvable is not installed: the name is right and apt still fails.
	dir, state := guestScripts(t, "ripgrep sqlite3",
		aptStub{known: []string{"ripgrep", "sqlite3"}, installFor: "sqlite3"})

	if _, ok := provision(t, dir, state); ok {
		t.Fatal("provisioning passed with an install that failed")
	}
	if line := verifyLine(t, dir, state, "extra packages"); !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want FAIL", line)
	}
}

func TestAPackageMissingAfterAHappyAptFailsVerification(t *testing.T) {
	// apt-get exits zero and the package is not there anyway. dpkg is the one
	// that answers the question the user actually asked.
	dir, state := guestScripts(t, "ripgrep",
		aptStub{known: []string{"ripgrep"}, absent: "ripgrep"})

	if _, ok := provision(t, dir, state); ok {
		t.Fatal("provisioning passed with a package apt claimed to install and did not")
	}
	line := verifyLine(t, dir, state, "extra packages")
	if !strings.Contains(line, "FAIL") || !strings.Contains(line, "ripgrep") {
		t.Errorf("verify.sh = %q, want a FAIL naming ripgrep", line)
	}
}

func TestAVersionPinIsCheckedAgainstTheNameDpkgKnows(t *testing.T) {
	// The host accepts `pkg=1:2.0~rc1-3`; dpkg-query is asked about the part
	// before the `=`, or it would report every pinned package as missing.
	dir, state := guestScripts(t, "sqlite3=3.46.1-1",
		aptStub{known: []string{"sqlite3=3.46.1-1"}, absent: "sqlite3=3.46.1-1"})

	if out, ok := provision(t, dir, state); !ok {
		t.Fatalf("a pinned package was reported missing:\n%s", out)
	}
}

func TestNoExtraPackagesTouchesAptAtAll(t *testing.T) {
	dir, state := guestScripts(t, "", aptStub{})

	if out, ok := provision(t, dir, state); !ok {
		t.Fatalf("provisioning failed with no packages configured:\n%s", out)
	}
	if log := aptLog(t, dir); log != "" {
		t.Errorf("apt-get ran with an empty package list: %q", log)
	}
	if line := verifyLine(t, dir, state, "extra packages"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
}

func TestASecondBootReinstallsNothing(t *testing.T) {
	// Invariant 6: the script runs on every boot, and after a good one it must
	// exit in milliseconds without asking apt for anything.
	dir, state := guestScripts(t, "ripgrep", aptStub{known: []string{"ripgrep"}})
	if _, ok := provision(t, dir, state); !ok {
		t.Fatal("the first boot failed")
	}
	if err := os.Remove(filepath.Join(dir, "apt.log")); err != nil {
		t.Fatal(err)
	}
	if _, ok := provision(t, dir, state); !ok {
		t.Fatal("the second boot failed")
	}
	if log := aptLog(t, dir); log != "" {
		t.Errorf("the second boot went back to apt: %q", log)
	}
}

func TestTheRebootRechecksAFailureWithoutTheNetwork(t *testing.T) {
	// A failed boot 1 leaves no done marker, so the script runs again on the
	// reboot that raises the firewall. It must reach the same verdict from the
	// lists on disk - the marker verify.sh reads is the one this run wrote.
	dir, state := guestScripts(t, "ripgred", aptStub{})
	provision(t, dir, state)
	if err := os.Remove(filepath.Join(dir, "apt.log")); err != nil {
		t.Fatal(err)
	}

	if _, ok := provision(t, dir, state); ok {
		t.Fatal("the reboot passed a VM the first boot failed")
	}
	if strings.Contains(aptLog(t, dir), "update") {
		t.Errorf("the re-check needs the network:\n%s", aptLog(t, dir))
	}
	if line := verifyLine(t, dir, state, "extra packages"); !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want FAIL", line)
	}
}

func TestAFixedListClearsAnOlderFailure(t *testing.T) {
	// The marker describes this boot, not a previous one. (Re-creating the VM
	// is the supported way to change the list; this is about the same list
	// resolving on the reboot after a mirror hiccup.)
	dir, state := guestScripts(t, "ripgrep", aptStub{})
	if _, ok := provision(t, dir, state); ok {
		t.Fatal("provisioning passed with nothing resolvable")
	}
	t.Setenv("APT_KNOWN", "ripgrep")

	if out, ok := provision(t, dir, state); !ok {
		t.Fatalf("the second boot failed once the package resolved:\n%s", out)
	}
	if line := verifyLine(t, dir, state, "extra packages"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK - the stale marker was not cleared", line)
	}
}
