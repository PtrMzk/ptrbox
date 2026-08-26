package cli

// cmd_allow - manage a sandbox's egress allowlist.
//
//	ptrbox allow my-api registry.example.com   append one or more domains
//	ptrbox allow my-api                        open the VM's list in $EDITOR
//	ptrbox allow my-api --list                 print the VM's list
//
// The VM comes first, always: since item 38 every sandbox has its own list
// (~/.config/ptrbox/allowed_domains.d/<vm>.txt) and there is nothing global
// left to edit here - the shared file is a TEMPLATE, copied at create time,
// edited by hand, and needing no reload. No legal VM name contains a dot and
// every domain must, so a domain in first position is a diagnosable mistake
// rather than an ambiguity.
//
// The first touch of a VM's list seeds it from the template. The VM does not
// have to exist yet: the file outliving the VM is what makes declaring
// egress before `ptrbox new` - and reproducing it after `ptrbox rm` - work,
// so an unknown name gets a warning (it is also the typo detector), not an
// error.
//
// The lists live on the HOST and are the source of truth; the proxy package
// pushes them into the proxy VM, where squid validates the result and
// reloads with `squid -k reconfigure` - which drops no live tunnels. If
// squid rejects the result, both the host file and the VM are restored and
// your version is kept alongside. With the proxy VM down, edits still land
// in the host file and are pushed by the next `ptrbox new`/`start`, or by
// `ptrbox sync-proxy`.

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

const allowHelp = `ptrbox allow - manage a sandbox's egress allowlist

  ptrbox allow <vm> <domain>...   append domains (a leading dot covers subdomains)
  ptrbox allow <vm>               open the VM's allowlist in $EDITOR
  ptrbox allow <vm> --list        print the VM's allowlist

Each sandbox has its own list; the first touch seeds it from the template.
What FUTURE sandboxes start with is the template itself -
~/.config/ptrbox/allowed_domains.txt - edited by hand, no reload needed.
Changes to a VM's list are validated and reloaded without dropping live
connections; after editing files by hand, apply them with: ptrbox sync-proxy
`

func cmdAllow(env *Env, args []string) error {
	var positional []string
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
			positional = append(positional, arg)
		}
	}

	if len(positional) == 0 {
		return errors.New("usage: ptrbox allow <vm> [domain...] - the VM comes first; each sandbox has its own list")
	}
	if strings.Contains(positional[0], ".") && !strings.ContainsAny(positional[0], "/\\") {
		return fmt.Errorf("%q looks like a domain - the VM comes first: ptrbox allow <vm> %s",
			positional[0], positional[0])
	}
	name, err := resolveSandbox(positional[0],
		fmt.Sprintf("%q is the shared egress proxy - it has no allowlist of its own", config.ProxyVM))
	if err != nil {
		return err
	}
	domains := positional[1:]

	path := config.VMAllowlistPath(name)

	if list {
		return listAllowlist(env, name, path)
	}

	// A name nothing answers to is almost always a typo, and this warning is
	// the moment to catch it - but not an error: the file outliving the VM is
	// what makes pre-create declaration and re-creates work.
	if env.Lima.Available() && !env.Lima.Exists(name) {
		env.Out.Warn("no VM named %q yet - this list takes effect when it is created", name)
	}

	// Seed-on-first-touch, from the template: the list must stay complete,
	// because nothing falls through behind it.
	seeded, err := env.Proxy.EnsureVMAllowlist(name)
	if err != nil {
		return err
	}
	original := []byte(seeded)

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
	env.Out.Say("allowlist for %q reloaded (no tunnels dropped)", name)
	return nil
}

// listAllowlist prints what the VM may reach. Reads never seed: with no file
// yet, the truthful answer is the template the VM will start from, said so.
func listAllowlist(env *Env, name, path string) error {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		env.Out.Say("%q has no list yet; it will start from the template:", name)
		body, err = os.ReadFile(config.AllowlistPath())
		if err != nil {
			return fmt.Errorf("no allowlist at %s - run 'ptrbox install' first", config.AllowlistPath())
		}
	} else if err != nil {
		return err
	}
	// Strip comments and blanks: just the capabilities.
	for _, entry := range allowEntries(body) {
		fmt.Fprintln(env.Stdout, entry)
	}
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
