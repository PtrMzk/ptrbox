package cli

// Carrying a user's settings across a config-file update.
//
// `ptrbox install --update` used to replace the settings file wholesale,
// keeping a .bak. That is right for the annotated prose - which is the whole
// point of updating - and wrong for the handful of lines the user actually
// wrote, which are the only part of the file that is theirs. Item 46 said as
// much when it argued that "replace your config?" and "replace your allowlist
// template?" are different questions, then answered both the same way.
//
// So: take the new file, and put each active assignment from the old one back
// where that key's commented default sits. The result is the current wording,
// the current key list, and the user's choices - and because the line is
// copied VERBATIM rather than parsed and reserialised, `"$HOME/code"` stays
// written that way instead of being frozen into the path it expands to today.
//
// A key with nowhere to go - retired, renamed, or a scratch variable - is not
// dropped. It is appended under a header that says what happened, because the
// alternative is a setting disappearing during an upgrade, which is the exact
// failure this whole file exists to prevent.

import (
	"fmt"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
)

// mergeResult is what a merge did, so the caller can report it. Everything
// here is for the user's benefit: an update that silently rearranged their
// settings would be no better than one that silently discarded them.
type mergeResult struct {
	merged  []byte
	carried []string // keys placed at their key's line in the new file
	orphans []string // keys the new file has no home for
}

// mergeSettings puts the active assignments from current into shipped.
//
// ok is false when current holds a line that is not an assignment, a comment
// or blank. That should be impossible - install has already loaded this file,
// and a file that does not parse fails the load - but a merge is a rewrite of
// something the user owns, so it declines to guess and lets the caller fall
// back to the plain replace-with-backup.
func mergeSettings(current, shipped []byte) (mergeResult, bool) {
	// Last assignment wins, the way it would if the file were still sourced.
	// Order is remembered separately so the report reads in file order.
	values := map[string]string{}
	var order []string
	for _, line := range strings.Split(string(current), "\n") {
		name, isAssignment, err := config.AssignmentName(line)
		if err != nil {
			return mergeResult{}, false
		}
		if !isAssignment {
			continue
		}
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = line
	}

	var out []string
	placed := map[string]bool{}
	for _, line := range strings.Split(string(shipped), "\n") {
		// The commented default for a key looks like `#PTRBOX_CPUS=4`. The `=`
		// is load-bearing: without it `#PTRBOX_NODE=` would also match the
		// line for PTRBOX_NODE_VERSION.
		replaced := false
		if after, isComment := strings.CutPrefix(strings.TrimLeft(line, " \t"), "#"); isComment {
			if name, _, err := config.AssignmentName(after); err == nil {
				if userLine, wanted := values[name]; wanted && !placed[name] {
					out = append(out, userLine)
					placed[name] = true
					replaced = true
				}
			}
		}
		if !replaced {
			out = append(out, line)
		}
	}

	result := mergeResult{}
	for _, name := range order {
		if placed[name] {
			result.carried = append(result.carried, name)
		} else {
			result.orphans = append(result.orphans, name)
		}
	}

	if len(result.orphans) > 0 {
		// Kept, commented, and explained. Uncommented they might not parse
		// against the current key list; deleted they would be gone.
		out = append(out, "", carriedHeader)
		for _, name := range result.orphans {
			out = append(out, "#"+strings.TrimLeft(values[name], " \t"))
		}
	}

	result.merged = []byte(strings.Join(out, "\n"))
	return result, true
}

const carriedHeader = `# --- carried over from your previous settings file ---
# These were set in the file this replaced, and the current one has no such
# key. A key may have been renamed or retired, or these may be scratch
# variables. They are commented out so this file still parses; move what you
# still want to the matching setting above and delete the rest.`

// describeMerge is the one-line summary of what moved, for install's output.
func describeMerge(r mergeResult) string {
	if len(r.carried) == 0 {
		return "you had set nothing in it"
	}
	return fmt.Sprintf("carried over %s", strings.Join(r.carried, " "))
}
