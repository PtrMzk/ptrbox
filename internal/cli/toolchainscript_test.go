package cli

// vm/provision/30-toolchain.sh and vm/verify.sh's reading of what it records,
// run for real - the same treatment 15-extra-packages.sh gets next door, and
// for the same reason: a guest assertion nothing executes is a comment.
//
// What is new here is that the script now branches. PTRBOX_TOOLCHAIN decides
// which runtimes are installed, so the thing worth executing is that asking
// for one runtime does not quietly install the other, that Claude Code is
// installed either way, and that a runtime which was requested but did not
// appear is a failed check rather than a VM that looks ready.
//
// The stubs stand in for three `curl | bash` installers. Each one produces the
// artifact the real installer produces - nvm's nvm.sh, a uv binary, a claude
// binary - because verify.sh's question is "is it on PATH", and a stub that
// skipped that step would let the check pass on nothing.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/render"
)

// toolchainScript renders 30-toolchain.sh the way `ptrbox new` renders it and
// puts a stub world around it: a fake curl serving each installer, a fake nvm,
// and a HOME of its own for the markers both scripts use.
func toolchainScript(t *testing.T, toolchain, nodeVersion string) (dir string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	dir = t.TempDir()
	stubs := filepath.Join(dir, "stubs")
	if err := os.MkdirAll(stubs, 0o755); err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	err := render.Render(&buf, ptrbox.Assets, "vm/provision/30-toolchain.sh", "vm",
		render.Values{"TOOLCHAIN": toolchain, "NODE_VERSION": nodeVersion})
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(dir, "toolchain.sh"), buf.String())
	writeScript(t, filepath.Join(dir, "verify.sh"), asset(t, "vm/verify.sh"))

	// curl: log the URL, then emit the installer that URL really serves. Each
	// one leaves behind what the real thing leaves behind, so "is it on PATH"
	// stays a question with a real answer.
	writeScript(t, filepath.Join(stubs, "curl"), `#!/bin/bash
printf '%s\n' "$*" >>"$CURL_LOG"
case "$*" in
*nvm-sh/nvm*)
  # The nvm installer writes nvm.sh, which the script then sources. The
  # function it defines records its arguments and produces a node on PATH,
  # which is what a real nvm install amounts to for this purpose.
  cat <<'INSTALLER'
mkdir -p "$HOME/.nvm"
cat >"$HOME/.nvm/nvm.sh" <<'NVMSH'
nvm() {
  printf '%s\n' "$*" >>"$NVM_LOG"
  if [ "$1" = install ]; then
    printf '#!/bin/sh\necho node\n' >"$STUBS/node"
    chmod 755 "$STUBS/node"
  fi
}
NVMSH
INSTALLER
  ;;
*astral.sh*)
  printf 'printf "#!/bin/sh\\necho uv\\n" >"$STUBS/uv"; chmod 755 "$STUBS/uv"\n'
  ;;
*claude.ai*)
  printf 'printf "#!/bin/sh\\necho claude\\n" >"$STUBS/claude"; chmod 755 "$STUBS/claude"\n'
  ;;
esac
exit 0
`)
	// verify.sh's other checks are not what these cases are about, and a test
	// host is not a sandbox: without stubs the egress probes would spend
	// seconds discovering they have no proxy. curl is already stubbed above,
	// and answers those probes with success - harmless here, since the only
	// line these tests read is the toolchain one.
	writeScript(t, filepath.Join(stubs, "sudo"), "#!/bin/sh\nexit 1\n")
	writeScript(t, filepath.Join(stubs, "systemctl"), "#!/bin/sh\nexit 1\n")
	writeScript(t, filepath.Join(stubs, "mount"), "#!/bin/sh\nexit 0\n")

	t.Setenv("HOME", dir)
	t.Setenv("STUBS", stubs)
	// A PATH of the stubs plus the system directories, NOT the developer's.
	// These tests ask whether a runtime is on PATH, and the machine running
	// them may well have node and uv installed for real - in ~/.nvm and
	// ~/.local/bin, which is exactly where a leak would come from. With the
	// real PATH appended, "verify.sh fails when node is missing" passes on a
	// bare CI box and fails on the laptop of anyone who has node.
	t.Setenv("PATH", strings.Join([]string{stubs, "/usr/bin", "/bin"},
		string(os.PathListSeparator)))
	t.Setenv("CURL_LOG", filepath.Join(dir, "curl.log"))
	t.Setenv("NVM_LOG", filepath.Join(dir, "nvm.log"))
	return dir
}

// installToolchain runs the rendered 30-toolchain.sh.
func installToolchain(t *testing.T, dir string) (string, bool) {
	t.Helper()
	out, err := exec.Command("bash", filepath.Join(dir, "toolchain.sh")).CombinedOutput()
	return string(out), err == nil
}

func logOf(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(body)
}

func TestTheDefaultToolchainInstallsNodeAndUvAndClaude(t *testing.T) {
	dir := toolchainScript(t, "node uv", "lts")

	out, ok := installToolchain(t, dir)
	if !ok {
		t.Fatalf("provisioning failed:\n%s", out)
	}
	curl := logOf(t, dir, "curl.log")
	for _, want := range []string{"nvm-sh/nvm", "astral.sh", "claude.ai"} {
		if !strings.Contains(curl, want) {
			t.Errorf("%s was never fetched:\n%s", want, curl)
		}
	}
	if nvm := logOf(t, dir, "nvm.log"); !strings.Contains(nvm, "install --lts") {
		t.Errorf("nvm log = %q, want the LTS install", nvm)
	}
	if line := verifyLine(t, dir, dir, "toolchain"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
}

// The point of the feature: a sandbox that wants neither runtime gets neither,
// and still gets Claude Code.
func TestAnEmptyToolchainInstallsOnlyClaudeCode(t *testing.T) {
	dir := toolchainScript(t, "", "lts")

	out, ok := installToolchain(t, dir)
	if !ok {
		t.Fatalf("provisioning failed:\n%s", out)
	}
	curl := logOf(t, dir, "curl.log")
	if !strings.Contains(curl, "claude.ai") {
		t.Errorf("Claude Code was not installed:\n%s", curl)
	}
	for _, unwanted := range []string{"nvm-sh/nvm", "astral.sh"} {
		if strings.Contains(curl, unwanted) {
			t.Errorf("%s was fetched for an empty toolchain:\n%s", unwanted, curl)
		}
	}
	// And verify.sh is satisfied by a VM with no runtimes, rather than
	// insisting on the ones it used to hardcode.
	if line := verifyLine(t, dir, dir, "toolchain"); !strings.Contains(line, "OK") {
		t.Errorf("verify.sh = %q, want OK", line)
	}
}

func TestEachRuntimeCanBeAskedForAlone(t *testing.T) {
	for _, tc := range []struct{ toolchain, fetched, skipped string }{
		{"node", "nvm-sh/nvm", "astral.sh"},
		{"uv", "astral.sh", "nvm-sh/nvm"},
	} {
		t.Run(tc.toolchain, func(t *testing.T) {
			dir := toolchainScript(t, tc.toolchain, "lts")
			if out, ok := installToolchain(t, dir); !ok {
				t.Fatalf("provisioning failed:\n%s", out)
			}
			curl := logOf(t, dir, "curl.log")
			if !strings.Contains(curl, tc.fetched) {
				t.Errorf("%s was not fetched:\n%s", tc.fetched, curl)
			}
			if strings.Contains(curl, tc.skipped) {
				t.Errorf("%s was fetched anyway:\n%s", tc.skipped, curl)
			}
			if line := verifyLine(t, dir, dir, "toolchain"); !strings.Contains(line, "OK") {
				t.Errorf("verify.sh = %q, want OK", line)
			}
		})
	}
}

// A pinned version reaches nvm as itself. This is the value that is
// interpolated into a shell command inside the guest, so what it becomes there
// is worth executing rather than eyeballing.
func TestAPinnedNodeVersionIsWhatNvmIsGiven(t *testing.T) {
	dir := toolchainScript(t, "node", "22.11.0")
	if out, ok := installToolchain(t, dir); !ok {
		t.Fatalf("provisioning failed:\n%s", out)
	}
	nvm := logOf(t, dir, "nvm.log")
	if !strings.Contains(nvm, "install 22.11.0") {
		t.Errorf("nvm log = %q, want the pinned version", nvm)
	}
	if strings.Contains(nvm, "--lts") {
		t.Errorf("nvm log = %q, want no LTS install alongside the pin", nvm)
	}
}

// The record is written before anything is installed, so a runtime that was
// requested and did not arrive is a failed check rather than a silent absence.
// This is the case that used to be impossible to have: the old check hardcoded
// node and uv, so "requested but missing" and "not requested" looked the same.
func TestARequestedRuntimeThatDidNotArriveFailsVerification(t *testing.T) {
	dir := toolchainScript(t, "node uv", "lts")
	if out, ok := installToolchain(t, dir); !ok {
		t.Fatalf("provisioning failed:\n%s", out)
	}
	// Take node away, the way a failed nvm install would have.
	if err := os.Remove(filepath.Join(dir, "stubs", "node")); err != nil {
		t.Fatal(err)
	}
	line := verifyLine(t, dir, dir, "toolchain")
	if !strings.Contains(line, "FAIL") || !strings.Contains(line, "node") {
		t.Errorf("verify.sh = %q, want a FAIL naming node", line)
	}
}

// No record at all is a failure too. Without this, a 30-toolchain.sh that
// never ran would reduce the check to "claude and git" and pass.
func TestAMissingToolchainRecordFailsVerification(t *testing.T) {
	dir := toolchainScript(t, "node uv", "lts")
	if out, ok := installToolchain(t, dir); !ok {
		t.Fatalf("provisioning failed:\n%s", out)
	}
	if err := os.Remove(filepath.Join(dir, ".ptrbox", "toolchain")); err != nil {
		t.Fatal(err)
	}
	if line := verifyLine(t, dir, dir, "toolchain"); !strings.Contains(line, "FAIL") {
		t.Errorf("verify.sh = %q, want a FAIL for the missing record", line)
	}
}

// The done marker still guards the whole script: Lima re-runs provisioning on
// every boot, and by then the firewall is up and nodejs.org is unreachable.
func TestASecondRunInstallsNothing(t *testing.T) {
	dir := toolchainScript(t, "node uv", "lts")
	if out, ok := installToolchain(t, dir); !ok {
		t.Fatalf("provisioning failed:\n%s", out)
	}
	if err := os.Remove(filepath.Join(dir, "curl.log")); err != nil {
		t.Fatal(err)
	}
	if out, ok := installToolchain(t, dir); !ok {
		t.Fatalf("the second run failed:\n%s", out)
	}
	if curl := logOf(t, dir, "curl.log"); curl != "" {
		t.Errorf("the second run fetched things:\n%s", curl)
	}
}
