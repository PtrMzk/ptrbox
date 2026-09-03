```
        _        _
  _ __ | |_ _ __| |__   _____  __
 | '_ \| __| '__| '_ \ / _ \ \/ /    ptrbox
 | |_) | |_| |  | |_) | (_) >  <     disposable Claude Code sandboxes, one VM per repo
 | .__/ \__|_|  |_.__/ \___/_/\_\
 |_|
```

[![CI](https://github.com/PtrMzk/ptrbox/actions/workflows/ci.yml/badge.svg)](https://github.com/PtrMzk/ptrbox/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go 1.24+](https://img.shields.io/badge/go-1.24%2B-blue.svg)](go.mod)

**`ptrbox` gives every repo its own Lima VM on your Mac, with
[Claude Code](https://claude.com/claude-code) inside and a kernel-enforced
firewall around it.** The agent can install packages, run tests and edit code.
It cannot reach the internet except through a domain allowlist, cannot see
anything on your Mac except that one project directory, and holds no
credentials except its own Claude token.

## The problem

Running a coding agent directly on your Mac hands it your shell: your files,
your ssh keys, your browser sessions, your network. Permission prompts help,
but they depend on you reading every one — and the answer you give at hour
one is not the attention you have at hour six.

`ptrbox` moves the agent into a VM instead. The boundary is the hypervisor
and a firewall in the guest kernel, so it holds no matter what the agent
does — auto-accepting edits, or `--dangerously-skip-permissions` if you want
it fully unattended. The blast radius is the same either way, which is the
point: the guarantees come from the machine, not from the agent asking first.

## Why use this

- **Egress is default-deny, per sandbox.** An nftables wall in each guest
  allows exactly one road out: a CONNECT-only squid proxy with a domain
  allowlist. Each VM has its own list, so granting `pypi.org` to one project
  grants it to nothing else. `ptrbox logs --denied` shows what was blocked
  and which VM asked.
- **One directory, no keys.** The project repo is the only mount. Git push
  happens on the host, where your ssh keys stay. The only credential in a VM
  is the Claude token — injected from the macOS Keychain, and only after
  every security assertion has passed.
- **No root, ever.** Passwordless sudo is removed and setuid binaries are
  stripped on every boot. The agent cannot undo the firewall, and neither
  can you — which is what makes the next point work.
- **VMs are sheep, not pets.** Everything a sandbox contains is declared up
  front — distro, sizing, runtimes, packages, allowlist — and installed at
  create time from a template. With no root there is no fixing a VM by hand,
  so no VM ever accumulates state worth keeping. Deleting one loses nothing:
  its config and allowlist live on the host and survive `ptrbox rm`, so
  `ptrbox new` rebuilds the identical box in minutes. Suspect a sandbox is
  in a weird state? Shoot it and make another.
- **Only what you ask for.** Language runtimes (node, Python via uv, Go) and
  browser testing are each one flag, off by default. A LaTeX VM gets no npm,
  and no npm-shaped holes in its allowlist — every runtime you skip is
  surface the agent never has and domains it can never reach.
- **Verified before handover.** `ptrbox new` reboots the VM to arm the
  firewall, then asserts every property — direct egress blocked, proxy
  works, denied domains denied, sudo gone, requested runtimes present. Any
  failure refuses to inject your token.
- **The proxy is sandboxed too.** Squid parses bytes every agent can
  influence, so it runs in its own VM with no mounts and no credentials —
  an exploit lands in an empty guest, not in a host process with your
  privileges. Its lifecycle is automatic: the first sandbox up starts it,
  the last one down stops it.

```
macOS host
├── 127.0.0.1:8888              port forward into the proxy VM - the host's
│                               entire share of the egress path
├── git + ssh keys              stay on the host; you push from the host
├── ~/code/<repo>/              the ONLY directory a sandbox VM can see
├── Lima VM "ptrbox-proxy"      squid, CONNECT-only domain allowlist
│    └── no mounts, no credentials, nothing else installed
└── Lima VM per repo (vz + virtiofs)
     ├── mount:    ~/code/<repo> -> /workspace   (nothing else is mounted)
     ├── nftables: default-deny egress; only the proxy and DNS
     ├── no sudo:  passwordless root removed, setuid stripped, every boot
     └── claude    any permission mode - the VM is the boundary
```

## Quick start

Requires macOS on Apple Silicon, [Lima](https://lima-vm.io)
(`brew install lima`), and a Claude Pro/Max/Team subscription.

```bash
go install github.com/PtrMzk/ptrbox/cmd/ptrbox@latest
ptrbox install                # dependency checks, proxy VM, config files

claude setup-token            # prints a 1-year token; then store it:
security add-generic-password -a "$USER" -s claude-sandbox-token -w

ptrbox new my-api             # creates ~/code/my-api if needed, builds its VM
ssh lima-my-api
cd /workspace && claude
```

`ptrbox install` is idempotent — re-run it after upgrades any time; it never
overwrites files you have edited without asking. It never installs packages
on the Mac for you; a missing dependency prints the `brew install` line and
stops. `ptrbox new` takes a few minutes: it shows you the plan first (and
offers to edit it), provisions over an open network, reboots to arm the
firewall, then verifies everything before handing the VM over.

Your repo is a live two-way mount, so the agent's edits appear on your Mac
immediately. Review and push from the host, where your keys are:

```bash
cd ~/code/my-api
git add -p && git commit && git push
```

Done with a project? `ptrbox rm my-api` destroys the VM and reclaims the
disk. The repo, the VM's config and its allowlist are untouched.

## Commands

| Command | What it does |
|---|---|
| `ptrbox install [--yes] [--update]` | Host setup: dependencies, the proxy VM, config files. Idempotent; `--update` refreshes seeded files, carrying your settings. |
| `ptrbox new <repo>` | Create the repo if needed, provision and verify its VM. |
| `ptrbox rm <repo\|vm>` | Destroy a VM and its artifacts. Leaves the repo alone. |
| `ptrbox start <repo\|vm>` | Start a stopped VM, bringing the proxy VM up first. |
| `ptrbox stop <repo\|vm>` | Stop a VM; the proxy VM stops with the last sandbox. |
| `ptrbox logs [--denied]` | Read the proxy log. `--denied` shows what was blocked. |
| `ptrbox allow <vm> [domain…]` | Add domains to that sandbox's allowlist, or open it in `$EDITOR`; `--list` prints it. Applies live. |
| `ptrbox sync-proxy` | Push hand-edited allowlists to the proxy now. |

A bare name lands under `~/code`; anything with a slash is used as a path.
Prefer `ptrbox start`/`stop` over `limactl start`/`stop` for sandboxes — a
sandbox started without its proxy has no network at all.

### When a build can't reach something

```bash
ptrbox logs --denied                  # find the domain (localport= says which VM asked)
ptrbox allow my-api files.example.com # grant it to that sandbox; validates and reloads
```

Each sandbox's list starts as a copy of the template
(`~/.config/ptrbox/allowed_domains.txt`) minus the groups for runtimes it
does not have. The per-VM file survives `ptrbox rm`, so a re-create keeps
its egress; delete the file to reset. If an edit doesn't parse, the previous
version is restored automatically. Every entry is a capability grant — keep
each VM's list minimal.

## Configuration

`ptrbox install` writes `~/.config/ptrbox/config`, annotated, with every
line commented out — configuring is uncommenting, not assembling. Guest
distro, repo root, VM sizing, runtimes, extra apt packages, Claude model,
in-VM git identity. Every key also works as a `PTRBOX_*` environment
variable, which wins over the file.

Settings that describe one sandbox rather than your Mac can be set per VM:

```sh
# ~/.config/ptrbox/vms/thesis
PTRBOX_EXTRA_PACKAGES="texlive-latex-recommended latexmk"
PTRBOX_MEMORY=4GiB
```

Layers, lowest first, each stating only what it changes:

```
built-in defaults  <  ~/.config/ptrbox/config  <  ~/.config/ptrbox/vms/<vm>  <  environment
```

`ptrbox new` bakes the result into the VM, so editing a file does nothing
until you re-create that sandbox — and the file surviving the VM is what
makes the re-create reproduce the same sandbox. Per-VM config is host-side,
never read from the repo mount, so an agent cannot choose what its own
sandbox contains.

## What's inside a VM

Debian 13 (or Ubuntu 24.04), Claude Code, tmux, build tooling — plus the
runtimes you ask for, and **only** those. Each is per-VM settable and off by
default:

```sh
PTRBOX_NODE=true               # npm/npx, and therefore npx-based MCP servers
PTRBOX_NODE_VERSION=22.11.0    # or lts, the default
PTRBOX_UV=true                 # Python, via uv
PTRBOX_GO=true                 # the upstream Go toolchain, under $HOME
PTRBOX_PLAYWRIGHT=true         # Chromium/GTK libraries + the Playwright CDNs
PTRBOX_EXTRA_PACKAGES="ripgrep sqlite3"   # any apt packages, checked by name
```

Claude Code is always installed: it is a native binary and needs no runtime.
A runtime or package that was asked for and failed to install fails
`ptrbox new` loudly, not a week later when something needs it. Anything that
needs root is a change to `vm/provision/`, then re-create the VM — the
template stays the single source of truth for what a sandbox contains.

## Development

```bash
make build    # dist/ptrbox
make lint     # go vet, plus shellcheck on the guest scripts
make test     # ~250 cases against a faked limactl and Keychain
make check    # both; needs no Mac, no VM and no network
make smoke    # the real thing: recreates a scratch VM (macOS only)
```

`make check` runs anywhere, including Linux and CI, because every external
command is faked — it covers the whole lifecycle plus the security
invariants asserted against the generated VM configs. Changing what a VM
contains means changing `vm/`, then `make golden`: the diff to the golden
files is the review surface. The host CLI depends on the Go standard library
and nothing else.

The layout follows where code runs, because that boundary is the security
model: `cmd/` and `internal/` run on your Mac, `vm/` becomes the guests,
`host/` is the squid config and seed allowlist pushed into the proxy VM.

## Limitations

- macOS on Apple Silicon only.
- At most 16 sandboxes at once: each holds one of the proxy's per-VM ports
  (`ptrbox rm` frees the slot).
- The proxy VM idles at a few hundred MB of RAM while any sandbox runs;
  ptrbox deliberately errs toward leaving it up rather than ever cutting a
  live agent's network.
- Guest DNS can still resolve external names (queries go out even though
  connections are blocked); closing that hole is planned.
- `uninstall` and `doctor` are not implemented yet.

## License

MIT, see [LICENSE](LICENSE).
