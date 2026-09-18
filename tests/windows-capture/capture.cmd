@echo off
rem ===========================================================================
rem ptrbox step 0 - feasibility capture for the Multipass backend.
rem
rem Run from a normal (NOT admin) Command Prompt or PowerShell, in this folder:
rem
rem     capture.cmd main            about 10 minutes, the bulk of it
rem     capture.cmd sudo            about 5 minutes, THE experiment that matters
rem     capture.cmd credential      a minute; asks you to type one throwaway value
rem     capture.cmd before-reboot   seconds; then reboot the PC, then:
rem     capture.cmd after-reboot    seconds
rem
rem Everything lands in .\out\ - stdout, stderr and exit status kept apart,
rem because the fake ptrbox tests against has to know which stream says what.
rem It creates and deletes VMs named scratch, scratch2 and 1abc. Nothing else
rem is touched.
rem
rem Before the first run, ONCE, in an ADMIN PowerShell (see README.txt):
rem     New-VMSwitch -Name ptrbox -SwitchType Internal
rem     New-NetIPAddress -InterfaceAlias "vEthernet (ptrbox)" -IPAddress 172.31.255.1 -PrefixLength 24
rem ===========================================================================
setlocal
set HERE=%~dp0
set OUT=%HERE%out
set REPO=%USERPROFILE%\code\ptrbox-scratch
if not exist "%OUT%" mkdir "%OUT%"
if "%1"=="" goto usage
goto %1

:usage
echo usage: capture.cmd main ^| sudo ^| credential ^| before-reboot ^| after-reboot
exit /b 2

rem ---------------------------------------------------------------------------
:main
if not exist "%REPO%" mkdir "%REPO%"
>"%REPO%\from-host.txt" echo hello from the host

echo == environment
>"%OUT%\when.txt" echo %DATE% %TIME%
ver > "%OUT%\windows-version.txt"
multipass version > "%OUT%\version.txt" 2>&1
multipass get local.driver > "%OUT%\driver.txt" 2>&1
multipass networks --format json > "%OUT%\networks.json" 2> "%OUT%\networks.stderr"
multipass networks > "%OUT%\networks.txt" 2>&1
multipass find > "%OUT%\find.txt" 2>&1

echo == launch (several minutes)
multipass launch --name scratch --cpus 2 --memory 2G --disk 10G --cloud-init "%HERE%exp.yaml" --network name=ptrbox,mode=manual --mount "%REPO%:/workspace" 24.04 > "%OUT%\launch.stdout" 2> "%OUT%\launch.stderr"
>"%OUT%\launch.exit" echo %ERRORLEVEL%

echo == after launch
multipass exec scratch -- cloud-init status --long > "%OUT%\cloud-init-status-after-launch.txt" 2>&1
multipass info scratch --format json > "%OUT%\info.json" 2>&1
multipass list --format json > "%OUT%\list.json" 2>&1
multipass list > "%OUT%\list.txt" 2>&1
multipass exec scratch -- ip -j addr > "%OUT%\ip-addr-boot1.json" 2>&1
multipass exec scratch -- cat /var/lib/ptrbox/boots > "%OUT%\boots-after-launch.txt" 2>&1

echo == users and sudo
multipass exec scratch -- id > "%OUT%\id-login.txt" 2>&1
multipass exec scratch -- id agent > "%OUT%\id-agent.txt" 2>&1
multipass exec scratch -- sudo -n true > "%OUT%\sudo-login.txt" 2>&1
>"%OUT%\sudo-login.exit" echo %ERRORLEVEL%
multipass exec scratch -- sudo -u agent -H sudo -n true > "%OUT%\sudo-agent.txt" 2>&1
>"%OUT%\sudo-agent.exit" echo %ERRORLEVEL%
multipass exec scratch -- sudo cat /etc/sudoers.d/90-cloud-init-users > "%OUT%\sudoers-dropin.txt" 2>&1
multipass exec scratch -- sudo ls -la /etc/sudoers.d > "%OUT%\sudoers-dir.txt" 2>&1
multipass exec scratch -- sudo cat /home/ubuntu/.ssh/authorized_keys > "%OUT%\authorized-keys-login.txt" 2>&1

echo == the mount
multipass exec scratch -- bash -c "mount | grep -i -e workspace -e sshfs -e fuse" > "%OUT%\mount.txt" 2>&1
multipass exec scratch -- ls -ld /workspace > "%OUT%\mount-ls.txt" 2>&1
multipass exec scratch -- cat /workspace/from-host.txt > "%OUT%\mount-read.txt" 2>&1
multipass exec scratch -- sudo -u agent -H touch /workspace/written-by-agent > "%OUT%\mount-agent-write.txt" 2>&1
>"%OUT%\mount-agent-write.exit" echo %ERRORLEVEL%
multipass exec scratch -- sudo -u agent -H ls -ln /workspace > "%OUT%\mount-agent-ls.txt" 2>&1
multipass exec scratch -- bash -c "ps -eo user,pid,args | grep -i -e sshfs -e sftp | grep -v grep" > "%OUT%\mount-processes.txt" 2>&1
dir "%REPO%" > "%OUT%\mount-host-dir.txt" 2>&1

echo == stdin, exit status, stream split
echo hello-from-stdin| multipass exec scratch -- sudo -u agent -H bash -c "cat >> ~/.profile" > "%OUT%\stdin.txt" 2>&1
>"%OUT%\stdin.exit" echo %ERRORLEVEL%
multipass exec scratch -- sudo -u agent -H tail -1 /home/agent/.profile > "%OUT%\stdin-readback.txt" 2>&1
multipass exec scratch -- bash -c "exit 7" > "%OUT%\exit-status.txt" 2>&1
>"%OUT%\exit-status.exit" echo %ERRORLEVEL%
multipass exec scratch -- bash -c "echo to-stdout; echo to-stderr >&2" > "%OUT%\streams.stdout" 2> "%OUT%\streams.stderr"
multipass exec scratch -- bash -lc "echo login-shell-ok; pwd" > "%OUT%\bash-lc.txt" 2>&1

echo == restart (does per-boot run again? does the static address come up?)
multipass restart scratch > "%OUT%\restart.stdout" 2> "%OUT%\restart.stderr"
>"%OUT%\restart.exit" echo %ERRORLEVEL%
multipass exec scratch -- cloud-init status --long > "%OUT%\cloud-init-status-after-restart.txt" 2>&1
multipass exec scratch -- cat /var/lib/ptrbox/boots > "%OUT%\boots-after-restart.txt" 2>&1
multipass exec scratch -- ip -j addr > "%OUT%\ip-addr-boot2.json" 2>&1
multipass exec scratch -- ip route > "%OUT%\ip-route-boot2.txt" 2>&1
multipass info scratch --format json > "%OUT%\info-after-restart.json" 2>&1
multipass exec scratch -- ls -ld /workspace/from-host.txt > "%OUT%\mount-after-restart.txt" 2>&1

echo == the ptrbox switch
ping -n 2 172.31.255.17 > "%OUT%\switch-host-to-guest.txt" 2>&1
multipass exec scratch -- ping -c 2 172.31.255.1 > "%OUT%\switch-guest-to-host.txt" 2>&1
ipconfig > "%OUT%\ipconfig.txt" 2>&1

echo == stop / start
multipass stop scratch > "%OUT%\stop.stdout" 2> "%OUT%\stop.stderr"
>"%OUT%\stop.exit" echo %ERRORLEVEL%
multipass list --format json > "%OUT%\list-stopped.json" 2>&1
multipass start scratch > "%OUT%\start.stdout" 2> "%OUT%\start.stderr"
>"%OUT%\start.exit" echo %ERRORLEVEL%
multipass exec scratch -- cat /var/lib/ptrbox/boots > "%OUT%\boots-after-start.txt" 2>&1

echo == name rules (both are expected to be refused)
multipass launch --name 1abc 24.04 > "%OUT%\name-leading-digit.txt" 2>&1
>"%OUT%\name-leading-digit.exit" echo %ERRORLEVEL%
multipass launch --name -x 24.04 > "%OUT%\name-leading-dash.txt" 2>&1
>"%OUT%\name-leading-dash.exit" echo %ERRORLEVEL%
multipass delete --purge 1abc > nul 2>&1

echo == errors worth knowing the shape of
multipass info no-such-vm > "%OUT%\info-missing.stdout" 2> "%OUT%\info-missing.stderr"
>"%OUT%\info-missing.exit" echo %ERRORLEVEL%
multipass exec no-such-vm -- true > "%OUT%\exec-missing.stdout" 2> "%OUT%\exec-missing.stderr"
>"%OUT%\exec-missing.exit" echo %ERRORLEVEL%

echo.
echo main: done. scratch is left RUNNING for before-reboot/after-reboot.
echo Next: capture.cmd sudo
exit /b 0

rem ---------------------------------------------------------------------------
:sudo
if not exist "%REPO%" mkdir "%REPO%"
>"%REPO%\from-host.txt" echo hello from the host

echo == launch scratch2: boot 1 keeps sudo, so the mount is SET UP normally
multipass launch --name scratch2 --cpus 2 --memory 2G --disk 10G --cloud-init "%HERE%exp-nosudo.yaml" --mount "%REPO%:/workspace" 24.04 > "%OUT%\nosudo-launch.stdout" 2> "%OUT%\nosudo-launch.stderr"
>"%OUT%\nosudo-launch.exit" echo %ERRORLEVEL%
multipass exec scratch2 -- sudo -n true > "%OUT%\nosudo-boot1-sudo.txt" 2>&1
>"%OUT%\nosudo-boot1-sudo.exit" echo %ERRORLEVEL%
multipass exec scratch2 -- ls -la /workspace > "%OUT%\nosudo-boot1-mount.txt" 2>&1

echo == restart: boot 2 removes sudo - but DURING the boot, racing the re-mount,
echo    so whatever boot 2 shows is a hint and not the answer
multipass restart scratch2 > "%OUT%\nosudo-restart.stdout" 2> "%OUT%\nosudo-restart.stderr"
>"%OUT%\nosudo-restart.exit" echo %ERRORLEVEL%
multipass exec scratch2 -- sudo -n true > "%OUT%\nosudo-boot2-sudo.txt" 2>&1
>"%OUT%\nosudo-boot2-sudo.exit" echo %ERRORLEVEL%
multipass exec scratch2 -- cat /var/lib/ptrbox/nosudo-ran > "%OUT%\nosudo-ran.txt" 2>&1
multipass exec scratch2 -- bash -c "mount | grep -i -e workspace -e sshfs -e fuse" > "%OUT%\nosudo-boot2-mount.txt" 2>&1
multipass exec scratch2 -- ls -la /workspace > "%OUT%\nosudo-boot2-ls.txt" 2>&1
>"%OUT%\nosudo-boot2-ls.exit" echo %ERRORLEVEL%
multipass info scratch2 --format json > "%OUT%\nosudo-info.json" 2>&1

echo == stop/start as well: ptrbox reboots that way, not with restart
multipass stop scratch2 > nul 2>&1
multipass start scratch2 > "%OUT%\nosudo-start.stdout" 2> "%OUT%\nosudo-start.stderr"
>"%OUT%\nosudo-start.exit" echo %ERRORLEVEL%
multipass exec scratch2 -- sudo -n true > "%OUT%\nosudo-boot3-sudo.txt" 2>&1
>"%OUT%\nosudo-boot3-sudo.exit" echo %ERRORLEVEL%
multipass exec scratch2 -- bash -c "mount | grep -i -e workspace -e sshfs -e fuse" > "%OUT%\nosudo-boot3-mount.txt" 2>&1
multipass exec scratch2 -- ls -la /workspace > "%OUT%\nosudo-boot3-ls.txt" 2>&1
>"%OUT%\nosudo-boot3-ls.exit" echo %ERRORLEVEL%
multipass exec scratch2 -- cat /var/lib/ptrbox/boots > "%OUT%\nosudo-boots.txt" 2>&1

echo == the daemon's log says HOW it mounts (may need an admin prompt to read)
for /r "%ProgramData%\Multipass" %%f in (*.log) do copy /y "%%f" "%OUT%\multipassd-%%~nxf" > nul 2>&1

multipass delete --purge scratch2 > nul 2>&1
echo.
echo sudo: done. The answer is out\nosudo-boot3-ls.txt - boot 3 is the first boot
echo that had no sudo from its first second (nosudo-boot3-sudo.exit must not be 0):
echo   from-host.txt listed   = the mount survives without sudo  (single-user model)
echo   empty / an error       = the mount needs sudo             (two-user model)
exit /b 0

rem ---------------------------------------------------------------------------
:credential
if not exist "%HERE%credread-probe.exe" (
  echo credread-probe.exe is not in this folder. It is built in WSL, by
  echo     make -C ~/code/ptrbox windows-capture
  echo and then this folder is copied out again. See README.txt.
  exit /b 1
)
echo == a throwaway entry, read back through CredReadW
cmdkey /generic:ptrbox-credread-probe /user:token /pass:probe-VALUE-123 > "%OUT%\cmdkey-add.txt" 2>&1
cmdkey /list:ptrbox-credread-probe > "%OUT%\cmdkey-list.txt" 2>&1
set PTRBOX_TEST_CREDENTIAL=1
"%HERE%credread-probe.exe" -test.run TestCredReadAgainstCmdkey -test.v > "%OUT%\credread.txt" 2>&1
>"%OUT%\credread.exit" echo %ERRORLEVEL%
cmdkey /delete:ptrbox-credread-probe > nul 2>&1

echo == does a bare /pass PROMPT? Type anything at the prompt (it is deleted again).
echo    If no prompt appears, that is the finding.
cmdkey /generic:ptrbox-prompt-probe /user:token /pass
>"%OUT%\cmdkey-prompt.exit" echo %ERRORLEVEL%
cmdkey /delete:ptrbox-prompt-probe > nul 2>&1
set /p PROMPTED=Did cmdkey ask for a password just now? [y/n] 
>"%OUT%\cmdkey-prompt.txt" echo prompted=%PROMPTED%
echo.
echo credential: done. out\credread.txt should end in PASS.
exit /b 0

rem ---------------------------------------------------------------------------
:before-reboot
multipass info scratch --format json > "%OUT%\reboot-before-info.json" 2>&1
ipconfig > "%OUT%\reboot-before-ipconfig.txt" 2>&1
echo before-reboot: done. Reboot the PC, then run: capture.cmd after-reboot
exit /b 0

:after-reboot
multipass start scratch > "%OUT%\reboot-after-start.txt" 2>&1
multipass info scratch --format json > "%OUT%\reboot-after-info.json" 2>&1
ipconfig > "%OUT%\reboot-after-ipconfig.txt" 2>&1
ping -n 2 172.31.255.17 > "%OUT%\reboot-after-ping.txt" 2>&1
multipass exec scratch -- ls -la /workspace > "%OUT%\reboot-after-mount.txt" 2>&1
multipass delete --purge scratch > nul 2>&1
echo after-reboot: done, scratch deleted. Send back the whole out\ folder.
exit /b 0
