# smoke.ps1 - the real VM cycle on the Windows PC. `make smoke` for Multipass.
#
# Run from a normal PowerShell (not admin: ptrbox never elevates), with
# ptrbox.exe on PATH or next to this script. Destroys and recreates a scratch
# sandbox named sandbox-test; takes minutes. Every verification line must read
# OK - a FAIL anywhere is a failed run, and the token is never injected into a
# VM that failed.
#
# What a green run proves, in order (plans/windows.md, "Verification"):
#   install   the switch preflight passed, the proxy VM launched, took its
#             address on the switch after its reboot, cloud-init finished,
#             verify-proxy.sh's lines read OK, and the host dialed
#             172.31.255.2:8888 and :8889 - the summary says "reached at
#             172.31.255.2:8888"
#   new       every verify.sh line OK as the agent, including "exactly one
#             mount" (fuse.sshfs), "sudo removed", "setuid stripped", "daemon
#             user isolated", "backend marker", "direct egress blocked",
#             "egress fails fast", "proxy egress works"; then the token from
#             Credential Manager, and the timing line
#   shell     lands as the agent user in /workspace
#   rm        purges the sandbox and stops the proxy when it was the last one
#
# Then, by hand: reboot the PC, `ptrbox start sandbox-test`, and re-run the
# verification - the test the internal switch and Ready() exist for.
$ErrorActionPreference = "Stop"

$ptrbox = Get-Command ptrbox -ErrorAction SilentlyContinue
if (-not $ptrbox) {
    $local = Join-Path $PSScriptRoot "ptrbox.exe"
    if (Test-Path $local) { $ptrbox = $local } else { throw "ptrbox.exe is neither on PATH nor beside this script at $local" }
}

function Step($what, [scriptblock]$run) {
    Write-Host "== $what"
    & $run
    if ($LASTEXITCODE -ne 0) { throw "$what failed (exit $LASTEXITCODE)" }
}

Step "ptrbox install" { & $ptrbox install --yes }
# A leftover from a previous run is not an error.
& $ptrbox rm sandbox-test 2>$null
Step "ptrbox new sandbox-test" { & $ptrbox new sandbox-test --no-edit }
Step "ptrbox shell sandbox-test (id -un; pwd)" {
    # An interactive shell needs a terminal; this asks the same question the
    # way a script can: through the agent account, in the workspace.
    & multipass exec sandbox-test -d /workspace -- sudo -n -u agent -H sh -c 'id -un; pwd; grep -c " /workspace " /proc/mounts'
}
Step "ptrbox rm sandbox-test" { & $ptrbox rm sandbox-test }
Write-Host "smoke: OK"
