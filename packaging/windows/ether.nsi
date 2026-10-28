; Ether for Windows — NSIS installer.
;
; Builds EtherSetup-<version>.exe which:
;   * installs to %ProgramFiles%\Ether (server, tray, ffmpeg/ffprobe),
;   * creates %ProgramData%\Ether (config.json, credential key, database,
;     cache, logs) with an ACL of SYSTEM + Administrators only,
;   * registers the Ether Windows service (delayed auto-start, LocalSystem,
;     restart-forever recovery) via `ether service install --data-dir`,
;     which bakes the data dir into the service as ETHER_DATA_DIR; on an
;     upgrade it waits for the running service to stop, then updates the
;     registration in place, keeping its ETHER_DATA_DIR,
;   * puts the tray in the current user's Run key (launch at login),
;   * allows ether.exe through Windows Firewall on its port (32400, or the
;     fallback it moves to when Plex Media Server already uses 32400),
;   * registers an uninstaller that stops/removes the service and the rule.
;
; Usage (release workflow, windows runner):
;   stage into dist\windows\staging: ether.exe, ether-tray.exe,
;   ffmpeg.exe, ffprobe.exe, LICENSE, THIRD_PARTY_NOTICES.md
;   makensis /DVERSION=1.2.0 /DSTAGING=dist\windows\staging ^
;            /DOUTFILE=dist\EtherSetup-1.2.0.exe packaging\windows\ether.nsi
; Then sign with signtool when a code-signing cert is available
; (see .github/workflows/release.yml).

Unicode True
ManifestDPIAware True
RequestExecutionLevel admin

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef STAGING
  !define STAGING "..\..\dist\windows\staging"
!endif
!ifndef OUTFILE
  !define OUTFILE "..\..\dist\EtherSetup-${VERSION}.exe"
!endif

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"

; %ProgramData%\Ether, resolved at install time.
Var DataDir
; 1 when the Ether service was already registered (upgrade), set by StopEther.
Var ServiceExists

Name "Ether"
OutFile "${OUTFILE}"
InstallDir "$PROGRAMFILES64\Ether"
InstallDirRegKey HKLM "Software\Ether" "InstallDir"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "Ether"
VIAddVersionKey "FileDescription" "Ether media server installer"
VIAddVersionKey "LegalCopyright" "Ether contributors (GPLv3)"

!define MUI_ICON "..\..\cmd\ether-tray\assets\icon.ico"
!define MUI_UNICON "..\..\cmd\ether-tray\assets\icon.ico"
!define MUI_ABORTWARNING

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "..\..\LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\ether-tray.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Launch Ether"
; The wizard's port is the server's choice (32400, or a fallback when Plex
; Media Server holds it), so open it through the tray, which reads it.
!define MUI_FINISHPAGE_SHOWREADME ""
!define MUI_FINISHPAGE_SHOWREADME_TEXT "Open the setup wizard"
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION OpenSetupWizard
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Function OpenSetupWizard
  Exec '"$INSTDIR\ether-tray.exe" --open'
FunctionEnd

; Upgrade path: stop a running install before its files are overwritten.
; Sets $ServiceExists to 1 when the Ether service is already registered.
; A graceful stop can take up to the server's 30s stop timeout, and File
; fails on a still-locked ether.exe, so wait for the SCM to report STOPPED
; (up to 60s) and only then fall back to killing the process.
Function StopEther
  StrCpy $ServiceExists 0
  ; sc query exits 1060 (ERROR_SERVICE_DOES_NOT_EXIST) when there is no
  ; service. Any other result counts as installed: wrongly assuming an
  ; upgrade only costs a wait, while `ether service install` without
  ; --data-dir still does a fresh install when the service is missing.
  nsExec::ExecToStack 'sc query ether'
  Pop $0
  Pop $1
  ${If} $0 != 1060
    StrCpy $ServiceExists 1
    nsExec::ExecToLog 'sc stop ether'
    Pop $0
    StrCpy $2 0
    ${Do}
      ; findstr exits 0 once the state line reads STOPPED (STOP_PENDING
      ; does not match).
      nsExec::ExecToStack 'cmd.exe /C sc query ether | findstr STOPPED'
      Pop $0
      Pop $1
      ${If} $0 == 0
        ${Break}
      ${EndIf}
      ${If} $2 >= 60
        ; Still not stopped: last resort, so the binary can be replaced.
        DetailPrint "Ether did not stop within 60 seconds; ending ether.exe"
        nsExec::ExecToLog 'taskkill /IM ether.exe /F'
        Pop $0
        Sleep 2000
        ${Break}
      ${EndIf}
      Sleep 1000
      IntOp $2 $2 + 1
    ${Loop}
  ${EndIf}
  ; The tray holds ether-tray.exe open; end it (the finish page relaunches
  ; it, and so does the user's next login).
  nsExec::ExecToLog 'taskkill /IM ether-tray.exe /F'
  Pop $0
  ; Give Windows a moment to release the image files of ended processes.
  Sleep 1000
FunctionEnd

Section "Ether" SecEther
  SetOutPath "$INSTDIR"

  Call StopEther

  File "${STAGING}\ether.exe"
  File "${STAGING}\ether-tray.exe"
  File "${STAGING}\ffmpeg.exe"
  File "${STAGING}\ffprobe.exe"
  File "${STAGING}\LICENSE"
  File "${STAGING}\THIRD_PARTY_NOTICES.md"

  ; Service data directory. It holds config.json and the credential
  ; encryption key, so replace the inherited ACL (which lets Users read
  ; and create files under ProgramData) with full control for SYSTEM
  ; (S-1-5-18) and Administrators (S-1-5-32-544) only. SIDs keep this
  ; independent of the Windows display language. The tray runs as the
  ; user and falls back to defaults when it cannot read the config.
  ReadEnvStr $DataDir "PROGRAMDATA"
  ${If} $DataDir == ""
    StrCpy $DataDir "C:\ProgramData"
  ${EndIf}
  StrCpy $DataDir "$DataDir\Ether"
  CreateDirectory "$DataDir"
  nsExec::ExecToLog 'icacls "$DataDir" /inheritance:r /grant:r *S-1-5-18:(OI)(CI)F /grant:r *S-1-5-32-544:(OI)(CI)F'

  ${If} $ServiceExists == 1
    ; Upgrade: update the existing registration in place (binary path,
    ; start type, restart policy) and keep its Environment, so an owner's
    ; own ETHER_DATA_DIR survives. Never delete and recreate it: with any
    ; handle open (services.msc) a delete only marks the service for
    ; deletion, the recreate fails, and after a reboot there is no
    ; service. Without --data-dir, `ether service install` updates an
    ; existing service in place and leaves its data dir alone.
    nsExec::ExecToLog '"$INSTDIR\ether.exe" service install'
    Pop $0
    ${If} $0 != 0
      nsExec::ExecToLog 'sc config ether binPath= "\"$INSTDIR\ether.exe\"" start= auto DisplayName= "Ether"'
      Pop $0
    ${EndIf}
  ${Else}
    ; Fresh machine: `ether service install` renders the SCM entry (name,
    ; ETHER_DATA_DIR, restart policy) via internal/service; fall back to a
    ; plain sc.exe registration if that ever fails (the server then
    ; defaults to %ProgramData%\Ether by itself).
    nsExec::ExecToLog '"$INSTDIR\ether.exe" service install --data-dir "$DataDir"'
    Pop $0
    ${If} $0 != 0
      nsExec::ExecToLog 'sc create ether binPath= "\"$INSTDIR\ether.exe\"" start= auto DisplayName= "Ether"'
      Pop $0
      nsExec::ExecToLog 'sc description ether "Ether media server"'
      Pop $0
    ${EndIf}
  ${EndIf}
  ; Restart forever with backoff (5s, 15s, then 60s for every later
  ; failure; the count resets after a clean day), treat error exits as
  ; failures too, and start once boot settles. `ether service install`
  ; applies the same policy; repeating it here covers the sc.exe fallback.
  nsExec::ExecToLog 'sc failure ether reset= 86400 actions= restart/5000/restart/15000/restart/60000'
  nsExec::ExecToLog 'sc failureflag ether 1'
  nsExec::ExecToLog 'sc config ether start= delayed-auto'

  ; Firewall: ether.exe's TCP port in (whichever port it uses), GDM
  ; discovery UDP in. "Ether (32400)" is the rule earlier versions added.
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Ether (32400)"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Ether"'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="Ether" dir=in action=allow protocol=TCP program="$INSTDIR\ether.exe"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Ether GDM discovery"'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="Ether GDM discovery" dir=in action=allow protocol=UDP localport=32410-32414 program="$INSTDIR\ether.exe"'

  ; Tray at login for the installing user (the tray's own toggle manages
  ; the same value later).
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "EtherTray" '"$INSTDIR\ether-tray.exe"'

  ; Shortcuts.
  CreateDirectory "$SMPROGRAMS\Ether"
  CreateShortcut "$SMPROGRAMS\Ether\Ether.lnk" "$INSTDIR\ether-tray.exe"
  CreateShortcut "$SMPROGRAMS\Ether\Open Ether setup.lnk" "$INSTDIR\ether-tray.exe" "--open"

  WriteRegStr HKLM "Software\Ether" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\Ether" "Version" "${VERSION}"

  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Ether" "DisplayName" "Ether"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Ether" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Ether" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Ether" "Publisher" "Ether contributors"
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Ether" "NoModify" 1
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Ether" "NoRepair" 1

  nsExec::ExecToLog 'sc start ether'
SectionEnd

Section "Uninstall"
  nsExec::ExecToLog '"$INSTDIR\ether.exe" service uninstall'
  nsExec::ExecToLog 'sc stop ether'
  nsExec::ExecToLog 'sc delete ether'
  nsExec::ExecToLog 'taskkill /IM ether-tray.exe /F'

  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Ether (32400)"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Ether"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Ether GDM discovery"'

  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "EtherTray"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Ether"
  DeleteRegKey HKLM "Software\Ether"

  RMDir /r "$SMPROGRAMS\Ether"
  Delete "$INSTDIR\ether.exe"
  Delete "$INSTDIR\ether.exe.old"
  Delete "$INSTDIR\ether-tray.exe"
  Delete "$INSTDIR\ffmpeg.exe"
  Delete "$INSTDIR\ffprobe.exe"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\THIRD_PARTY_NOTICES.md"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"

  ; Data under %ProgramData%\Ether is intentionally kept. Delete it
  ; manually (as an administrator) to purge configuration, database,
  ; cache and logs.
  MessageBox MB_OK "Ether was removed. Your settings, cache and logs in %ProgramData%\Ether were kept — delete that folder (as an administrator) to purge everything." /SD IDOK
SectionEnd
