package proxy

// A VM's allowlist follows its config across a re-create.
//
// Item 42 seeded a VM's list once, filtered by the features it had at that
// moment, and then never touched it: "from the moment the file is written it
// is yours". Correct for the lines a person adds, and wrong for the groups
// the template put there - a sandbox created with node, deleted, and
// re-created with node and uv came back with no PyPI, because the file
// outlived the VM and nothing re-read it. The features decide what the guest
// holds; the allowlist has to agree with them, on every create.
//
// So the group markers now survive into a VM's list, and `ptrbox new` reads
// them back against that VM's resolved config before the list is pushed:
//
//   - a group whose feature is wanted and that holds no entries (left out at
//     seed time, or emptied by an earlier re-create) is restored from the
//     template;
//   - a group whose feature is NOT wanted loses the template's entries, and
//     says so with the same note the seed writes;
//   - a template group the file does not have at all is appended when its
//     feature is wanted - which is how a list seeded before the markers
//     existed catches up.
//
// What is deliberately left alone: entries a person added, wherever they put
// them; entries a person pruned from a group that is still enabled (an
// enabled group is only ever restored when it is EMPTY, so pruning astral.sh
// from the uv group holds); and anything outside a marked group. The
// template is the authority for what a feature grants, the person is the
// authority for everything else, and the plan output names every change.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
)

// allowGroup is one `# @requires` group of the template.
type allowGroup struct {
	features []string // what the marker names, in its order
	lines    []string // the template's lines inside the group, verbatim
	domains  []string // the first field of each entry line
}

// label is the marker argument as it is spelled in a list.
func (g allowGroup) label() string { return strings.Join(g.features, " ") }

// AllowlistChange is one thing reconciling did, or could not do.
type AllowlistChange struct {
	Text string
	Warn bool
}

// ReconcileVMAllowlist brings a VM's existing list into line with that VM's
// resolved config, returning what changed. A VM with no list yet is nothing
// to reconcile: the seed handles it. The template is validated first, so a
// marker naming something that is not a feature fails here the way it fails
// the seed.
func (p *Proxy) ReconcileVMAllowlist(name string) ([]AllowlistChange, error) {
	path := config.VMAllowlistPath(name)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	template, err := os.ReadFile(config.AllowlistPath())
	if err != nil {
		return nil, fmt.Errorf("no allowlist at %s - run 'ptrbox install' first", config.AllowlistPath())
	}
	cfg, err := p.Cfg.Overlay(name)
	if err != nil {
		return nil, fmt.Errorf("reconciling the allowlist for %q: %w", name, err)
	}
	if _, err := SeedFor(template, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", config.AllowlistPath(), err)
	}
	groups, err := templateGroups(template)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.AllowlistPath(), err)
	}

	updated, changes := reconcile(string(body), groups, cfg)
	if updated == string(body) {
		return changes, nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	return changes, nil
}

// templateGroups reads the marked groups out of the template. Structure only
// - SeedFor is what validates the feature names.
func templateGroups(template []byte) ([]allowGroup, error) {
	var groups []allowGroup
	var current *allowGroup
	scanner := bufio.NewScanner(bytes.NewReader(template))
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		switch directive, argument := parseMarker(text); directive {
		case requiresMarker:
			if current != nil {
				return nil, fmt.Errorf("line %d: groups do not nest", line)
			}
			current = &allowGroup{features: strings.Fields(argument)}
		case endMarker:
			if current == nil {
				return nil, fmt.Errorf("line %d: %s with no %s above it", line, endMarker, requiresMarker)
			}
			groups = append(groups, *current)
			current = nil
		default:
			if current != nil {
				current.lines = append(current.lines, text)
				if domain := domainOf(text); domain != "" {
					current.domains = append(current.domains, domain)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if current != nil {
		return nil, fmt.Errorf("the %q group is never closed with %s", current.label(), endMarker)
	}
	return groups, nil
}

// domainOf is the entry on a line, or "" for a comment or a blank.
func domainOf(line string) string {
	text := strings.TrimSpace(line)
	if text == "" || strings.HasPrefix(text, "#") {
		return ""
	}
	return strings.Fields(text)[0]
}

// omittedNote is the line a group holds instead of its entries when the VM
// does not have the feature. The seed writes it; reconciling writes and
// removes it.
func omittedNote(features []string) string {
	return fmt.Sprintf("# (omitted: this VM has no %s)", strings.Join(features, " or "))
}

func isNote(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "# (omitted:") ||
		strings.HasPrefix(strings.TrimSpace(line), "# (added:")
}

// reconcile is the pure half: the list as it should read for cfg, and what
// was done to get there.
func reconcile(body string, groups []allowGroup, cfg *config.Config) (string, []AllowlistChange) {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var changes []AllowlistChange
	say := func(format string, args ...any) {
		changes = append(changes, AllowlistChange{Text: fmt.Sprintf(format, args...)})
	}

	for _, g := range groups {
		wanted := slices.ContainsFunc(g.features, cfg.Wants)
		start, end := findGroup(lines, g.label())
		switch {
		case start >= 0 && end < 0:
			changes = append(changes, AllowlistChange{Warn: true, Text: fmt.Sprintf(
				"allowlist: the %s group has no %s line, so it was left as it is", g.label(), endMarker)})
			continue

		case start < 0:
			// No such group in the file: an older list, or one somebody
			// flattened. Added only when wanted, and only the entries the
			// file does not already hold somewhere.
			if !wanted {
				continue
			}
			var missing []string
			for _, line := range g.lines {
				if d := domainOf(line); d == "" || !hasDomain(lines, d) {
					missing = append(missing, line)
				}
			}
			if !slices.ContainsFunc(missing, func(l string) bool { return domainOf(l) != "" }) {
				continue
			}
			lines = append(lines, "", "# "+requiresMarker+" "+g.label(),
				fmt.Sprintf("# (added: this VM now has %s)", strings.Join(g.features, " or ")))
			lines = append(lines, missing...)
			lines = append(lines, "# "+endMarker)
			say("allowlist: added the %s group (%s)", g.label(), strings.Join(entryDomains(missing), ", "))

		default:
			inside := lines[start+1 : end]
			entries := entryDomains(inside)
			switch {
			case wanted && len(entries) == 0:
				// Left out, or emptied by an earlier re-create: restore the
				// template's group, keeping any comment of the person's.
				var kept []string
				for _, line := range inside {
					if !isNote(line) {
						kept = append(kept, line)
					}
				}
				replacement := append(kept, g.lines...)
				lines = splice(lines, start+1, end, replacement)
				say("allowlist: restored the %s group (%s)", g.label(), strings.Join(g.domains, ", "))

			case !wanted && len(entries) > 0:
				// The template's entries go; anything else in the group stays.
				var kept, removed []string
				for _, line := range inside {
					if d := domainOf(line); d != "" && slices.Contains(g.domains, d) {
						removed = append(removed, d)
						continue
					}
					kept = append(kept, line)
				}
				if len(removed) == 0 {
					continue
				}
				if len(entryDomains(kept)) == 0 && !slices.ContainsFunc(kept, isNote) {
					kept = append([]string{omittedNote(g.features)}, kept...)
				}
				lines = splice(lines, start+1, end, kept)
				say("allowlist: removed %s from the %s group - this VM has no %s",
					strings.Join(removed, ", "), g.label(), strings.Join(g.features, " or "))
			}
		}
	}
	return strings.Join(lines, "\n") + "\n", changes
}

// findGroup locates a group by its marker argument: the marker line and the
// matching @end, or -1 for each that is missing.
func findGroup(lines []string, label string) (start, end int) {
	start, end = -1, -1
	for i, line := range lines {
		directive, argument := parseMarker(line)
		if start < 0 {
			if directive == requiresMarker && strings.Join(strings.Fields(argument), " ") == label {
				start = i
			}
			continue
		}
		if directive == endMarker {
			return start, i
		}
		if directive == requiresMarker {
			return start, -1
		}
	}
	return start, end
}

func entryDomains(lines []string) []string {
	var out []string
	for _, line := range lines {
		if d := domainOf(line); d != "" {
			out = append(out, d)
		}
	}
	return out
}

func hasDomain(lines []string, domain string) bool {
	return slices.Contains(entryDomains(lines), domain)
}

// splice replaces lines[from:to] with replacement.
func splice(lines []string, from, to int, replacement []string) []string {
	out := make([]string, 0, len(lines)-(to-from)+len(replacement))
	out = append(out, lines[:from]...)
	out = append(out, replacement...)
	return append(out, lines[to:]...)
}
