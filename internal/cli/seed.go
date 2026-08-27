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

import (
	"io/fs"
	"os"
	"path/filepath"

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
func seedConfigDir(env *Env) error {
	for _, dir := range []string{config.Dir(), config.VMDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := seedFile(env, config.Path(), func() ([]byte, error) {
		return fs.ReadFile(env.Assets, "config/ptrbox.conf.example")
	}); err != nil {
		return err
	}
	return seedFile(env, filepath.Join(config.VMDir(), "README.txt"), func() ([]byte, error) {
		return []byte(vmsREADME), nil
	})
}

// seedFile writes path from body if nothing is there yet. An existing file is
// left exactly as it is - it is the user's, and install is not a command that
// edits your settings.
func seedFile(env *Env, path string, body func() ([]byte, error)) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	content, err := body()
	if err != nil {
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
