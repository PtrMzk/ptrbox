// Package config resolves ptrbox's settings and the names and paths derived
// from them.
//
// Precedence, lowest to highest: built-in defaults < config file <
// environment. Every key is a PTRBOX_<KEY> environment variable and the same
// name in the config file.
//
// Values here are interpolated into a guest firewall ruleset, a Lima config
// and a root shell script inside the guest, so they are validated rather than
// trusted: a typo in a config file must fail at load, not produce a VM with a
// subtly wrong wall.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Version is reported by `ptrbox version`.
const Version = "0.1.0"

// Keys are the settable names, in the order they are read. Every entry is
// both a PTRBOX_<key> environment variable and a config-file key.
var Keys = []string{
	"REPO_ROOT", "CPUS", "MEMORY", "DISK", "PORT_MIN", "PORT_MAX",
	"PROXY_HOST", "PROXY_PORT", "PROXY_CPUS", "PROXY_MEMORY", "PROXY_DISK",
	"DNS_SERVERS", "CLAUDE_MODEL", "KEYCHAIN_SERVICE", "SQUID_LOG",
	"GIT_USER_NAME", "GIT_USER_EMAIL", "DISTRO", "IMAGE_URL", "BIN_DIR",
	"EXTRA_PACKAGES", "NODE", "NODE_VERSION", "UV",
}

// SandboxProxyPorts is how many sandbox VMs can hold a proxy port at once.
// Squid sees every client as 127.0.0.1 (the usernet relay and the loopback
// forward erase the source), so the port a sandbox dials is the only signal
// that survives the trip - each VM gets its own, and that port is its identity
// at the proxy. The range is static and declared in vm/proxy.yaml, because
// adding a forward to a running proxy VM means restarting it, which takes the
// network away from every live sandbox at once. 16 is a ceiling on concurrent
// sandboxes, not on repos - ports free on `ptrbox rm`.
const SandboxProxyPorts = 16

// SandboxPortMin is the first per-sandbox proxy port. PROXY_PORT itself stays
// the base port: the Mac's own way into squid, and what install verifies.
func (c *Config) SandboxPortMin() int { return c.ProxyPort + 1 }

// SandboxPortMax is the last per-sandbox proxy port.
func (c *Config) SandboxPortMax() int { return c.ProxyPort + SandboxProxyPorts }

// Toolchains are the language runtimes vm/provision/30-toolchain.sh knows how
// to install, in the order it installs them. Each name carries three jobs, and
// they are deliberately the same string:
//
//   - upper-cased it is the config key that turns the runtime on
//     (node -> PTRBOX_NODE), so there is no table mapping one to the other;
//   - it is what 30-toolchain.sh records in ~/.ptrbox/toolchain before
//     installing anything;
//   - it is the command vm/verify.sh then looks for on PATH, which is what
//     makes a requested runtime that did not install a failed `ptrbox new`
//     rather than a surprise a week later.
//
// Every entry must therefore also appear in Keys; a test asserts it. All of
// them default to false: a sandbox installs a runtime because someone asked
// for it, not because it is a sandbox.
//
// Claude Code is deliberately absent - it is installed unconditionally, being
// the thing a sandbox exists to run, and it is a native binary that needs
// neither of these.
var Toolchains = []string{"node", "uv"}

// toolchainKey is the config key that switches a runtime on.
func toolchainKey(tool string) string { return strings.ToUpper(tool) }

// Guest images, one per supported distro. Both are apt-based on purpose: the
// provisioning scripts install Debian package names, and since the time_t
// transition those names are identical on trixie and noble. A dnf or pacman
// distro would need its own base script, not just a URL here.
//
// Always-current URLs (no pinned build), so fresh VMs pick up security
// updates.
var images = []struct{ distro, url string }{
	{"debian13", "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2"},
	{"ubuntu2404", "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-arm64.img"},
}

// Distros lists the supported PTRBOX_DISTRO values, in declaration order.
func Distros() []string {
	names := make([]string, 0, len(images))
	for _, im := range images {
		names = append(names, im.distro)
	}
	return names
}

func imageFor(distro string) string {
	for _, im := range images {
		if im.distro == distro {
			return im.url
		}
	}
	return ""
}

// Config is the resolved configuration for one ptrbox run.
type Config struct {
	RepoRoot        string
	CPUs            int
	Memory          string
	Disk            string
	PortMin         int
	PortMax         int
	ProxyHost       string
	ProxyPort       int
	ProxyCPUs       int
	ProxyMemory     string
	ProxyDisk       string
	DNSServers      []string
	ClaudeModel     string
	KeychainService string
	SquidLog        string
	GitUserName     string
	GitUserEmail    string
	Distro          string
	ImageURL        string
	BinDir          string
	ExtraPackages   []string
	Toolchain       []string
	NodeVersion     string

	// Warnings are non-fatal notes from loading - an unrecognised key in the
	// config file, say. The caller prints them; nothing here writes to a
	// stream of its own.
	Warnings []string
}

func defaults() map[string]string {
	home := os.Getenv("HOME")
	return map[string]string{
		"REPO_ROOT": filepath.Join(home, "code"),
		"CPUS":      "4",
		"MEMORY":    "8GiB",
		"DISK":      "50GiB",
		"PORT_MIN":  "3000",
		"PORT_MAX":  "9000",
		// Where a sandbox VM reaches the proxy: Lima usernet's conventional
		// gateway address, which relays to 127.0.0.1 on the host - where the
		// proxy VM's port forward listens. If `ip route | grep default`
		// inside a VM shows something else, override here.
		"PROXY_HOST": "192.168.5.2",
		"PROXY_PORT": "8888",
		// The proxy VM runs squid and nothing else; it stays deliberately tiny.
		"PROXY_CPUS":   "1",
		"PROXY_MEMORY": "512MiB",
		"PROXY_DISK":   "4GiB",
		// Quad9 + Cloudflare. Quad9 also blocks known-malicious domains at
		// resolution time, a bonus filter layer.
		"DNS_SERVERS":      "9.9.9.9 1.1.1.1",
		"CLAUDE_MODEL":     "opus",
		"KEYCHAIN_SERVICE": "claude-sandbox-token",
		"DISTRO":           "debian13",
		// Where `ptrbox install` offers to symlink the CLI.
		"BIN_DIR": filepath.Join(home, "bin"),
		// Extra apt packages for sandbox VMs, space separated. Host-side by
		// design: the list is rendered into the generated config at
		// `ptrbox new` time, never read from inside a VM (a repo-provided
		// list would let an agent install into its own sandbox).
		"EXTRA_PACKAGES": "",
		// Language runtimes, alongside the unconditional Claude Code. OFF by
		// default, both of them: a VM for a LaTeX document or a Go service
		// has no use for either, every runtime installed is surface the agent
		// can reach, and each one costs its own group of allowlisted domains.
		// What node buys is npm/npx - so a sandbox without it also has no
		// npx-based MCP servers, which is the one consequence of this default
		// worth knowing before it surprises someone.
		"NODE": "false",
		"UV":   "false",
		// What `nvm install` is given. "lts" is the always-fresh default;
		// a version pins a project to the runtime it needs.
		"NODE_VERSION": "lts",
	}
}

// Layers, lowest precedence first. Every layer above the first is sparse: it
// says only what it changes, and a key it does not mention falls through to
// the layer below. Only layerDefault is complete, and it lives in code rather
// than in a file - which is why the shipped example config has every line
// commented out and an empty config file is a valid one.
const (
	layerDefault = iota
	layerFile    // ~/.config/ptrbox/config
	layerVM      // ~/.config/ptrbox/vms/<name>
	layerEnv     // PTRBOX_*
)

// Load resolves defaults, the config file and the environment into a
// validated Config. Per-VM overrides are not applied: the VM's name is not
// known until a command has parsed its arguments. See Config.Overlay.
func Load() (*Config, error) { return load("") }

// load resolves every layer. vm is the sandbox whose per-VM file should be
// layered in, or "" for none.
func load(vm string) (*Config, error) {
	values := defaults()
	var warnings []string

	// Which layer last set each key. Only the derivation below reads this,
	// and only to settle one question: whether a distro named higher up than
	// an image URL should re-derive that URL.
	origin := map[string]int{}
	apply := func(from map[string]string, layer int) {
		for key, value := range from {
			values[key] = value
			origin[key] = layer
		}
	}

	// The config file, if there is one. Unlike the bash version this parses
	// KEY=value rather than sourcing, so a config file is data now, not code
	// that runs with your privileges before ptrbox does anything.
	path := Path()
	fileValues, fileWarnings, err := parseFile(path)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, fileWarnings...)
	apply(fileValues, layerFile)

	// The per-VM file, if this run is about a particular sandbox. Restricted
	// to the keys a VM can own; see perVMKeys for why the rest are refused
	// rather than ignored.
	if vm != "" {
		vmPath := VMConfigPath(vm)
		vmValues, vmWarnings, err := parseFile(vmPath)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, vmWarnings...)
		for _, key := range Keys {
			if _, ok := vmValues[key]; ok && !perVMKeys[key] {
				return nil, fmt.Errorf(
					"%s: PTRBOX_%s describes the host, not one VM, so it cannot be set per VM"+
						" - move it to %s (settable per VM: %s)",
					vmPath, key, Path(), strings.Join(PerVMKeys(), " "))
			}
		}
		apply(vmValues, layerVM)
	}

	// The environment wins over every file.
	for _, key := range Keys {
		if value, ok := os.LookupEnv("PTRBOX_" + key); ok {
			values[key] = value
			origin[key] = layerEnv
		}
	}

	// Derived defaults, resolved after every layer has had its say.

	// A path INSIDE the proxy VM (Debian squid's default), read via
	// limactl shell.
	if values["SQUID_LOG"] == "" {
		values["SQUID_LOG"] = "/var/log/squid/access.log"
	}

	// Distro -> image URL, unless an explicit URL was configured. The
	// override is an escape hatch for pinning a build or trying another
	// apt-based image; you are on your own for package names if the image is
	// not Debian-family.
	//
	// A distro named at a HIGHER layer than the image URL re-derives it: a
	// per-VM `DISTRO=ubuntu2404` next to a pinned IMAGE_URL in the main
	// config otherwise produces a VM labelled ubuntu that boots the Debian
	// image, and nothing anywhere says so. Same layer means the URL was the
	// more specific statement of the two, so it stands.
	if values["IMAGE_URL"] == "" || origin["DISTRO"] > origin["IMAGE_URL"] {
		values["IMAGE_URL"] = imageFor(values["DISTRO"])
		if values["IMAGE_URL"] == "" {
			return nil, fmt.Errorf("unknown PTRBOX_DISTRO %q (supported: %s)",
				values["DISTRO"], strings.Join(Distros(), " "))
		}
	}

	// Git identity for in-VM commits, taken from the host unless configured.
	// Empty is a valid answer: the host may have no identity either, and
	// vm/provision/40-userenv.sh then leaves git unconfigured rather than
	// inventing one.
	if values["GIT_USER_NAME"] == "" {
		values["GIT_USER_NAME"] = gitGlobal("user.name")
	}
	if values["GIT_USER_EMAIL"] == "" {
		values["GIT_USER_EMAIL"] = gitGlobal("user.email")
	}

	cfg := &Config{
		RepoRoot:        values["REPO_ROOT"],
		Memory:          values["MEMORY"],
		Disk:            values["DISK"],
		ProxyHost:       values["PROXY_HOST"],
		ProxyMemory:     values["PROXY_MEMORY"],
		ProxyDisk:       values["PROXY_DISK"],
		DNSServers:      strings.Fields(values["DNS_SERVERS"]),
		ClaudeModel:     values["CLAUDE_MODEL"],
		KeychainService: values["KEYCHAIN_SERVICE"],
		SquidLog:        values["SQUID_LOG"],
		// Double quotes and backslashes are stripped: these values are
		// interpolated into a shell assignment inside the guest.
		GitUserName:   stripQuoting(values["GIT_USER_NAME"]),
		GitUserEmail:  stripQuoting(values["GIT_USER_EMAIL"]),
		Distro:        values["DISTRO"],
		ImageURL:      values["IMAGE_URL"],
		BinDir:        values["BIN_DIR"],
		ExtraPackages: strings.Fields(values["EXTRA_PACKAGES"]),
		NodeVersion:   values["NODE_VERSION"],
		Warnings:      warnings,
	}

	// The runtime list, assembled from one boolean per runtime. It stays a
	// list past this point because that is what the guest needs: 30-toolchain.sh
	// records it and vm/verify.sh checks every name in it is on PATH. Only how
	// the request is SPELLED is per-runtime; the assertion is unchanged.
	for _, tool := range Toolchains {
		key := toolchainKey(tool)
		on, err := parseBoolean(key, values[key])
		if err != nil {
			return nil, err
		}
		if on {
			cfg.Toolchain = append(cfg.Toolchain, tool)
		}
	}

	// Numbers first, so a later range check has something to compare.
	for _, num := range []struct {
		key   string
		field *int
	}{
		{"CPUS", &cfg.CPUs},
		{"PROXY_CPUS", &cfg.ProxyCPUs},
		{"PORT_MIN", &cfg.PortMin},
		{"PORT_MAX", &cfg.PortMax},
		{"PROXY_PORT", &cfg.ProxyPort},
	} {
		n, err := parseNumber(num.key, values[num.key])
		if err != nil {
			return nil, err
		}
		*num.field = n
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

var (
	numberRe  = regexp.MustCompile(`^[0-9]+$`)
	sizeRe    = regexp.MustCompile(`^[0-9].*(GiB|MiB)$`)
	ipv4Re    = regexp.MustCompile(`^[0-9.]+$`)
	packageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.+=:~-]*$`)
	// nvm's version vocabulary, minus every shell metacharacter.
	nodeVersionRe = regexp.MustCompile(`^[a-z0-9][a-z0-9./-]*$`)
)

// parseBoolean reads an on/off setting. The spellings are the ones a person
// writing a shell-ish config file would try; anything else is an error rather
// than a falsy default, because "PTRBOX_NODE=ture" silently meaning "no node"
// is a quiet wrong answer. An empty value is off - it reads as deliberately
// unset.
func parseBoolean(key, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false", "no", "off", "0":
		return false, nil
	case "true", "yes", "on", "1":
		return true, nil
	}
	return false, fmt.Errorf("PTRBOX_%s must be true or false, got %q", key, value)
}

func parseNumber(key, value string) (int, error) {
	if !numberRe.MatchString(value) {
		return 0, fmt.Errorf("PTRBOX_%s must be a number, got %q", key, value)
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("PTRBOX_%s must be a number, got %q", key, value)
	}
	return n, nil
}

func (c *Config) validate() error {
	if c.PortMin > c.PortMax {
		return fmt.Errorf("PTRBOX_PORT_MIN (%d) is above PTRBOX_PORT_MAX (%d)",
			c.PortMin, c.PortMax)
	}

	for _, size := range []struct{ key, value string }{
		{"MEMORY", c.Memory},
		{"DISK", c.Disk},
		{"PROXY_MEMORY", c.ProxyMemory},
		{"PROXY_DISK", c.ProxyDisk},
	} {
		if !sizeRe.MatchString(size.value) {
			return fmt.Errorf("PTRBOX_%s must look like 8GiB or 512MiB, got %q",
				size.key, size.value)
		}
	}

	// The sandbox ports sit directly above the base port, so the whole block
	// has to fit under the port ceiling.
	if c.ProxyPort < 1 || c.ProxyPort+SandboxProxyPorts > 65535 {
		return fmt.Errorf("PTRBOX_PROXY_PORT must leave room for %d sandbox ports above it, got %d",
			SandboxProxyPorts, c.ProxyPort)
	}

	if !ipv4Re.MatchString(c.ProxyHost) {
		return fmt.Errorf("PTRBOX_PROXY_HOST must be an IPv4 address, got %q", c.ProxyHost)
	}
	for _, ns := range c.DNSServers {
		if !ipv4Re.MatchString(ns) {
			return fmt.Errorf("PTRBOX_DNS_SERVERS must be an IPv4 address, got %q", ns)
		}
	}
	if len(c.DNSServers) == 0 {
		return fmt.Errorf("PTRBOX_DNS_SERVERS is empty")
	}

	// Each package name is interpolated into a root shell script inside the
	// guest, so hold it to Debian's package-name charset (plus '=' and the
	// version charset for pins like pkg=1.2-3). Anything else - shell
	// metacharacters, an option-like leading dash - must stop the run here.
	for _, pkg := range c.ExtraPackages {
		if !packageRe.MatchString(pkg) {
			return fmt.Errorf("PTRBOX_EXTRA_PACKAGES: %q is not a valid apt package name", pkg)
		}
	}

	// The guest image is downloaded and booted with no signature check beyond
	// TLS, so plain http would be a supply-chain hole.
	if !strings.HasPrefix(c.ImageURL, "https://") {
		return fmt.Errorf("PTRBOX_IMAGE_URL must be https, got %q", c.ImageURL)
	}

	// Interpolated into `nvm install "…"` inside the guest, so the charset is
	// the whole check: no shell metacharacter may survive it. What passes is
	// everything nvm understands - lts, node, lts/hydrogen, 22, 22.11.0 - and
	// whether nvm actually has that version is a question only nvm can answer.
	// It answers it on boot 1, and vm/verify.sh turns a runtime that is not on
	// PATH into a failed `ptrbox new`.
	//
	// Checked even when node is off, because it is rendered into the guest
	// script either way: the script decides at run time whether to use it, so
	// the value has left the host by then regardless.
	if !nodeVersionRe.MatchString(c.NodeVersion) {
		return fmt.Errorf("PTRBOX_NODE_VERSION must look like lts, 22 or 22.11.0, got %q",
			c.NodeVersion)
	}
	return nil
}

// stripQuoting removes the two characters that would break out of the shell
// assignment the value is interpolated into inside the guest.
func stripQuoting(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' {
			return -1
		}
		return r
	}, s)
}

func gitGlobal(key string) string {
	out, err := exec.Command("git", "config", "--global", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
