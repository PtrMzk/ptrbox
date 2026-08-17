package cli

// cmd_allow - manage the egress allowlist.
//
//	ptrbox allow api.example.com      append one or more domains
//	ptrbox allow                      open the file in $EDITOR
//	ptrbox allow --list               print the current domains
//
// The allowlist lives on the HOST (next to ptrbox's config file) and is the
// source of truth; the proxy package pushes it into the proxy VM, where squid
// validates it and reloads with `squid -k reconfigure` - which drops no live
// tunnels. A restart would sever every VM's connection, including Claude's
// in-flight request.
//
// If squid rejects the result, both the host file and the VM are restored and
// your version is kept alongside - a broken allowlist means squid refuses to
// start, which takes every sandbox offline.
//
// With the proxy VM down, edits still land in the host file and are pushed by
// the next `ptrbox new`/`ptrbox start`.
//
// Every entry is a capability grant to EVERY VM: the proxy is shared and
// cannot tell sandboxes apart.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/proxy"
)

const allowHelp = `ptrbox allow - manage the egress allowlist

  ptrbox allow <domain>...   append domains (a leading dot covers subdomains)
  ptrbox allow               open the allowlist in $EDITOR
  ptrbox allow --list        print the current domains

Changes are validated and reloaded without dropping live connections.
`

func cmdAllow(env *Env, args []string) error {
	var domains []string
	list := false

	for _, arg := range args {
		switch {
		case arg == "--list":
			list = true
		case arg == "-h" || arg == "--help":
			fmt.Fprint(env.Stdout, allowHelp)
			return nil
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("allow: unknown option %q", arg)
		default:
			domains = append(domains, arg)
		}
	}

	path := config.AllowlistPath()
	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no allowlist at %s - run 'ptrbox install' first", path)
	}

	if list {
		// Strip comments and blanks: just the capabilities.
		for _, entry := range allowEntries(original) {
			fmt.Fprintln(env.Stdout, entry)
		}
		return nil
	}

	changed := false
	if len(domains) > 0 {
		changed, err = appendDomains(env, path, original, domains)
	} else {
		changed, err = editAllowlist(env, path, original)
	}
	if err != nil || !changed {
		return err
	}

	// --- apply to the proxy VM ----------------------------------------------
	if !env.Proxy.Running() {
		env.Out.Say("saved. The proxy VM is not running; the change is applied when it next starts.")
		return nil
	}

	result, err := env.Proxy.Sync()
	if err != nil {
		return err
	}
	if result == proxy.Rejected {
		// The VM was already restored by the sync; restore the host file too,
		// keeping the refused version for inspection.
		rejected := path + ".rejected"
		if err := os.Rename(path, rejected); err != nil {
			return err
		}
		if err := os.WriteFile(path, original, 0o644); err != nil {
			return err
		}
		env.Out.Say("squid rejected the new allowlist; restored the previous one")
		env.Out.Say("your version is kept at %s", rejected)
		return errors.New("the allowlist was not applied")
	}
	env.Out.Say("allowlist reloaded (no tunnels dropped)")
	return nil
}

// appendDomains adds validated domains, reporting whether anything changed.
func appendDomains(env *Env, path string, original []byte, domains []string) (bool, error) {
	// Validate every domain before writing any of them: a rejected domain
	// must never reach the file, not even behind an accepted one.
	for _, domain := range domains {
		if err := assertDomain(domain); err != nil {
			return false, err
		}
	}

	body := original
	added := 0
	for _, domain := range domains {
		if allowContains(body, domain) {
			env.Out.Say("%s is already allowed", domain)
			continue
		}
		if len(body) > 0 && !bytes.HasSuffix(body, []byte("\n")) {
			body = append(body, '\n')
		}
		body = append(body, []byte(domain+"\n")...)
		env.Out.Say("added %s", domain)
		added++
	}
	if added == 0 {
		return false, nil
	}
	return true, os.WriteFile(path, body, 0o644)
}

// editAllowlist opens $EDITOR and reports whether the file came back changed.
func editAllowlist(env *Env, path string, original []byte) (bool, error) {
	if err := env.Editor(path); err != nil {
		return false, err
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if bytes.Equal(original, edited) {
		env.Out.Say("unchanged")
		return false, nil
	}
	return true, nil
}

// assertDomain refuses anything that is not plainly a hostname: the value
// becomes a squid ACL entry.
func assertDomain(domain string) error {
	switch {
	case domain == "" || domain == ".":
		return errors.New("empty domain")
	case strings.HasPrefix(domain, "-"):
		return fmt.Errorf("%q starts with a hyphen", domain)
	case strings.HasSuffix(domain, "."):
		return fmt.Errorf("%q ends with a dot", domain)
	}
	for _, r := range domain {
		ok := r == '.' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("%q is not a domain (letters, digits, dots and hyphens only)", domain)
		}
	}
	if !strings.Contains(domain, ".") {
		return fmt.Errorf("%q has no dot - a bare name is not a domain", domain)
	}
	return nil
}

// allowEntries is the allowlist's first fields, comments and blanks dropped.
func allowEntries(body []byte) []string {
	var entries []string
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, strings.Fields(line)[0])
	}
	return entries
}

// allowContains compares first fields, so a comment after the domain does not
// produce a duplicate.
func allowContains(body []byte, domain string) bool {
	for _, entry := range allowEntries(body) {
		if entry == domain {
			return true
		}
	}
	return false
}
