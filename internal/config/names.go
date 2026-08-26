package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ProxyVM is the name of the shared egress proxy VM. It is reserved: no
// sandbox may take it, and it is excluded from `rm`/`start`/`stop` name
// resolution and from the "is any sandbox still running" count.
const ProxyVM = "ptrbox-proxy"

// RepoDir turns a repo argument into a host path. A bare name lands under the
// configured repo root; anything containing a separator is taken literally.
func (c *Config) RepoDir(arg string) string {
	if strings.ContainsRune(arg, filepath.Separator) {
		return arg
	}
	return filepath.Join(c.RepoRoot, arg)
}

// VMName turns a repo path or name into a Lima VM name: lowercased and
// stripped to [a-z0-9-], which is what Lima accepts. `new` and `rm` share this
// so the two cannot drift.
func VMName(arg string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(arg)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	name := b.String()
	switch {
	case name == "":
		return "", fmt.Errorf("%q has no usable characters for a VM name", arg)
	case strings.HasPrefix(name, "-"):
		return "", fmt.Errorf("%q would produce a VM name starting with '-'", arg)
	}
	return name, nil
}

// palette skips blue (the guest prompt's cwd color) and unstyled white/black.
// Red stays in - it reads "caution", which a sandbox shell arguably should.
var palette = []string{"31", "32", "33", "35", "36"}

// VMColor maps a VM name to a stable ANSI color (SGR attributes, e.g. "1;32")
// for the guest prompt, so shells in different sandboxes are tellable apart at
// a glance. Plain byte sum: names differing by one letter usually land on
// different colors, and the same name always gets the same one.
func VMColor(name string) string {
	sum := 0
	for i := 0; i < len(name); i++ {
		sum += int(name[i])
	}
	return "1;" + palette[sum%len(palette)]
}

// DNSNftSet renders the resolver list as an nftables set body:
// "9.9.9.9 1.1.1.1" -> "9.9.9.9, 1.1.1.1".
func (c *Config) DNSNftSet() string { return strings.Join(c.DNSServers, ", ") }

// ExtraPackageList is the validated package list as it is substituted into the
// generated config: a single space-separated line.
func (c *Config) ExtraPackageList() string { return strings.Join(c.ExtraPackages, " ") }

// ToolchainList is the validated runtime list as it is substituted into the
// generated config, and as vm/verify.sh reads it back: a single
// space-separated line, empty when no runtime was asked for.
func (c *Config) ToolchainList() string { return strings.Join(c.Toolchain, " ") }

// DNSList is the resolver list as the guest's resolv.conf writer wants it.
func (c *Config) DNSList() string { return strings.Join(c.DNSServers, " ") }
