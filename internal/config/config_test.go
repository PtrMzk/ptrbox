package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setup gives a test a throwaway HOME, an empty config file, and an
// environment with no PTRBOX_* left over from the developer's shell - a stray
// one would silently outrank everything the test sets up.
func setup(t *testing.T) (home, configPath string) {
	t.Helper()
	tmp := t.TempDir()
	home = filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(tmp, "config")

	for _, key := range Keys {
		unset(t, "PTRBOX_"+key)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PTRBOX_CONFIG", configPath)
	// Deterministic git identity: without this, Load picks up whatever the
	// machine running the tests has configured.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return home, configPath
}

// unset removes a variable for the duration of the test. t.Setenv registers
// the restore; the Unsetenv is what the test actually wants.
func unset(t *testing.T, key string) {
	t.Helper()
	if _, ok := os.LookupEnv(key); ok {
		t.Setenv(key, "")
	}
	os.Unsetenv(key)
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustLoad(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// loadErr asserts Load fails and that the message mentions want.
func loadErr(t *testing.T, want string) {
	t.Helper()
	_, err := Load()
	if err == nil {
		t.Fatalf("Load succeeded; want an error mentioning %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Load error = %q; want it to mention %q", err, want)
	}
}

// --- precedence --------------------------------------------------------------

func TestDefaultsApplyWithNoConfigFile(t *testing.T) {
	home, _ := setup(t)
	cfg := mustLoad(t)

	if want := filepath.Join(home, "code"); cfg.RepoRoot != want {
		t.Errorf("RepoRoot = %q, want %q", cfg.RepoRoot, want)
	}
	if cfg.CPUs != 4 {
		t.Errorf("CPUs = %d, want 4", cfg.CPUs)
	}
	if got := strings.Join(cfg.DNSServers, " "); got != "9.9.9.9 1.1.1.1" {
		t.Errorf("DNSServers = %q, want \"9.9.9.9 1.1.1.1\"", got)
	}
}

// The shared proxy VM is not configured, it is decided - so what used to be
// load-time validation of six settings is a test over six constants. The
// numbers matter to more than taste: the port block has to fit under the
// ceiling, and the sizing is what item 28 will revisit with a measurement.
func TestTheFixedProxySettings(t *testing.T) {
	if !ipv4Re.MatchString(ProxyHost) {
		t.Errorf("ProxyHost = %q, want an IPv4 address", ProxyHost)
	}
	if ProxyPort < 1 || ProxyPort+SandboxProxyPorts > 65535 {
		t.Errorf("ProxyPort = %d leaves no room for %d sandbox ports", ProxyPort, SandboxProxyPorts)
	}
	if SandboxPortMin() != ProxyPort+1 || SandboxPortMax() != ProxyPort+SandboxProxyPorts {
		t.Errorf("sandbox port block = %d-%d, want %d above the base port",
			SandboxPortMin(), SandboxPortMax(), SandboxProxyPorts)
	}
	if ProxyCPUs < 1 {
		t.Errorf("ProxyCPUs = %d", ProxyCPUs)
	}
	for _, size := range []string{ProxyMemory, ProxyDisk} {
		if !sizeRe.MatchString(size) {
			t.Errorf("proxy size %q does not look like 8GiB or 512MiB", size)
		}
	}
	// A path inside the proxy VM, read over limactl shell.
	if !strings.HasPrefix(SquidLog, "/") {
		t.Errorf("SquidLog = %q, want an absolute path in the proxy VM", SquidLog)
	}
}

func TestConfigFileOverridesDefaults(t *testing.T) {
	_, path := setup(t)
	writeConfig(t, path, "PTRBOX_CPUS=8\nPTRBOX_MEMORY=16GiB\n")
	cfg := mustLoad(t)
	if cfg.CPUs != 8 || cfg.Memory != "16GiB" {
		t.Errorf("got %d/%s, want 8/16GiB", cfg.CPUs, cfg.Memory)
	}
}

func TestEnvironmentOverridesTheConfigFile(t *testing.T) {
	_, path := setup(t)
	writeConfig(t, path, "PTRBOX_CPUS=8\n")
	t.Setenv("PTRBOX_CPUS", "2")
	if cfg := mustLoad(t); cfg.CPUs != 2 {
		t.Errorf("CPUs = %d, want 2", cfg.CPUs)
	}
}

func TestEnvironmentOverridesDefaultsWithNoFile(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_REPO_ROOT", "/tmp/elsewhere")
	if cfg := mustLoad(t); cfg.RepoRoot != "/tmp/elsewhere" {
		t.Errorf("RepoRoot = %q, want /tmp/elsewhere", cfg.RepoRoot)
	}
}

func TestConfigFileMayReferenceHome(t *testing.T) {
	home, path := setup(t)
	writeConfig(t, path, `PTRBOX_REPO_ROOT="$HOME/projects"`+"\n")
	if cfg := mustLoad(t); cfg.RepoRoot != filepath.Join(home, "projects") {
		t.Errorf("RepoRoot = %q, want %s/projects", cfg.RepoRoot, home)
	}
}

// --- validation --------------------------------------------------------------
// These values are interpolated into a guest firewall ruleset, so bad input
// must stop the run rather than produce a VM with a subtly wrong wall.

func TestRejectsNonNumericCPUCount(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_CPUS", "many")
	loadErr(t, "PTRBOX_CPUS must be a number")
}

func TestRejectsMalformedMemory(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_MEMORY", "8GB")
	loadErr(t, "PTRBOX_MEMORY")
}

func TestRejectsDNSServerThatIsNotAnAddress(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_DNS_SERVERS", "9.9.9.9 ; rm -rf /")
	loadErr(t, "PTRBOX_DNS_SERVERS")
}

func TestRejectsEmptyDNSServers(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_DNS_SERVERS", "   ")
	loadErr(t, "PTRBOX_DNS_SERVERS is empty")
}

func TestRejectsInvertedPortRange(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_PORT_MIN", "9000")
	t.Setenv("PTRBOX_PORT_MAX", "3000")
	loadErr(t, "above")
}

func TestStripsQuotesAndBackslashesFromGitIdentity(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_GIT_USER_NAME", `Ex "quoted" \name`)
	if cfg := mustLoad(t); cfg.GitUserName != "Ex quoted name" {
		t.Errorf("GitUserName = %q, want %q", cfg.GitUserName, "Ex quoted name")
	}
}

// --- extra guest packages ----------------------------------------------------
// The list is interpolated into a root shell script inside the guest, so it is
// held to apt's package-name charset - config-file injection must die here,
// not become a command in the VM.

func TestExtraPackagesDefaultToNone(t *testing.T) {
	setup(t)
	if cfg := mustLoad(t); len(cfg.ExtraPackages) != 0 {
		t.Errorf("ExtraPackages = %v, want none", cfg.ExtraPackages)
	}
}

func TestExtraPackagesAcceptNamesPinsAndWrappedWhitespace(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_EXTRA_PACKAGES", "ripgrep   libfoo2.1+dev=1:2.0~rc1-3\n  sqlite3")
	cfg := mustLoad(t)
	if got := cfg.ExtraPackageList(); got != "ripgrep libfoo2.1+dev=1:2.0~rc1-3 sqlite3" {
		t.Errorf("ExtraPackageList = %q", got)
	}
}

func TestRejectsBadPackageNames(t *testing.T) {
	for _, bad := range []struct{ name, value string }{
		{"shell metacharacters", "ripgrep; rm -rf /"},
		{"command substitution", "jq$(reboot)"},
		{"apt option", "--reinstall"},
		// Debian names are lowercase; a stray Capital is a typo, not a package.
		{"uppercase", "RipGrep"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			setup(t)
			t.Setenv("PTRBOX_EXTRA_PACKAGES", bad.value)
			loadErr(t, "PTRBOX_EXTRA_PACKAGES")
		})
	}
}

// --- derived values ----------------------------------------------------------

func TestBuildsTheNftablesDNSSet(t *testing.T) {
	setup(t)
	if got := mustLoad(t).DNSNftSet(); got != "9.9.9.9, 1.1.1.1" {
		t.Errorf("DNSNftSet = %q", got)
	}
}

func TestNftablesDNSSetHandlesASingleResolver(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_DNS_SERVERS", "9.9.9.9")
	if got := mustLoad(t).DNSNftSet(); got != "9.9.9.9" {
		t.Errorf("DNSNftSet = %q", got)
	}
}

func TestDefaultsToTheDebianImage(t *testing.T) {
	setup(t)
	cfg := mustLoad(t)
	if cfg.Distro != "debian13" {
		t.Errorf("Distro = %q", cfg.Distro)
	}
	if !strings.HasSuffix(cfg.ImageURL, "debian-13-genericcloud-arm64.qcow2") {
		t.Errorf("ImageURL = %q", cfg.ImageURL)
	}
}

func TestDistroSelectsTheUbuntuImage(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_DISTRO", "ubuntu2404")
	if cfg := mustLoad(t); !strings.HasSuffix(cfg.ImageURL, "ubuntu-24.04-server-cloudimg-arm64.img") {
		t.Errorf("ImageURL = %q", cfg.ImageURL)
	}
}

func TestUnknownDistroIsRejectedWithTheSupportedList(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_DISTRO", "gentoo")
	loadErr(t, `unknown PTRBOX_DISTRO "gentoo"`)
	_, err := Load()
	if !strings.Contains(err.Error(), "debian13 ubuntu2404") {
		t.Errorf("error does not list the supported distros: %v", err)
	}
}

func TestExplicitImageURLOverridesTheDistro(t *testing.T) {
	setup(t)
	t.Setenv("PTRBOX_IMAGE_URL", "https://example.com/custom-arm64.qcow2")
	if cfg := mustLoad(t); cfg.ImageURL != "https://example.com/custom-arm64.qcow2" {
		t.Errorf("ImageURL = %q", cfg.ImageURL)
	}
}

func TestPlainHTTPImageURLIsRefused(t *testing.T) {
	// The image is booted with no verification beyond TLS.
	setup(t)
	t.Setenv("PTRBOX_IMAGE_URL", "http://example.com/custom-arm64.qcow2")
	loadErr(t, "must be https")
}

// --- names and paths ---------------------------------------------------------

func TestBareRepoNamesLandUnderTheRepoRoot(t *testing.T) {
	home, _ := setup(t)
	cfg := mustLoad(t)
	if got := cfg.RepoDir("my-api"); got != filepath.Join(home, "code", "my-api") {
		t.Errorf("RepoDir = %q", got)
	}
}

func TestRepoPathsAreUsedLiterally(t *testing.T) {
	setup(t)
	cfg := mustLoad(t)
	for _, path := range []string{"/src/thing", "./thing"} {
		if got := cfg.RepoDir(path); got != path {
			t.Errorf("RepoDir(%q) = %q, want it unchanged", path, got)
		}
	}
}

func TestVMNamesAreLowercasedAndStripped(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"My.Repo", "myrepo"},
		{"/Users/x/code/Some_Repo", "somerepo"},
		{"already-fine", "already-fine"},
	} {
		got, err := VMName(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("VMName(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestVMNameDerivationIsIdenticalForAPathAndItsBasename(t *testing.T) {
	fromPath, err1 := VMName("/home/x/code/My.Api")
	fromName, err2 := VMName("My.Api")
	if err1 != nil || err2 != nil || fromPath != fromName {
		t.Errorf("%q != %q (%v, %v)", fromPath, fromName, err1, err2)
	}
}

func TestVMColorsAreStableAndStayInThePalette(t *testing.T) {
	// Same name, same color - across calls and therefore across recreations.
	if VMColor("demo") != VMColor("demo") {
		t.Error("VMColor is not stable")
	}
	inPalette := map[string]bool{"1;31": true, "1;32": true, "1;33": true, "1;35": true, "1;36": true}
	for _, name := range []string{"demo", "myrepo", "a", "zz", "some-long-repo-name"} {
		if !inPalette[VMColor(name)] {
			t.Errorf("unexpected color for %s: %s", name, VMColor(name))
		}
	}
	// Similar names should usually differ - pin one known-distinct pair.
	if VMColor("demo") == VMColor("demp") {
		t.Error("demo and demp share a color")
	}
}

func TestRejectsARepoNameWithNoUsableCharacters(t *testing.T) {
	if _, err := VMName("!!!"); err == nil || !strings.Contains(err.Error(), "no usable characters") {
		t.Errorf("VMName(%q) error = %v", "!!!", err)
	}
}

func TestRejectsANameThatWouldStartWithADash(t *testing.T) {
	if _, err := VMName("-lead"); err == nil {
		t.Error("VMName(-lead) succeeded")
	}
}

func TestPathPrecedence(t *testing.T) {
	home, configPath := setup(t)
	if Path() != configPath {
		t.Errorf("Path() = %q, want %q", Path(), configPath)
	}
	unset(t, "PTRBOX_CONFIG")
	want := filepath.Join(home, ".config", "ptrbox", "config")
	if Path() != want {
		t.Errorf("Path() = %q, want %q", Path(), want)
	}
	unset(t, "XDG_CONFIG_HOME")
	if Path() != want {
		t.Errorf("Path() without XDG = %q, want %q", Path(), want)
	}
	if AllowlistPath() != filepath.Join(filepath.Dir(want), "allowed_domains.txt") {
		t.Errorf("AllowlistPath() = %q", AllowlistPath())
	}
	if GeneratedConfig("demo") != filepath.Join(home, ".lima", "_generated", "demo.yaml") {
		t.Errorf("GeneratedConfig = %q", GeneratedConfig("demo"))
	}
	if SSHConfigLink("demo") != filepath.Join(home, ".ssh", "config.d", "lima-demo") {
		t.Errorf("SSHConfigLink = %q", SSHConfigLink("demo"))
	}
}

func TestRecordManifestAppends(t *testing.T) {
	setup(t)
	for _, line := range []string{"wrote a", "linked b"} {
		if err := RecordManifest(line); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(Dir(), "install-manifest"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "wrote a\nlinked b\n" {
		t.Errorf("manifest = %q", got)
	}
}
