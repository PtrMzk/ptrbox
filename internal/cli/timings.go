package cli

// Where provisioning spent its minutes.
//
// Every script under vm/provision/ records `<script> <start> <end>` (epoch
// seconds) on exit - root's scripts under /var/lib/ptrbox, the agent user's
// under ~/.ptrbox - and `ptrbox new` reads both back for its summary. The point
// is a number per step rather than one figure for the whole boot: lima's log
// says how long the boot scripts took as a block, and the block is where the
// time goes.
//
// Diagnostic only. A guest with no record, or one that cannot be read, costs
// the summary a line and nothing else.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/backend"
)

// timingsScript prints both records. $HOME is expanded IN THE GUEST: the
// agent's home carries a version-dependent suffix and is never named from the
// host. A missing file is silent, and the trailing true keeps a missing pair
// from reading as a failed command.
const timingsScript = `cat /var/lib/ptrbox/timings "$HOME/.ptrbox/timings" 2>/dev/null; true`

// timing is one script's total, summed over however many times it ran.
type timing struct {
	name    string
	seconds int
}

// readTimings fetches and parses a VM's records; nil when there are none or
// the VM could not be asked.
func readTimings(env *Env, vm string) []timing {
	out, err := env.Backend.Output(vm, backend.Agent, "bash", "-c", timingsScript)
	if err != nil {
		return nil
	}
	return parseTimings(out)
}

// parseTimings sums the records by script name, in first-seen order. Anything
// that is not exactly `name start end` with numeric times is ignored rather
// than reported: the file is written by a shell trap in a guest, and a torn
// line is not worth a warning in a summary.
func parseTimings(body string) []timing {
	var out []timing
	index := map[string]int{}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		start, err1 := strconv.Atoi(fields[1])
		end, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || end < start {
			continue
		}
		name := fields[0]
		i, seen := index[name]
		if !seen {
			index[name] = len(out)
			out = append(out, timing{name: name})
			i = len(out) - 1
		}
		out[i].seconds += end - start
	}
	return out
}

// formatTimings renders one summary line: `10-base 1m16s, 30-toolchain 1m04s`.
// Empty for no records.
func formatTimings(timings []timing) string {
	parts := make([]string, 0, len(timings))
	for _, t := range timings {
		parts = append(parts, t.name+" "+seconds(t.seconds))
	}
	return strings.Join(parts, ", ")
}

// seconds is the narrator's duration shape - `12s`, `1m04s` - so the summary
// and the translated lima lines read the same.
func seconds(total int) string {
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	return fmt.Sprintf("%dm%02ds", total/60, total%60)
}
