package proxy_test

// The runtime filter on the seeded allowlist: a sandbox starts out able to
// reach what its runtimes need and nothing more.
//
// Every line in an allowlist is a capability grant, and a grant nothing in the
// VM can use is the worst kind - it costs the same and buys nothing, so it is
// never noticed and never removed. With both runtimes off by default, the
// unfiltered template handed a plain Claude-only sandbox the npm registry,
// PyPI and two Playwright CDNs.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/proxy"
)

// seedTemplate is the shape of the shipped file, small enough to reason about:
// an ungrouped entry, one group per runtime, and a group naming two.
const seedTemplate = `# a comment
always.example.com

# @requires node
registry.npmjs.org
# @end

# @requires uv
pypi.org
# @end

# @requires node uv
cdn.playwright.dev
# @end
`

func seedFor(t *testing.T, template, vmConfig string) string {
	t.Helper()
	if vmConfig != "" {
		if err := os.MkdirAll(config.VMDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(config.VMDir(), "demo"), []byte(vmConfig), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	vm, err := cfg.Overlay("demo")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := proxy.SeedFor([]byte(template), vm)
	if err != nil {
		t.Fatalf("SeedFor: %v", err)
	}
	return string(seed)
}

// The default sandbox: no runtimes, so no runtime domains.
func TestASandboxWithNoRuntimesGetsNoRuntimeDomains(t *testing.T) {
	newHarness(t)
	seed := seedFor(t, seedTemplate, "")

	if !strings.Contains(seed, "always.example.com") {
		t.Error("an ungrouped entry was dropped")
	}
	for _, gone := range []string{"registry.npmjs.org", "pypi.org", "cdn.playwright.dev"} {
		if strings.Contains(seed, gone) {
			t.Errorf("%s was granted to a VM with no runtimes:\n%s", gone, seed)
		}
	}
	// Said out loud rather than silently missing: someone reading the file
	// has to be able to tell "left out" from "never existed".
	if !strings.Contains(seed, "# (omitted: this VM has no node)") {
		t.Errorf("the dropped node group left no note:\n%s", seed)
	}
}

// One runtime on: its group survives, the other's does not, and a group naming
// either is kept by whichever one is present.
func TestARuntimeBringsItsOwnDomains(t *testing.T) {
	for _, tc := range []struct{ vmConfig, kept, dropped string }{
		{"PTRBOX_NODE=true\n", "registry.npmjs.org", "pypi.org"},
		{"PTRBOX_UV=true\n", "pypi.org", "registry.npmjs.org"},
	} {
		t.Run(tc.kept, func(t *testing.T) {
			newHarness(t)
			seed := seedFor(t, seedTemplate, tc.vmConfig)

			if !strings.Contains(seed, tc.kept) {
				t.Errorf("%s was dropped from a VM that has its runtime:\n%s", tc.kept, seed)
			}
			if strings.Contains(seed, tc.dropped) {
				t.Errorf("%s was granted to a VM without its runtime:\n%s", tc.dropped, seed)
			}
			// Either runtime keeps the shared group.
			if !strings.Contains(seed, "cdn.playwright.dev") {
				t.Errorf("the either-runtime group was dropped:\n%s", seed)
			}
		})
	}
}

// The markers are read in the template only. A VM's own list is a plain list -
// nothing re-reads it against the config, so a marker left in it would claim a
// relationship that does not exist.
func TestTheMarkersDoNotSurviveIntoAVMsList(t *testing.T) {
	newHarness(t)
	seed := seedFor(t, seedTemplate, "PTRBOX_NODE=true\nPTRBOX_UV=true\n")

	for _, marker := range []string{"@requires", "@end"} {
		if strings.Contains(seed, marker) {
			t.Errorf("%s survived into the seeded list:\n%s", marker, seed)
		}
	}
}

// The template is a file people edit. A marker naming something that is not a
// runtime has two quiet readings - grant domains nothing can use, or take away
// domains somebody asked for - so it is neither, it is an error.
func TestAMalformedMarkerIsAnError(t *testing.T) {
	for name, template := range map[string]string{
		"unknown runtime": "# @requires python\nx.example.com\n# @end\n",
		"no runtime":      "# @requires\nx.example.com\n# @end\n",
		"unclosed":        "# @requires node\nx.example.com\n",
		"misspelled":      "# @require node\nx.example.com\n# @end\n",
		"nested":          "# @requires node\n# @requires uv\nx.example.com\n# @end\n# @end\n",
		"stray end":       "x.example.com\n# @end\n",
	} {
		t.Run(name, func(t *testing.T) {
			newHarness(t)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.SeedFor([]byte(template), cfg); err == nil {
				t.Errorf("SeedFor accepted %q", template)
			}
		})
	}
}

// The shipped template has to survive its own filter, both ways round: this is
// the file every sandbox starts from, and a typo in a marker there would fail
// every `ptrbox new` on the machine.
func TestTheShippedTemplateFiltersBothWays(t *testing.T) {
	h := newHarness(t)
	template, err := os.ReadFile(filepath.Join("..", "..", "host", "allowed_domains.txt"))
	if err != nil {
		t.Fatal(err)
	}

	bare, err := proxy.SeedFor(template, h.Cfg)
	if err != nil {
		t.Fatalf("the shipped template does not filter: %v", err)
	}
	// Claude Code's own domains are ungrouped: a sandbox that cannot reach
	// the API is not a sandbox, whatever its runtimes.
	for _, want := range []string{"api.anthropic.com", "claude.ai", "github.com"} {
		if !strings.Contains(string(bare), want) {
			t.Errorf("%s is not in a runtime-free sandbox's list", want)
		}
	}
	for _, gone := range []string{"registry.npmjs.org", "pypi.org", "astral.sh"} {
		if strings.Contains(string(bare), gone) {
			t.Errorf("%s reached a runtime-free sandbox's list", gone)
		}
	}

	both := seedFor(t, string(template), "PTRBOX_NODE=true\nPTRBOX_UV=true\n")
	for _, want := range []string{"registry.npmjs.org", "pypi.org", "astral.sh"} {
		if !strings.Contains(both, want) {
			t.Errorf("%s is missing from a VM that has both runtimes", want)
		}
	}
	// Playwright is its own flag, not a consequence of having a runtime: the
	// CDNs are useless without the Chromium packages, and those cost ~20 apt
	// packages that only PTRBOX_PLAYWRIGHT installs. Both runtimes on is
	// still not a browser-testing VM.
	if strings.Contains(both, "cdn.playwright.dev") {
		t.Error("the Playwright CDNs were granted to a VM that has no Chromium libraries")
	}
	withBrowser := seedFor(t, string(template), "PTRBOX_NODE=true\nPTRBOX_PLAYWRIGHT=true\n")
	if !strings.Contains(withBrowser, "cdn.playwright.dev") {
		t.Error("cdn.playwright.dev is missing from a VM that asked for Playwright")
	}
}

// The filter runs on the way in and never again: once the file exists it is
// the user's, and turning a runtime off later must not rewrite it.
func TestTurningARuntimeOffDoesNotRewriteAnExistingList(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	if err := os.MkdirAll(config.VMDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.VMDir(), "demo"), []byte("PTRBOX_NODE=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.AllocatePort("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Sync(); err != nil {
		t.Fatal(err)
	}
	seeded, err := os.ReadFile(config.VMAllowlistPath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seeded), "registry.npmjs.org") {
		t.Fatalf("the node group never reached the seeded list:\n%s", seeded)
	}

	// node off, sync again.
	if err := os.WriteFile(filepath.Join(config.VMDir(), "demo"), []byte("PTRBOX_NODE=false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Sync(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(config.VMAllowlistPath("demo"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(seeded) {
		t.Errorf("an existing list was rewritten by a config change:\n%s", after)
	}
}
