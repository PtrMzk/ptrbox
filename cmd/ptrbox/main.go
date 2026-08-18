// Command ptrbox creates and manages sandboxed Claude Code VMs, one per repo.
//
// This binary runs on the HOST with your privileges. It is deliberately never
// provisioned into a sandbox VM - an agent that can edit its own sandbox's
// provisioning code is not sandboxed.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/cli"
	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/proxy"
	"github.com/PtrMzk/ptrbox/internal/ui"
)

func main() {
	args, color := colorFlag(os.Args[1:])
	out := ui.Printer{W: os.Stderr, Color: color}

	env := &cli.Env{
		Assets:      ptrbox.Assets,
		Out:         out,
		Stdout:      os.Stdout,
		Stdin:       os.Stdin,
		Keychain:    cli.SecurityKeychain{},
		Exe:         executable(),
		Interactive: interactive(),
		Editor:      cli.DefaultEditor,
		Now:         time.Now,
		Lima:        &lima.Client{Runner: lima.Exec{}, Stdout: os.Stdout, Stderr: os.Stderr},
		Load:        load,
	}

	err := cli.Run(env, args)
	switch {
	case err == nil:
		return
	case errors.Is(err, cli.ErrUsage):
		// Usage was already printed; the status is what distinguishes "you
		// typed something that is not a command" from "the command failed".
		os.Exit(2)
	case errors.Is(err, cli.ErrReported):
		os.Exit(1)
	default:
		out.Fail("%v", err)
		os.Exit(1)
	}
}

// colorFlag decides whether ptrbox may style its output, and removes
// --no-color from the arguments so no subcommand has to know about it.
//
// The decision is made here and passed down as one bool, because a terminal
// is a property of the process rather than of a message. stderr is what is
// tested: that is where every informational line goes, so a run whose stdout
// is piped into a file still gets its progress coloured.
func colorFlag(args []string) ([]string, bool) {
	kept := make([]string, 0, len(args))
	asked := true
	for _, arg := range args {
		if arg == "--no-color" {
			asked = false
			continue
		}
		kept = append(kept, arg)
	}
	if !asked || os.Getenv("NO_COLOR") != "" {
		return kept, false
	}
	// An unset TERM is the same answer as "dumb": something is running ptrbox
	// that never said it could render anything.
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		return kept, false
	}
	info, err := os.Stderr.Stat()
	if err != nil {
		return kept, false
	}
	return kept, info.Mode()&os.ModeCharDevice != 0
}

// load resolves the configuration and everything that depends on it. Called
// only for commands that need it, so a config file that does not parse cannot
// stop `ptrbox help` from explaining itself.
func load(env *cli.Env) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, warning := range cfg.Warnings {
		env.Out.Warn("%s", warning)
	}
	env.Cfg = cfg
	env.Proxy = &proxy.Proxy{Cfg: cfg, Lima: env.Lima, Assets: env.Assets, Out: env.Out}
	return nil
}

// executable is the path to this binary, with symlinks resolved, so
// `ptrbox install` can tell an existing link to itself from somebody else's
// ptrbox.
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// interactive reports whether there is a terminal to ask a question on.
func interactive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
