; Ether for Windows — NSIS installer.
;
; Builds EtherSetup-<version>.exe which:
;   * installs to %ProgramFiles%\Ether (server, tray, ffmpeg/ffprobe),
;   * registers the Ether Windows service (auto-start, LocalSystem for the
;     initial release; data dir %ProgramData%\Ether via ETHER_DATA_DIR
;     service environment — set by `ether service install`… see below),
;   * puts the tray in the current user's Run key (launch at login),
;   * opens TCP 32400 in Windows Firewall (private + public profiles),
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
!define MUI_FINISHPAGE_LINK "Open the setup wizard"
!define MUI_FINISHPAGE_LINK_LOCATION "http://localhost:32400/ether/"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Ether" SecEther
  SetOutPath "$INSTDIR"

  ; Stop a running install before overwriting files (upgrade path).
  nsExec::ExecToLog 'sc stop ether'
  Sleep 2000
  nsExec::ExecToLog 'taskkill /IM ether-tray.exe /F'

  File "${STAGING}\ether.exe"
  File "${STAGING}\ether-tray.exe"
  File "${STAGING}\ffmpeg.exe"
  File "${STAGING}\ffprobe.exe"
  File "${STAGING}\LICENSE"
  File "${STAGING}\THIRD_PARTY_NOTICES.md"

  ; Data directory shared by service + tray.
  nsExec::ExecToLog 'cmd /c mkdir "%ProgramData%\Ether" 2>nul'

  ; Register/update the service. `ether service install` renders the SCM
  ; entry (name, env, restart policy) via internal/service; fall back to
  ; a plain sc.exe registration if that ever fails.
  nsExec::ExecToLog '"$INSTDIR\ether.exe" service uninstall'
  nsExec::ExecToLog '"$INSTDIR\ether.exe" service install'
  Pop $0
  ${If} $0 != 0
    nsExec::ExecToLog 'sc create ether binPath= "\"$INSTDIR\ether.exe\"" start= auto DisplayName= "Ether"'
    nsExec::ExecToLog 'sc description ether "Ether media server"'
  ${EndIf}
  ; Restart forever with backoff (5s, 15s, then 60s for every later
  ; failure; the count resets after a clean day), treat error exits as
  ; failures too, and start once boot settles. `ether service install`
  ; applies the same policy; repeating it here covers the sc.exe fallback.
  nsExec::ExecToLog 'sc failure ether reset= 86400 actions= restart/5000/restart/15000/restart/60000'
  nsExec::ExecToLog 'sc failureflag ether 1'
  nsExec::ExecToLog 'sc config ether start= delayed-auto'

  ; Firewall: Plex port in, GDM discovery UDP in.
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Ether (32400)"'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="Ether (32400)" dir=in action=allow protocol=TCP localport=32400 program="$INSTDIR\ether.exe"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Ether GDM discovery"'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="Ether GDM discovery" dir=in action=allow protocol=UDP localport=32410-32414 program="$INSTDIR\ether.exe"'

  ; Tray at login for the installing user (the tray's own toggle manages
  ; the same value later).
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "EtherTray" '"$INSTDIR\ether-tray.exe"'

  ; Shortcuts.
  CreateDirectory "$SMPROGRAMS\Ether"
  CreateShortcut "$SMPROGRAMS\Ether\Ether.lnk" "$INSTDIR\ether-tray.exe"
  CreateShortcut "$SMPROGRAMS\Ether\Open Ether setup.lnk" "http://localhost:32400/ether/"

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
  ; manually to purge configuration, database and cache.
  MessageBox MB_OK "Ether was removed. Your settings and cache in %ProgramData%\Ether were kept — delete that folder to purge everything."
SectionEnd
