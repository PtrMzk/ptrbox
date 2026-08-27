package cli

// ptrbox allow and ptrbox sync-proxy - managing per-VM egress allowlists.
//
// Since item 38 every sandbox has its own list and the shared file is only a
// template, so the command takes the VM first. The interesting cases are the
// ones where bad input or a broken edit could take the proxy down, the ones
// where the proxy is not running - and the seeding: a first touch that did
// NOT start from the template would boot a sandbox that can reach one domain
// and nothing else, including Anthropic.

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

// withVM is installed plus a "demo" sandbox, which is what most allow cases
// operate on.
func withVM(t *testing.T) *harness {
	t.Helper()
	h := installed(t)
	h.mustRun("new", "demo")
	h.fake.Reset()
	return h
}

func (h *harness) vmList(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(config.VMAllowlistPath("demo"))
	if err != nil {
		t.Fatalf("no allowlist for demo: %v", err)
	}
	return string(body)
}

// pushedList is demo's list as the proxy VM serves it.
func (h *harness) pushedList() string { return h.proxyFile("/etc/squid/allowed.d/demo.txt") }

// --- appending ---------------------------------------------------------------

func TestAllowAppendsADomainToTheVMsListAndPushesIt(t *testing.T) {
	h := withVM(t)
	h.mustRun("allow", "demo", "files.example.com")
	if !containsLine(h.vmList(t), "files.example.com") {
		t.Error("the domain is not in the VM's host-side list")
	}
	if !containsLine(h.pushedList(), "files.example.com") {
		t.Error("the domain did not reach the proxy VM")
	}
}

func TestAllowTouchesOnlyTheNamedVMsList(t *testing.T) {
	// The point of the whole feature: a grant to one sandbox is not a grant
	// to the others.
	h := withVM(t)
	h.mustRun("new", "other")
	h.mustRun("allow", "demo", "files.example.com")

	other, err := os.ReadFile(config.VMAllowlistPath("other"))
	if err != nil {
		t.Fatal(err)
	}
	if containsLine(string(other), "files.example.com") {
		t.Error("a grant to demo leaked into other's list")
	}
	template, err := os.ReadFile(config.AllowlistPath())
	if err != nil {
		t.Fatal(err)
	}
	if containsLine(string(template), "files.example.com") {
		t.Error("a grant to demo leaked into the template")
	}
}

func TestAllowAcceptsSeveralDomainsAtOnce(t *testing.T) {
	h := withVM(t)
	h.mustRun("allow", "demo", "one.example.com", "two.example.com")
	for _, domain := range []string{"one.example.com", "two.example.com"} {
		if !containsLine(h.vmList(t), domain) {
			t.Errorf("%s is missing", domain)
		}
	}
}

func TestAllowAcceptsALeadingDotForSubdomains(t *testing.T) {
	h := withVM(t)
	h.mustRun("allow", "demo", ".cdn.example.com")
	if !containsLine(h.vmList(t), ".cdn.example.com") {
		t.Error("the wildcard entry is missing")
	}
}

func TestAddingADomainTwiceDoesNotDuplicateIt(t *testing.T) {
	h := withVM(t)
	h.mustRun("allow", "demo", "files.example.com")
	h.mustRun("allow", "demo", "files.example.com")
	h.assertOutputContains("already allowed")

	count := 0
	for _, line := range strings.Split(h.vmList(t), "\n") {
		if line == "files.example.com" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the domain appears %d times", count)
	}
}

// api.anthropic.com rather than a runtime's domain: the seeded list carries
// only the groups this VM's runtimes justify, and the default VM has none.
func TestADomainAlreadySeededFromTheTemplateIsRecognised(t *testing.T) {
	h := withVM(t)
	h.fake.Reset()
	h.mustRun("allow", "demo", "api.anthropic.com")
	h.assertOutputContains("already allowed")
	h.assertNotCalled("squid -k reconfigure")
}

// --- seeding -----------------------------------------------------------------

func TestTheFirstTouchSeedsTheListFromTheTemplate(t *testing.T) {
	// Before the VM exists: `ptrbox allow` then `ptrbox new` is the
	// declare-first workflow, and the seed is what keeps the resulting list
	// complete rather than one domain long.
	h := installed(t)
	h.mustRun("allow", "demo", "files.example.com")
	h.assertOutputContains(`no VM named "demo" yet`)

	list := h.vmList(t)
	if !containsLine(list, "files.example.com") {
		t.Error("the added domain is missing")
	}
	// Contains, not a whole-line match: the template annotates its entries
	// with trailing comments.
	if !strings.Contains(list, "api.anthropic.com") {
		t.Error("the seed did not carry the template - this VM would boot unable to reach Anthropic")
	}

	// ...and new uses that file rather than re-seeding.
	h.mustRun("new", "demo")
	if !containsLine(h.pushedList(), "files.example.com") {
		t.Error("the pre-declared grant did not reach the created VM")
	}
}

// --- reloading ---------------------------------------------------------------

func TestAChangeReloadsSquidWithoutRestartingIt(t *testing.T) {
	h := withVM(t)
	h.mustRun("allow", "demo", "files.example.com")
	// A restart severs every live VM tunnel, including Claude's request.
	h.assertCalled("sudo squid -k reconfigure")
	h.assertNotCalled("systemctl restart")
}

func TestTheNewAllowlistIsValidatedBeforeItIsKept(t *testing.T) {
	h := withVM(t)
	h.mustRun("allow", "demo", "files.example.com")
	h.assertOrder("sudo squid -k parse", "sudo squid -k reconfigure")
}

func TestAnAllowlistSquidRejectsIsRolledBackOnHostAndInTheVM(t *testing.T) {
	h := withVM(t)
	h.fake.SquidParseFails = true

	if err := h.run("allow", "demo", "files.example.com"); err == nil {
		t.Fatal("allow succeeded despite a rejected config")
	}
	h.assertOutputContains("restored the previous one")
	// The live file is the old one. Whole-line match: the seeded list
	// mentions example domains in its comments.
	if containsLine(h.vmList(t), "files.example.com") {
		t.Error("the host-side list was not restored")
	}
	// The proxy VM was restored too - a later squid restart there must not
	// trip over a file we knew was bad.
	if containsLine(h.pushedList(), "files.example.com") {
		t.Error("the VM's pushed list was not restored")
	}
	// ...and the rejected version is kept rather than thrown away.
	if !containsLine(readFile(t, config.VMAllowlistPath("demo")+".rejected"), "files.example.com") {
		t.Error("the rejected version was not kept")
	}
	h.assertNotCalled("squid -k reconfigure")
}

// --- the proxy VM is down ----------------------------------------------------

func TestWithTheProxyStoppedTheEditIsSavedAndDeferred(t *testing.T) {
	h := withVM(t)
	h.fake.SetStatus(config.ProxyVM, "Stopped")
	h.fake.Reset()

	h.mustRun("allow", "demo", "files.example.com")
	h.assertOutputContains("applied when it next starts")
	if !containsLine(h.vmList(t), "files.example.com") {
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
			h := withVM(t)
			before := h.vmList(t)

			err := h.run("allow", "demo", tc.domain)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if h.vmList(t) != before {
				t.Error("a rejected domain reached the file")
			}
			h.assertNotCalled("squid -k reconfigure")
		})
	}
}

func TestOneBadDomainRejectsTheWholeBatch(t *testing.T) {
	// Validation happens before anything is written, so a good domain listed
	// ahead of a bad one does not land either.
	h := withVM(t)
	before := h.vmList(t)

	if err := h.run("allow", "demo", "good.example.com", "bad;domain"); err == nil {
		t.Fatal("allow accepted a batch with an invalid domain")
	}
	if h.vmList(t) != before {
		t.Error("part of a rejected batch was written")
	}
}

func TestADomainInFirstPositionIsADiagnosedMistake(t *testing.T) {
	// No legal VM name contains a dot, so this cannot be a VM - say what the
	// caller almost certainly meant instead of inventing a VM called
	// "filesexamplecom".
	h := withVM(t)
	err := h.run("allow", "files.example.com")
	if err == nil || !strings.Contains(err.Error(), "the VM comes first") {
		t.Errorf("err = %v", err)
	}
}

func TestAllowWithNoArgumentsExplainsTheShape(t *testing.T) {
	h := withVM(t)
	err := h.run("allow")
	if err == nil || !strings.Contains(err.Error(), "ptrbox allow <vm>") {
		t.Errorf("err = %v", err)
	}
}

func TestAllowRefusesTheProxyVM(t *testing.T) {
	h := installed(t)
	err := h.run("allow", "ptrbox-proxy", "files.example.com")
	if err == nil || !strings.Contains(err.Error(), "no allowlist of its own") {
		t.Errorf("err = %v", err)
	}
}

// --- editor mode -------------------------------------------------------------

func TestWithNoDomainsItOpensTheVMsListAndReloads(t *testing.T) {
	h := withVM(t)
	h.editor = func(path string) error {
		if path != config.VMAllowlistPath("demo") {
			t.Errorf("the editor opened %s, want demo's list", path)
		}
		return appendToFile(path, "edited.example.com\n")
	}

	h.mustRun("allow", "demo")
	if !containsLine(h.vmList(t), "edited.example.com") {
		t.Error("the edit was not saved")
	}
	if !containsLine(h.pushedList(), "edited.example.com") {
		t.Error("the edit did not reach the proxy VM")
	}
	h.assertCalled("sudo squid -k reconfigure")
}

func TestAnEditorSessionThatChangesNothingDoesNotReloadSquid(t *testing.T) {
	h := withVM(t)
	h.fake.Reset()
	h.editor = func(string) error { return nil }
	h.mustRun("allow", "demo")
	h.assertOutputContains("unchanged")
	h.assertNotCalled("squid -k reconfigure")
}

// --- listing -----------------------------------------------------------------

func TestListPrintsTheVMsDomainsWithoutComments(t *testing.T) {
	h := withVM(t)
	h.mustRun("allow", "demo", "only-mine.example.com")
	h.mustRun("allow", "demo", "--list")
	for _, want := range []string{"api.anthropic.com", "only-mine.example.com"} {
		if !strings.Contains(h.stdout, want) {
			t.Errorf("the list is missing %s", want)
		}
	}
	if strings.Contains(h.stdout, "#") {
		t.Errorf("the list contains comments:\n%s", h.stdout)
	}
}

func TestListBeforeTheFirstTouchShowsTheTemplateAndSaysSo(t *testing.T) {
	// Reads never seed: with no file yet the truthful answer is what the VM
	// will start from.
	h := installed(t)
	h.mustRun("allow", "demo", "--list")
	h.assertOutputContains("no list yet")
	if !strings.Contains(h.stdout, "api.anthropic.com") {
		t.Error("the template entries were not shown")
	}
	if h.exists(config.VMAllowlistPath("demo")) {
		t.Error("--list created a file")
	}
}

// --- preconditions -----------------------------------------------------------

func TestAMissingTemplatePointsAtInstall(t *testing.T) {
	h := installed(t)
	if err := os.Remove(config.AllowlistPath()); err != nil {
		t.Fatal(err)
	}
	err := h.run("allow", "demo", "files.example.com")
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
	if !strings.Contains(h.stdout, "ptrbox allow <vm> <domain>...") {
		t.Errorf("stdout = %q", h.stdout)
	}
}

// --- sync-proxy --------------------------------------------------------------

func TestSyncProxyPushesAHandEdit(t *testing.T) {
	h := withVM(t)
	if err := appendToFile(config.VMAllowlistPath("demo"), "hand.example.com\n"); err != nil {
		t.Fatal(err)
	}
	h.fake.Reset()

	h.mustRun("sync-proxy")
	h.assertOutputContains("changes applied")
	if !containsLine(h.pushedList(), "hand.example.com") {
		t.Error("the hand edit did not reach the proxy VM")
	}
	h.assertCalled("sudo squid -k reconfigure")
	h.assertNotCalled("systemctl restart")
}

func TestSyncProxyWithNothingToDoSaysSo(t *testing.T) {
	h := withVM(t)
	h.fake.Reset()
	h.mustRun("sync-proxy")
	h.assertOutputContains("nothing to push")
	h.assertNotCalled("squid -k reconfigure")
}

func TestSyncProxyWithTheProxyDownDefersLikeAllowDoes(t *testing.T) {
	h := withVM(t)
	h.fake.SetStatus(config.ProxyVM, "Stopped")
	h.fake.Reset()

	h.mustRun("sync-proxy")
	h.assertOutputContains("pushed when it next starts")
	h.assertNotCalled("sudo tee")
}

func TestSyncProxyLeavesARejectedHandEditInPlace(t *testing.T) {
	// Unlike allow, this command did not author the change, so it must not
	// un-author it: the file stays for the user to fix.
	h := withVM(t)
	if err := appendToFile(config.VMAllowlistPath("demo"), "bad entry here\n"); err != nil {
		t.Fatal(err)
	}
	h.fake.SquidParseFails = true

	if err := h.run("sync-proxy"); err == nil {
		t.Fatal("sync-proxy succeeded despite a rejected config")
	}
	if !containsLine(h.vmList(t), "bad entry here") {
		t.Error("the user's hand edit was rewritten")
	}
	// The proxy VM, though, was rolled back.
	if containsLine(h.pushedList(), "bad entry here") {
		t.Error("the rejected content was left live in the VM")
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
