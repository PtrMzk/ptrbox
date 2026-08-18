package cli

// cmd_save - pull a VM's Claude transcripts onto the host.
//
// A sandbox is disposable, but the conversation that happened in it usually
// is not, and `ptrbox rm` would otherwise take it with the disk. So the
// transcripts come out: on demand here, and automatically before rm destroys
// anything.
//
// Deliberately NOT a second mount. /workspace being the only host directory a
// VM can see is the core security decision of the design, and a writable host
// path for logs would be a second channel where agent-authored content lands
// on the Mac - including filenames and symlinks of the agent's choosing.
// Instead the archive is pulled: host-initiated, one-directional, over the
// same `limactl shell` pipe the token and the squid config already use.
//
// Two properties follow from that and are what make it safe:
//
//   - The archive is stored, never extracted. Nothing is written to a path
//     the guest named, so traversal and symlink tricks have nowhere to land.
//     `tar tzf` is how you look inside.
//   - The host filename is ptrbox's, built from the VM name and a timestamp.
//
// A transcript records everything the agent was shown - file contents,
// command output, anything it was handed. Pulling it here persists in
// plaintext what was previously confined to a disposable box, which is why
// the directory and the files are 0700/0600.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
)

// archiveScript streams a gzipped tar of the transcripts to stdout, or
// nothing at all if the VM has none.
//
// ~/.claude/projects is where Claude Code keeps them today. That layout is an
// undocumented internal - the supported handle is the transcript_path the
// statusline receives - so this archives the directory wholesale rather than
// assuming anything about what is in it, and drift shows up as an empty
// archive rather than a wrong one.
const archiveScript = `set -eu
cd "$HOME"
[ -d .claude/projects ] || exit 0
tar czf - .claude/projects`

// maxArchive caps what a VM can write to the host in one pull. An agent that
// filled its disk with transcripts should not be able to fill yours.
const maxArchive = 256 << 20 // 256 MiB

const saveHelp = `ptrbox save - archive a VM's Claude transcripts onto the host

  ptrbox save <repo-path | vm-name>

Writes a .tar.gz under ~/.local/state/ptrbox/transcripts/. The archive is
stored, never extracted; read it with tar tzf.

ptrbox rm does this automatically before destroying a VM.
`

func cmdSave(env *Env, args []string) error {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprint(env.Stdout, saveHelp)
			return nil
		}
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("save: unknown option %q", arg)
		}
	}

	name, err := sandboxTarget(env, args, "save",
		fmt.Sprintf("%q runs squid, not Claude - it has no transcripts", config.ProxyVM))
	if err != nil {
		return err
	}
	if !env.Lima.Exists(name) {
		return unknownVM(env, name)
	}
	if !env.Lima.Running(name) {
		return fmt.Errorf("VM %q is not running - start it first: ptrbox start %s", name, name)
	}

	path, err := archiveTranscripts(env, name)
	if err != nil {
		return err
	}
	if path == "" {
		env.Out.Say("VM %q has no Claude transcripts yet", name)
		return nil
	}
	fmt.Fprintln(env.Stdout, path)
	return nil
}

// archiveTranscripts pulls the transcripts out of a running VM and returns the
// archive path, or "" if the VM had none.
func archiveTranscripts(env *Env, name string) (string, error) {
	dir := config.TranscriptDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.tar.gz", name, env.now().Format("20060102-150405")))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	capped := &capWriter{W: file, Remaining: maxArchive}
	streamErr := env.Lima.Stream(capped, lima.ShellArgs(name, "bash", "-c", archiveScript)...)
	closeErr := file.Close()

	switch {
	case streamErr != nil:
		os.Remove(path)
		return "", fmt.Errorf("archiving transcripts from %q: %w", name, streamErr)
	case closeErr != nil:
		os.Remove(path)
		return "", closeErr
	case capped.Written == 0:
		// The VM has no transcripts. An empty file would be a worse answer
		// than none at all.
		os.Remove(path)
		return "", nil
	}

	env.Out.Say("archived %d bytes of transcripts to %s", capped.Written, path)
	return path, nil
}

// capWriter refuses to write more than Remaining bytes. The bytes come from a
// sandbox, so the host's disk is not the sandbox's to spend.
type capWriter struct {
	W         io.Writer
	Remaining int64
	Written   int64
}

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.Remaining {
		return 0, errors.New("the transcript archive is larger than ptrbox will copy out (256 MiB)")
	}
	n, err := c.W.Write(p)
	c.Remaining -= int64(n)
	c.Written += int64(n)
	return n, err
}
