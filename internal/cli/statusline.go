package cli

// The Claude Code statusLine, carried into each sandbox.
//
// The script comes from a host-side path (PTRBOX_STATUSLINE), never from the
// repo mount - same rule as the extra package list, and for a sharper reason:
// a statusLine is a command Claude Code executes on every render, so one read
// from /workspace would be arbitrary code the agent could hand itself.
//
// It is pushed over stdin after verification, like the token, rather than
// rendered into the generated config. That keeps an arbitrary user script away
// from the renderer entirely - no placeholder collisions, no YAML indentation,
// no heredoc terminators to collide with - and off disk in ~/.lima/_generated.
//
// It lands in the guest's home, which is VM disk rather than a mount, so it
// survives reboots and stop/start. Re-reading a changed host script means
// re-creating the VM (or repeating the push by hand).

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/lima"
)

// guestStatuslinePath is where the script lands inside the VM.
const guestStatuslinePath = "~/.claude/statusline-command.sh"

// secretish matches the credential shapes tests/invariants already refuse to
// see committed. A statusline is the one mechanism that copies arbitrary host
// file content into a VM, and invariant 4 says the Claude token is the only
// credential that gets to be in there.
var secretish = regexp.MustCompile(
	`sk-ant-[A-Za-z0-9-]{8}|-----BEGIN [A-Z ]*PRIVATE KEY|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{20}`)

// readStatusline loads the configured script, or returns nil if none is set.
//
// Called early in `new`, before any VM state is touched: a path typo should
// cost nothing, not surface after a VM has finished provisioning.
func readStatusline(env *Env) ([]byte, error) {
	path := env.Cfg.Statusline
	if path == "" {
		return nil, nil
	}
	body, err := os.ReadFile(expandHome(path))
	if err != nil {
		return nil, fmt.Errorf("PTRBOX_STATUSLINE: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("PTRBOX_STATUSLINE: %s is empty", path)
	}
	if match := secretish.Find(body); match != nil {
		return nil, fmt.Errorf(
			"PTRBOX_STATUSLINE: %s looks like it contains a credential (%q) - "+
				"it would be copied into every sandbox, and the Claude token is "+
				"the only credential a VM gets", path, string(match))
	}
	return body, nil
}

// expandHome resolves a leading ~/ so the config file can say what a person
// would write.
func expandHome(path string) string {
	if rest, found := strings.CutPrefix(path, "~/"); found {
		return os.Getenv("HOME") + "/" + rest
	}
	return path
}

// installStatusline writes the script into the VM and points Claude Code's
// settings at it.
//
// jq does the settings edit in the guest, so the model pre-seed written by
// 40-userenv.sh survives and nothing here has to know the guest's home path -
// which is version-dependent and must never be hardcoded.
const installStatuslineScript = `set -e
mkdir -p ~/.claude
cat > ~/.claude/statusline-command.sh
chmod +x ~/.claude/statusline-command.sh
tmp=$(mktemp)
jq --arg c "$HOME/.claude/statusline-command.sh" \
   '.statusLine = {type: "command", command: $c}' \
   ~/.claude/settings.json > "$tmp"
mv "$tmp" ~/.claude/settings.json`

func installStatusline(env *Env, vm string, script []byte) error {
	if len(script) == 0 {
		return nil
	}
	err := env.Lima.Send(strings.NewReader(string(script)),
		lima.ShellArgs(vm, "bash", "-c", installStatuslineScript)...)
	if err != nil {
		return errors.New("installing the statusline failed: " + err.Error())
	}
	env.Out.Say("statusline installed at %s", guestStatuslinePath)
	return nil
}
