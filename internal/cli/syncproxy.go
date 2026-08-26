package cli

// cmd_sync-proxy - make the proxy match the host-side files.
//
// The files under ~/.config/ptrbox/ are the desired state and Sync is the
// one reconciliation mechanism; every command that changes the files or
// starts the proxy already calls it. This is the standalone verb for the
// path none of them cover: files edited by hand - a VM's allowlist in vim, a
// dotfiles checkout freshly synced - that should apply now rather than on
// the next `ptrbox start`.

import (
	"errors"
	"fmt"

	"github.com/PtrMzk/ptrbox/internal/proxy"
)

const syncProxyHelp = `ptrbox sync-proxy - push hand-edited allowlists to the proxy

Reconciles the proxy VM against the host-side files (the per-VM allowlists
under ~/.config/ptrbox/allowed_domains.d/, the template, the squid config)
and reports what happened. The commands that change those files apply them
themselves; this is for edits made by hand.
`

func cmdSyncProxy(env *Env, args []string) error {
	for _, arg := range args {
		switch arg {
		case "-h", "--help":
			fmt.Fprint(env.Stdout, syncProxyHelp)
			return nil
		default:
			return fmt.Errorf("sync-proxy: unknown option %q", arg)
		}
	}
	if err := requireLima(env); err != nil {
		return err
	}

	if !env.Proxy.Running() {
		env.Out.Say("the proxy VM is not running; the files are pushed when it next starts")
		return nil
	}

	result, err := env.Proxy.Sync()
	if err != nil {
		return err
	}
	switch result {
	case proxy.Unchanged:
		env.Out.Say("nothing to push - the proxy already serves what the host files say")
	case proxy.Applied:
		env.Out.Say("changes applied to the proxy")
	case proxy.Rejected:
		// Sync already printed squid's parse error and rolled the VM back.
		// The hand-edited host file is left exactly as the user wrote it:
		// unlike `ptrbox allow`, this command did not author the change, so
		// it does not un-author it either.
		return errors.New("squid rejected the host-side files; the proxy VM was rolled back and your files are untouched - fix them and re-run ptrbox sync-proxy")
	}
	return nil
}
