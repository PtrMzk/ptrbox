ptrbox step 0 - what to run on the Windows PC, and why
======================================================

Everything in Track C (the actual Multipass backend) is designed around the
answers this produces. Nothing here is a ptrbox sandbox: the VMs are throwaway
Ubuntu guests with no firewall, made to be asked questions and deleted.

Getting this folder onto Windows: first, in WSL, build the credential probe
into it (so the PC needs neither Go nor a checkout):

    make -C ~/code/ptrbox windows-capture

then, on Windows, in cmd.exe (NOT PowerShell, whose pipes mangle binary data):

    cd /d %USERPROFILE%
    wsl -d ptrbox tar -C /home/ptrbox/code/ptrbox/tests -cf - windows-capture | tar -xf -

which leaves %USERPROFILE%\windows-capture. This is the way across because the
development distro runs with interop and automount off, and \\wsl.localhost is
not reachable on this PC either (tried 2026-09-17); wsl.exe reaching IN needs
none of them. tar also leaves the line endings alone, which git with autocrlf
would not, and they matter here. To check the copy, compare
    certutil -hashfile windows-capture\credread-probe.exe SHA256
with  sha256sum credread-probe.exe  in WSL.

Needs: Hyper-V on, and Multipass installed
    winget install --exact --id Canonical.Multipass

1. ONCE, in an ADMIN PowerShell - the private switch ptrbox VMs will share:

    New-VMSwitch -Name ptrbox -SwitchType Internal
    New-NetIPAddress -InterfaceAlias "vEthernet (ptrbox)" -IPAddress 172.31.255.1 -PrefixLength 24

   Save what those two print into out\switch-setup.txt (create the out folder
   if it is not there yet).

2. In a NORMAL prompt, in this folder:

    capture.cmd main             ~10 min
    capture.cmd sudo             ~5 min   <- the one that decides the design
    capture.cmd credential       ~1 min, asks you to type one throwaway value
    capture.cmd before-reboot
    (reboot the PC)
    capture.cmd after-reboot

3. Send back the whole out\ folder, from cmd.exe in this folder:

    tar -cf - out | wsl -d ptrbox tar -C /home/ptrbox/code/ptrbox/tests/windows-capture -xf -

   (git ignores it there). The only edit worth making first: replace your Windows
   user name in paths with "you", if you mind it being in the repo.

What each part answers
----------------------
main           argv and output shapes (what the fake must emulate); whether
               stdin, exit codes and the stdout/stderr split survive
               `multipass exec`; whether cloud-init can create the no-sudo
               "agent" user without breaking Multipass's own login; whether
               per-boot scripts re-run on restart; whether the second NIC is
               called eth1 and takes the static 172.31.255.17; whether the
               agent user can write to the mount; which VM names are refused.
sudo           whether the /workspace mount COMES BACK on a boot where the login
               user has no passwordless sudo - which is every boot of a
               ptrbox sandbox after the first. Boot 1 keeps sudo (Multipass
               needs it to set the mount up at all), boot 2 removes it
               mid-boot, and boot 3 is the answer. YES: Windows sandboxes are single-user and
               sudo-less, same as the Mac. NO: the mount needs a root-capable
               account to stay in the VM, and we talk before building that.
before/after   whether the VM's addresses change across a host reboot (the
reboot         Default Switch's do; the ptrbox switch's must not).
credential     whether ptrbox can read the Claude token from Credential
               Manager, and how cmdkey encodes it.

If something fails
------------------
Keep going and send what there is - a failure is an answer. The likely ones:
  - launch refuses --mount: say so; the fallback is `launch` then `mount`.
  - "ptrbox" missing from networks.json: the switch was not created, or
    Multipass cannot see Internal switches - either way that file says it.
  - mounts are disabled: run  multipass set local.privileged-mounts=true
    once, note that you had to, and re-run.

Step 22 - the first real run  (Track C is committed; this is what tests it)
---------------------------------------------------------------------------
In WSL:
    make -C ~/code/ptrbox windows-build
then copy the folder across as above (the same tar line; it now carries
ptrbox.exe and smoke.ps1). On the PC, first delete the step-0 VM - it holds
the first slot address and the mount of ptrbox-scratch:
    multipass delete --purge scratch
Store the token if not already (prompts; nothing on the command line):
    cmdkey /generic:claude-sandbox-token /user:token /pass
Then, in a NORMAL PowerShell (ptrbox never elevates) - unsigned scripts are
blocked by default, so bypass the policy for this one process:
    powershell -ExecutionPolicy Bypass -File %USERPROFILE%\windows-capture\smoke.ps1
It runs ptrbox install, ptrbox new sandbox-test, a check through the agent
account, ptrbox rm - and its header lists what a green run proves. Keep the
whole output. Things a first run is expected to decide (CLAUDE.md, items 76
and 78): whether cloud-init reports 0 or 2 with the real template, whether
the per-boot scripts run as the agent under runuser -l as they do under
lima, and whether 1200s is enough for the launch. Then reboot the PC and run
    ptrbox start sandbox-test
before the rm, which is the test the switch and the mount repair exist for.

Result (2026-09-20): the Windows backend ran GREEN on this PC - smoke.ps1 to
"smoke: OK" (Multipass 1.16.4+win, Windows 11, Hyper-V, Ubuntu 24.04 guests),
then the host-reboot test: after a reboot `ptrbox start sandbox-test` found the
sandbox's mount listed but dead, restarted the VM, and handed back a live
/workspace. Six fix-ups came out of the run, each its own commit (the 4 KiB
exec stall, twice; the smoke script's rm; Ubuntu's four extra setuid
binaries; listed-is-not-alive; a deadline on every client call). Two things
to know when running it again: PowerShell blocks unsigned scripts (use
`powershell -ExecutionPolicy Bypass -File ...`), and the multipass daemon can
wedge as the PC boots while re-initialising a mount - every `multipass` call
then hangs, ptrbox's included, until an ADMIN PowerShell runs
    Stop-Process -Name multipassd -Force
    Start-Service Multipass
