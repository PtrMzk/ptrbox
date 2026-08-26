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

// vmsREADME explains the per-VM directory to whoever opens it. Named in
// capitals deliberately: config.VMName lowercases, so no VM can ever be called
// "README" and this file can never be read as a per-VM config. A lowercase
// name here would be a config file for a sandbox called readme.
const vmsREADME = `Per-VM configuration overrides.

One optional file per sandbox, named for its VM - the name ptrbox new prints
and ssh lima-<name> uses. Same format as ../config, and sparse in the same
way: it states only what differs for that sandbox, and every key it does not
mention falls through to ../config, and then to the built-in defaults.

    $ cat > thesis <<'EOF'
    PTRBOX_EXTRA_PACKAGES="texlive-latex-recommended texlive-latex-extra latexmk"
    PTRBOX_MEMORY=4GiB
    EOF
    $ ptrbox rm thesis && ptrbox new thesis

Read once, at create time, and baked into the VM - so editing a file here does
nothing until you re-create that sandbox. Keep the file when the VM goes away:
it is what makes the next ptrbox new reproduce the same sandbox.

Only settings a VM owns may appear: sizing, port range, distro, image, extra
packages, Claude model, git identity. The rest describe your Mac - one proxy,
one Keychain, one repo root - and ptrbox refuses the file rather than ignoring
the line. ../config lists every key, with the per-VM ones marked.

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
	return seedFile(env, filepath.Join(config.VMDir(), "README"), func() ([]byte, error) {
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
