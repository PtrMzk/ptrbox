// Package guestfake simulates the inside of ptrbox's VMs: each one's
// filesystem, and what happens when a command is run in it.
//
// It is the half of a fake backend that does not depend on the backend. How
// "run this in that VM" is spelled belongs to limactl or to whatever else
// builds the VMs, and each fake parses its own spelling; what the command
// then does to a guest is the same everywhere, and lives here once, so that
// two fakes cannot drift into simulating two different guests.
//
// The in-VM file operations (tee/cat/mv/rm/tail) act on a per-VM map, which is
// how the proxy VM's squid config, allowlist and access log are simulated
// well enough for the sync logic to be exercised end to end.
package guestfake

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
)

// Guest is every VM's insides, plus the log a fake backend records into.
// The zero value has no files in it.
type Guest struct {
	Log

	// Files is each VM's filesystem: Files[vm][path] = content. Paths are
	// absolute except for the agent's home, which is spelled `~/...`: the
	// real home carries a version-dependent suffix, so the host never names
	// it and neither does the fake.
	Files map[string]map[string]string

	// Transcripts is what a Claude transcript pull returns for a VM. Absent
	// means the VM has none, which is the state of one nobody has worked in.
	Transcripts map[string][]byte

	// Canned failures.
	VerifyFails      bool // `bash -lc <verify.sh>` reports a failed sandbox
	ProxyVerifyFails bool // `bash -lc <verify-proxy.sh>` reports dead egress
	SquidParseFails  bool // in-VM `squid -k parse` rejects the config

	// Sessions are the interactive shells opened, in order. ShellExit is the
	// status they end with: whatever ran last in a real one.
	Sessions  []Session
	ShellExit int
}

// Exec runs argv inside vm. It handles the shapes ptrbox uses:
//
//	sudo <cmd...>     proxy VM management (file ops + squid)
//	bash -lc <script> a verification run
//	bash -c <script>  the token injection, payload on stdin
//
// Which verification is which is decided by the VM: the proxy gets
// verify-proxy.sh, everything else gets verify.sh, and the two fail
// independently because an install and a sandbox creation can each go wrong
// while the other is fine.
func (g *Guest) Exec(vm string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	switch {
	case len(argv) > 0 && argv[0] == "sudo":
		return g.sudo(vm, argv[1:], stdin, stdout, stderr)

	case vm == config.ProxyVM && len(argv) > 1 && argv[1] == "-lc":
		if g.ProxyVerifyFails {
			fmt.Fprintln(stderr, "  squid listening        FAIL - nothing accepts connections on port 8888 in the VM")
			return errors.New("proxy verification failed")
		}
		return nil
	case len(argv) > 2 && argv[1] == "-c" && strings.Contains(argv[2], ".claude/projects"):
		// The transcript pull: a tar streamed out on stdout. No output at all
		// is how a VM says it has nothing to archive.
		if body := g.Transcripts[vm]; len(body) > 0 {
			stdout.Write(body)
		}
		return nil

	case len(argv) > 2 && argv[1] == "-c" && strings.Contains(argv[2], ".ptrbox/timings"):
		// The provisioning timing readback: both records concatenated on
		// stdout, an absent one contributing nothing - the real command is a
		// cat with its errors discarded, for the same reason.
		for _, path := range []string{"/var/lib/ptrbox/timings", "~/.ptrbox/timings"} {
			if body, ok := g.Files[vm][path]; ok {
				io.WriteString(stdout, body)
			}
		}
		return nil

	case len(argv) > 1 && argv[1] == "-c":
		// Token injection: the payload arrives on stdin and must never be in
		// argv, which is exactly what these tests exist to prove.
		if stdin != nil {
			body, err := io.ReadAll(stdin)
			if err != nil {
				return err
			}
			g.Stdins = append(g.Stdins, string(body))
		}
		return nil
	default:
		if g.VerifyFails {
			fmt.Fprintln(stderr, "  sudo removed          FAIL - the agent user still has root")
			return errors.New("verification failed")
		}
		return nil
	}
}

func (g *Guest) sudo(vm string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("guestfake: sudo with no command")
	}
	switch args[0] {
	case "tee":
		body, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		g.WriteFile(vm, args[1], string(body))
		return nil

	case "cat":
		body, ok := g.Files[vm][args[1]]
		if !ok {
			fmt.Fprintf(stderr, "cat: %s: No such file or directory\n", args[1])
			return errors.New("cat failed")
		}
		io.WriteString(stdout, body)
		return nil

	case "mv":
		body, ok := g.Files[vm][args[1]]
		if !ok {
			return fmt.Errorf("guestfake: mv: no such file: %s", args[1])
		}
		g.WriteFile(vm, args[2], body)
		delete(g.Files[vm], args[1])
		return nil

	case "rm":
		for _, path := range args[1:] {
			if !strings.HasPrefix(path, "-") {
				delete(g.Files[vm], path)
			}
		}
		return nil

	case "squid":
		// `squid -k parse`, `squid -f FILE -k parse`, `squid -k reconfigure`
		if g.SquidParseFails && contains(args, "parse") {
			fmt.Fprintln(stderr, "squid: FATAL: Bungled config")
			return errors.New("squid parse failed")
		}
		return nil

	case "mkdir":
		// -p only; the fake filesystem is flat, so the directory is implicit.
		return nil

	case "systemctl":
		return nil

	case "tail":
		return g.tail(vm, args[1:], stdout, stderr)
	}
	return fmt.Errorf("guestfake: unhandled sudo command: %s", args[0])
}

// tail handles `tail -n N [-f] <path>`. -f is not simulated: a fake that
// blocked would hang the suite, and emitting the tail once is enough to test
// the plumbing.
func (g *Guest) tail(vm string, args []string, stdout, stderr io.Writer) error {
	n, path := 10, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-n":
			i++
			value := ""
			if i < len(args) {
				value = args[i]
			}
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("guestfake: tail -n %q", value)
			}
			n = parsed
		case "-f":
		default:
			path = args[i]
		}
	}
	body, ok := g.Files[vm][path]
	if !ok {
		fmt.Fprintf(stderr, "tail: cannot open '%s'\n", path)
		return errors.New("tail failed")
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if body == "" {
		return nil
	}
	fmt.Fprintln(stdout, strings.Join(lines, "\n"))
	return nil
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// WriteFile places content in a VM's filesystem.
func (g *Guest) WriteFile(vm, path, content string) {
	if g.Files == nil {
		g.Files = map[string]map[string]string{}
	}
	if g.Files[vm] == nil {
		g.Files[vm] = map[string]string{}
	}
	g.Files[vm][path] = content
}

// ReadFile returns a file from a VM's filesystem.
func (g *Guest) ReadFile(vm, path string) (string, bool) {
	body, ok := g.Files[vm][path]
	return body, ok
}

// Session is one interactive shell somebody opened in a VM.
type Session struct {
	VM      string
	Workdir string
	// Typed is what arrived on the session's stdin before it closed.
	Typed string
}

// ExitError is a shell that ended with a non-zero status. It answers
// ExitCode() the way *exec.ExitError does, which is all a caller may ask of
// either.
type ExitError int

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e ExitError) ExitCode() int { return int(e) }

// Interactive simulates a person's shell session: it records where the shell
// was opened and what was typed into it, writes a prompt so that a test can
// see the session's stdout is the caller's, and ends with ShellExit.
func (g *Guest) Interactive(vm, workdir string, stdin io.Reader, stdout io.Writer) error {
	session := Session{VM: vm, Workdir: workdir}
	if stdin != nil {
		typed, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		session.Typed = string(typed)
	}
	g.Sessions = append(g.Sessions, session)
	if stdout != nil {
		fmt.Fprintf(stdout, "[%s] %s $ \n", vm, workdir)
	}
	if g.ShellExit != 0 {
		return ExitError(g.ShellExit)
	}
	return nil
}
