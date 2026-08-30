package config

// Per-VM configuration: one optional file per sandbox, named for the VM, in
// ~/.config/ptrbox/vms/. It is a sparse override of the main config, which is
// itself a sparse override of the built-in defaults.
//
// HOST-SIDE, keyed by VM name, and never a file inside the repo. The
// idiomatic place for per-project settings is a dotfile in the project, and
// that is exactly what invariant 3 forbids: the repo is mounted into the
// sandbox, so a package list living there would let the agent install
// software into its own VM. Which packages exist in a VM stays a human
// decision made on the Mac.
//
// Keyed by name rather than selected with a --profile flag because the
// problem being solved is "I have to remember". A flag is forgotten at
// re-create time and silently yields a different VM; a file named for the VM
// survives `ptrbox rm`, which matters because changing any of these settings
// REQUIRES a re-create.

import (
	"fmt"
	"os"
)

// perVMKeys are the settings a per-VM file may override: the ones consumed
// once, at `ptrbox new` time, and then frozen into that VM's generated Lima
// config. Everything absent from this set describes the host - there is one
// proxy, one Keychain, one repo root - and a per-VM value for it would be
// meaningless at best. At worst it points one sandbox at a proxy that is not
// there, which surfaces minutes later as "the agent has no network".
//
// DNS_SERVERS is deliberately absent even though it is technically per-VM: it
// is rendered into that guest's nftables ruleset, so "this one sandbox
// resolves somewhere else" is an invariant-2 decision wearing a config key's
// clothes. It belongs in a commit message, not in a per-VM file.
var perVMKeys = map[string]bool{
	"CPUS":           true,
	"MEMORY":         true,
	"DISK":           true,
	"PORT_MIN":       true,
	"PORT_MAX":       true,
	"DISTRO":         true,
	"IMAGE_URL":      true,
	"EXTRA_PACKAGES": true,
	"GO":             true,
	"NODE":           true,
	"NODE_VERSION":   true,
	"PLAYWRIGHT":     true,
	"UV":             true,
	"CLAUDE_MODEL":   true,
	"GIT_USER_NAME":  true,
	"GIT_USER_EMAIL": true,
}

// PerVMKeys lists the per-VM settable keys in the order Keys declares them,
// for error messages and documentation.
func PerVMKeys() []string {
	names := make([]string, 0, len(perVMKeys))
	for _, key := range Keys {
		if perVMKeys[key] {
			names = append(names, key)
		}
	}
	return names
}

// Overlay re-resolves the configuration with vm's per-VM file layered in,
// between the main config file and the environment. It returns a new Config;
// the receiver is left alone.
//
// Every layer is re-read rather than patched, so a derived value settles
// against the final answer: a per-VM distro has to re-derive the image URL,
// and patching the distro field alone would not.
//
// Warnings are only the ones this layer added. The caller has already printed
// whatever Load produced, and repeating it would read as two problems.
func (c *Config) Overlay(vm string) (*Config, error) {
	// The name reaches the filesystem as a path element, so hold it to the
	// one rule that already decides what a VM may be called. Anything that
	// does not survive VMName unchanged - a separator, a "..", an empty
	// string - is not a VM name and cannot have a file.
	if name, err := VMName(vm); err != nil || name != vm {
		return nil, fmt.Errorf("%q is not a VM name, so it cannot have a per-VM config file", vm)
	}

	out, err := load(vm)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(c.Warnings))
	for _, warning := range c.Warnings {
		seen[warning] = true
	}
	// Left nil when there is nothing to say, so a config with no per-VM file
	// is indistinguishable from one Load produced - which is what the
	// "changes nothing for anyone not using it" test compares.
	var fresh []string
	for _, warning := range out.Warnings {
		if !seen[warning] {
			fresh = append(fresh, warning)
		}
	}
	out.Warnings = fresh
	return out, nil
}

// HasVMConfig reports whether vm has a per-VM config file. `ptrbox new` names
// it in the closing summary: editing that file changes nothing until the VM
// is re-created, so create time is the moment to say which file was read.
func HasVMConfig(vm string) bool {
	info, err := os.Stat(VMConfigPath(vm))
	return err == nil && !info.IsDir()
}
