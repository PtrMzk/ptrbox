package cli

// ptrbox allow - managing the egress allowlist.
//
// The allowlist lives on the host and is pushed into the proxy VM, where squid
// validates it. The interesting cases are the ones where bad input or a broken
// edit could take the proxy down - and, since the proxy became a VM, the ones
// where it is not running.

import (
	"os"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
)

// installed returns a harness with the host set up and the call log cleared.
func installed(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.mustRun("install")
	h.fake.Reset()
	return h
}

func (h *harness) vmAllowlist() string { return h.proxyFile("/etc/squid/allowed_domains.txt") }

// --- appending ---------------------------------------------------------------

func TestAllowAppendsADomainAndPushesItToTheProxyVM(t *testing.T) {
	h := installed(t)
	h.mustRun("allow", "files.example.com")
	if !containsLine(h.allowlist(), "files.example.com") {
		t.Error("the domain is not in the host allowlist")
	}
	if !containsLine(h.vmAllowlist(), "files.example.com") {
		t.Error("the domain did not reach the proxy VM")
	}
}

func TestAllowAcceptsSeveralDomainsAtOnce(t *testing.T) {
	h := installed(t)
	h.mustRun("allow", "one.example.com", "two.example.com")
	for _, domain := range []string{"one.example.com", "two.example.com"} {
		if !containsLine(h.allowlist(), domain) {
			t.Errorf("%s is missing", domain)
		}
	}
}

func TestAllowAcceptsALeadingDotForSubdomains(t *testing.T) {
	h := installed(t)
	h.mustRun("allow", ".cdn.example.com")
	if !containsLine(h.allowlist(), ".cdn.example.com") {
		t.Error("the wildcard entry is missing")
	}
}

func TestAddingADomainTwiceDoesNotDuplicateIt(t *testing.T) {
	h := installed(t)
	h.mustRun("allow", "files.example.com")
	h.mustRun("allow", "files.example.com")
	h.assertOutputContains("already allowed")

	count := 0
	for _, line := range strings.Split(h.allowlist(), "\n") {
		if line == "files.example.com" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the domain appears %d times", count)
	}
}

func TestADomainAlreadyShippedInTheAllowlistIsRecognised(t *testing.T) {
	h := installed(t)
	h.mustRun("allow", "pypi.org")
	h.assertOutputContains("already allowed")
	h.assertNotCalled("squid -k reconfigure")
}

// --- reloading ---------------------------------------------------------------

func TestAChangeReloadsSquidWithoutRestartingIt(t *testing.T) {
	h := installed(t)
	h.mustRun("allow", "files.example.com")
	// A restart severs every live VM tunnel, including Claude's request.
	h.assertCalled("sudo squid -k reconfigure")
	h.assertNotCalled("systemctl restart")
}

func TestTheNewAllowlistIsValidatedBeforeItIsKept(t *testing.T) {
	h := installed(t)
	h.mustRun("allow", "files.example.com")
	h.assertOrder("sudo squid -k parse", "sudo squid -k reconfigure")
}

func TestAnAllowlistSquidRejectsIsRolledBackOnHostAndInTheVM(t *testing.T) {
	h := installed(t)
	h.fake.SquidParseFails = true

	if err := h.run("allow", "files.example.com"); err == nil {
		t.Fatal("allow succeeded despite a rejected config")
	}
	h.assertOutputContains("restored the previous one")
	// The live file is the old one. Whole-line match: the shipped allowlist
	// mentions example domains in its comments.
	if containsLine(h.allowlist(), "files.example.com") {
		t.Error("the host allowlist was not restored")
	}
	// The proxy VM was restored too - a later squid restart there must not
	// trip over a file we knew was bad.
	if containsLine(h.vmAllowlist(), "files.example.com") {
		t.Error("the VM allowlist was not restored")
	}
	// ...and the rejected version is kept rather than thrown away.
	if !containsLine(readFile(t, config.AllowlistPath()+".rejected"), "files.example.com") {
		t.Error("the rejected version was not kept")
	}
	h.assertNotCalled("squid -k reconfigure")
}

// --- the proxy VM is down ----------------------------------------------------

func TestWithTheProxyStoppedTheEditIsSavedAndDeferred(t *testing.T) {
	h := installed(t)
	h.fake.SetStatus(config.ProxyVM, "Stopped")
	h.fake.Reset()

	h.mustRun("allow", "files.example.com")
	h.assertOutputContains("applied when it next starts")
	if !containsLine(h.allowlist(), "files.example.com") {
		t.Error("the edit was not saved host-side")
	}
	// Nothing was pushed at a VM that cannot answer.
	h.assertNotCalled("sudo tee")
	h.assertNotCalled("squid -k")
}

// --- validation --------------------------------------------------------------

func TestBadDomainsAreRefused(t *testing.T) {
	for _, tc := range []struct{ name, domain, want string }{
		{"shell or squid metacharacters", "evil.com all", "is not a domain"},
		{"a URL", "https://example.com/path", "is not a domain"},
		{"a bare name with no dot", "localhost", "no dot"},
		{"a semicolon", "bad;domain", "is not a domain"},
		// A leading hyphen never reaches the domain check: the argument parser
		// claims it first, which is the right answer for a different reason.
		{"a leading hyphen", "-lead.example.com", "unknown option"},
		{"a trailing dot", "example.com.", "ends with a dot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := installed(t)
			before := h.allowlist()

			err := h.run("allow", tc.domain)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if h.allowlist() != before {
				t.Error("a rejected domain reached the file")
			}
			h.assertNotCalled("squid -k reconfigure")
		})
	}
}

func TestOneBadDomainRejectsTheWholeBatch(t *testing.T) {
	// Validation happens before anything is written, so a good domain listed
	// ahead of a bad one does not land either.
	h := installed(t)
	before := h.allowlist()

	if err := h.run("allow", "good.example.com", "bad;domain"); err == nil {
		t.Fatal("allow accepted a batch with an invalid domain")
	}
	if h.allowlist() != before {
		t.Error("part of a rejected batch was written")
	}
}

// --- editor mode -------------------------------------------------------------

func TestWithNoArgumentsItOpensTheEditorAndReloads(t *testing.T) {
	h := installed(t)
	h.editor = func(path string) error {
		return appendToFile(path, "edited.example.com\n")
	}

	h.mustRun("allow")
	if !containsLine(h.allowlist(), "edited.example.com") {
		t.Error("the edit was not saved")
	}
	if !containsLine(h.vmAllowlist(), "edited.example.com") {
		t.Error("the edit did not reach the proxy VM")
	}
	h.assertCalled("sudo squid -k reconfigure")
}

func TestAnEditorSessionThatChangesNothingDoesNotReloadSquid(t *testing.T) {
	h := installed(t)
	h.editor = func(string) error { return nil }
	h.mustRun("allow")
	h.assertOutputContains("unchanged")
	h.assertNotCalled("squid -k reconfigure")
}

// --- listing -----------------------------------------------------------------

func TestListPrintsDomainsWithoutComments(t *testing.T) {
	h := installed(t)
	h.mustRun("allow", "--list")
	if !strings.Contains(h.stdout, "api.anthropic.com") {
		t.Error("the list is missing the Claude API entry")
	}
	if strings.Contains(h.stdout, "#") {
		t.Errorf("the list contains comments:\n%s", h.stdout)
	}
	h.assertNotCalled("squid")
}

// --- preconditions -----------------------------------------------------------

func TestAMissingAllowlistPointsAtInstall(t *testing.T) {
	h := installed(t)
	if err := os.Remove(config.AllowlistPath()); err != nil {
		t.Fatal(err)
	}
	err := h.run("allow", "files.example.com")
	if err == nil || !strings.Contains(err.Error(), "ptrbox install") {
		t.Errorf("err = %v", err)
	}
}

func TestAllowRejectsAnUnknownOption(t *testing.T) {
	h := installed(t)
	err := h.run("allow", "--deny-everything")
	if err == nil || !strings.Contains(err.Error(), "unknown option") {
		t.Errorf("err = %v", err)
	}
}

func TestAllowHelpPrintsUsage(t *testing.T) {
	h := installed(t)
	h.mustRun("allow", "--help")
	if !strings.Contains(h.stdout, "ptrbox allow <domain>...") {
		t.Errorf("stdout = %q", h.stdout)
	}
}

func appendToFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}
