package cli

// cmd_logs - read the proxy log, from inside the proxy VM.
//
// This is the debugging loop for "my build can't reach X": the request shows
// up as TCP_DENIED, `ptrbox allow` grants the domain, and squid reloads
// without dropping anything.
//
// Squid lives in the ptrbox-proxy VM, so every read goes through
// `limactl shell` - which also means the proxy has to be running, and when it
// is not, that IS the answer to "why does my sandbox have no network".
//
// Following is opt-in (-f) rather than the default, so the command is
// predictable in scripts and testable.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
)

// deniedMarker is what squid logs for a blocked request.
const deniedMarker = "TCP_DENIED"

const logsHelp = `ptrbox logs - read the egress proxy log

      --denied   only blocked requests (TCP_DENIED)
  -f, --follow   keep printing as new requests arrive
  -n <count>     how many lines to start from (default 50)
`

func cmdLogs(env *Env, args []string) error {
	denied, follow, lines := false, false, "50"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--denied":
			denied = true
		case "-f", "--follow":
			follow = true
		case "-n":
			i++
			if i >= len(args) {
				return errors.New("logs: -n needs a line count")
			}
			lines = args[i]
			if _, err := strconv.Atoi(lines); err != nil || strings.HasPrefix(lines, "-") {
				return fmt.Errorf("logs: -n must be a number, got %q", lines)
			}
		case "-h", "--help":
			fmt.Fprint(env.Stdout, logsHelp)
			return nil
		default:
			return fmt.Errorf("logs: unknown option %q", args[i])
		}
	}

	if err := requireLima(env); err != nil {
		return err
	}
	if !env.Proxy.Running() {
		return errors.New("the proxy VM is not running (no proxy, no VM egress) - 'ptrbox start <vm>' brings it up")
	}

	tail := []string{"sudo", "tail", "-n", lines}
	if follow {
		tail = append(tail, "-f")
	}
	tail = append(tail, config.SquidLog)
	argv := lima.ShellArgs(config.ProxyVM, tail...)

	if follow {
		// Streamed rather than buffered: the whole point of -f is seeing
		// requests as they arrive. Filtering happens line by line here, which
		// is what `grep --line-buffered` was doing on the other side of the
		// pipe before.
		out := io.Writer(env.Stdout)
		if denied {
			filter := &lineFilter{W: env.Stdout, Match: deniedMarker}
			defer filter.Flush()
			out = filter
		}
		return env.Lima.Stream(out, argv...)
	}

	var buf bytes.Buffer
	if err := env.Lima.Stream(&buf, argv...); err != nil {
		return fmt.Errorf("no proxy log at %s in the proxy VM - has any request been made?", config.SquidLog)
	}

	out := buf.String()
	if denied {
		// No denials is a good outcome, not an error.
		var kept []string
		for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			if strings.Contains(line, deniedMarker) {
				kept = append(kept, line)
			}
		}
		out = strings.Join(kept, "\n")
		if out != "" {
			out += "\n"
		}
	}

	if strings.TrimSpace(out) == "" {
		env.Out.Say("no matching lines in %s", config.SquidLog)
		return nil
	}
	fmt.Fprint(env.Stdout, out)

	if denied {
		env.Out.Say("to allow one of these: ptrbox allow <domain>")
		env.Out.Say("(reloads without dropping tunnels)")
	}
	return nil
}

// lineFilter passes through only the lines containing Match, holding back a
// partial trailing line until the rest of it arrives.
type lineFilter struct {
	W       io.Writer
	Match   string
	pending []byte
}

func (f *lineFilter) Write(p []byte) (int, error) {
	f.pending = append(f.pending, p...)
	for {
		i := bytes.IndexByte(f.pending, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := f.pending[:i+1]
		f.pending = f.pending[i+1:]
		if bytes.Contains(line, []byte(f.Match)) {
			if _, err := f.W.Write(line); err != nil {
				return len(p), err
			}
		}
	}
}

// Flush emits a final unterminated line, if the stream ended mid-line.
func (f *lineFilter) Flush() {
	if len(f.pending) > 0 && bytes.Contains(f.pending, []byte(f.Match)) {
		f.W.Write(f.pending)
	}
	f.pending = nil
}
