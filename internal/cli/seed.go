package cli

// cmd_install's config seeding: leave ~/.config/ptrbox/ in a state you can
// open an editor on.
//
// An empty directory is not "set up". A user who has to be told to mkdir a
// directory and copy an example into it before they can change a setting is
// being asked to do install's job by hand, from a doc, with a shell command
// that has no test anywhere. So install writes the annotated config file and
// creates the per-VM directory, and everything after it is editing rather
// than assembling.
//
// The rule throughout, taken from proxy.SeedAllowlist: create what is absent,
// never touch what is present, say what was written and record it in the
// manifest. Re-running install must be a no-op, because it is idempotent by
// contract and people re-run it after every rebuild.
//
// "Never touch what is present" is right about not clobbering settings and
// wrong about the case that actually happens: the person re-running install is
// usually the one who upgraded ptrbox, and their config was seeded by an older
// build. So a file that exists AND differs from the shipped one is offered
// rather than skipped - the current version kept as a timestamped .bak, the
// path said out loud, and nothing written without an explicit yes. An
// identical file stays silent, which is what keeps the no-op a no-op.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
)

// vmsREADME explains the per-VM directory to whoever opens it. The file name
// carries a dot deliberately: config.VMName strips everything outside
// [a-z0-9-], so no VM's config lookup can ever land on a name containing one,
// and this file can never be read as a per-VM config. It was "README" first,
// unreachable-by-capitals - which held on Linux and not on the Mac, where the
// filesystem case-folds and vms/readme resolved to vms/README. The charset
// argument survives case folding; the case argument was the case argument.
const vmsREADME = `Per-VM configuration. One optional file per sandbox, named for its VM - the
name ptrbox new prints and ssh lima-<name> uses.

Same format as ../config, which lists every key and marks the ones legal here
with [vm]. State only what differs; the rest falls through to ../config.

    $ cat > thesis <<'EOF'
    PTRBOX_EXTRA_PACKAGES="texlive-latex-recommended latexmk"
    PTRBOX_MEMORY=4GiB
    EOF
    $ ptrbox rm thesis && ptrbox new thesis

Read once, at create time: editing a file here does nothing until you
re-create that sandbox. Keep it when the VM goes away - it is what makes the
next ptrbox new reproduce the same sandbox.

This file is not a config file and is never read as one.
`

// seedConfigDir makes ~/.config/ptrbox/ ready to edit: the directory, the
// per-VM directory, and the annotated config file if there is not one already.
// It reports what it created, so install's summary can point at it.
func seedConfigDir(env *Env, update bool) error {
	for _, dir := range []string{config.Dir(), config.VMDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := seedFile(env, config.Path(), "settings file", update, true, func() ([]byte, error) {
		return fs.ReadFile(env.Assets, "config/ptrbox.conf.example")
	}); err != nil {
		return err
	}
	return seedFile(env, filepath.Join(config.VMDir(), "README.txt"), "per-VM README", update, false,
		func() ([]byte, error) { return []byte(vmsREADME), nil })
}

// seedFile writes path from body if nothing is there yet, and offers to bring
// it up to date if what is there differs. what names the file in the offer:
// "replace your settings file?" and "replace your allowlist template?" are
// different questions, and only one of them can lose something you typed.
func seedFile(env *Env, path, what string, update, merge bool, body func() ([]byte, error)) error {
	content, err := body()
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	switch {
	case err == nil:
		if bytes.Equal(current, content) {
			return nil // the no-op case, and it stays silent
		}
		return offerUpdate(env, path, what, current, content, update, merge)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return err
	}
	env.Out.Say("installed %s", path)
	return config.RecordManifest("wrote " + path)
}

// offerUpdate handles a seeded file that exists and differs from the version
// this binary carries.
//
// Three answers, in the order they are asked: --update was given, so go ahead;
// there is someone to ask, so ask; or there is not, in which case say what is
// available and change nothing. A scripted install must never rewrite
// settings, which is why --update is its own flag rather than something -y
// implies - "yes to every prompt" is about the things install was going to do
// anyway.
//
// merge says whether this file's shape lets a user's choices be carried into
// the new version. True for the settings file, which is KEY=value and where
// the only lines that are the user's are the assignments. False for the
// allowlist template and the README, which are prose and lists with no such
// separation - there, replacing is the whole operation.
func offerUpdate(env *Env, path, what string, current, shipped []byte, update, merge bool) error {
	env.Out.Say("your %s differs from the one this ptrbox ships: %s", what, path)

	switch {
	case update:
		// --update: asked for on the command line, which is the answer.
	case env.Interactive && !env.NoInput:
		question := fmt.Sprintf("replace it with the shipped %s? (yours is kept as a .bak)", what)
		if merge {
			question = fmt.Sprintf("update the shipped %s, keeping your settings? (a .bak is kept either way)", what)
		}
		if !confirm(env, question) {
			env.Out.Say("keeping yours")
			return nil
		}
	default:
		env.Out.Detail("keeping yours; `ptrbox install --update` updates it, with a backup")
		return nil
	}

	// What actually gets written. A mergeable file keeps the user's active
	// assignments at their key's place in the new text; anything else is
	// replaced outright.
	next, note := shipped, "replaced"
	if merge {
		if result, ok := mergeSettings(current, shipped); ok {
			next = result.merged
			note = "updated"
			env.Out.Detail("%s", describeMerge(result))
			if len(result.orphans) > 0 {
				// Loud, because a setting with no home is the one thing an
				// upgrade can silently take away.
				env.Out.Warn("no current setting matches: %s", strings.Join(result.orphans, " "))
				env.Out.Detail("kept, commented out, at the end of the file")
			}
		} else {
			// Only reachable if the file stopped parsing since install loaded
			// it. Say so rather than rewriting something unreadable.
			env.Out.Warn("could not read your settings to carry them over; replacing instead")
		}
	}

	// The backup is written before the change and named in the output: this is
	// the one place install can cost someone settings they typed, and an undo
	// they have to go looking for is not an undo.
	backup := path + ".bak-" + env.now().Format("20060102-150405")
	if err := os.WriteFile(backup, current, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(path, next, 0o644); err != nil {
		return err
	}
	env.Out.Say("%s %s; your version is at %s", note, path, backup)
	return config.RecordManifest(note + " " + path + " (backup at " + backup + ")")
}
