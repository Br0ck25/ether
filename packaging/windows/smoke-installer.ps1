# Only run on an ephemeral Windows release runner: installs and uninstalls Ether.
param([Parameter(Mandatory = $true)][string]$Version)
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$Installer = Join-Path $Root "dist\EtherSetup-$Version.exe"
$Installed = Join-Path $env:ProgramFiles "Ether"
$Tray = $null

try {
  $Setup = Start-Process -FilePath $Installer -ArgumentList "/S" -Wait -PassThru
  if ($Setup.ExitCode -ne 0) { throw "Installer exited $($Setup.ExitCode)" }

  $Ready = $false
  for ($Attempt = 0; $Attempt -lt 60; $Attempt++) {
    try {
      $Service = Get-Service ether
      if ($Service.Status -eq "Running") {
        $Identity = Invoke-WebRequest "http://127.0.0.1:32400/identity" -Headers @{Accept="application/xml"} -TimeoutSec 2
        if ($Identity.StatusCode -eq 200 -and ([xml]$Identity.Content).MediaContainer.machineIdentifier) {
          $Ready = $true
          break
        }
      }
    } catch { Write-Host "Waiting for installed server: $($_.Exception.Message)" }
    Start-Sleep -Seconds 1
  }
  if (-not $Ready) { throw "Installed server did not become ready" }

  $TrayPath = Join-Path $Installed "ether-tray.exe"
  $PE = [IO.File]::ReadAllBytes($TrayPath)
  $Header = [BitConverter]::ToInt32($PE, 0x3c)
  $Subsystem = [BitConverter]::ToUInt16($PE, $Header + 24 + 68)
  if ($Subsystem -ne 2) { throw "Desktop launcher has a console subsystem: $Subsystem" }
  $Tray = Start-Process -FilePath $TrayPath -PassThru
  Start-Sleep -Seconds 3
  if ($Tray.HasExited) { throw "Desktop launcher exited $($Tray.ExitCode)" }
  Write-Host "Installed service serves Plex identity; desktop launcher stays running."
} finally {
  if ($Tray -and -not $Tray.HasExited) { Stop-Process -Id $Tray.Id -Force }
  $Uninstaller = Join-Path $Installed "Uninstall.exe"
  if (Test-Path $Uninstaller) {
    $Removed = Start-Process -FilePath $Uninstaller -ArgumentList "/S" -Wait -PassThru
    if ($Removed.ExitCode -ne 0) { throw "Uninstaller exited $($Removed.ExitCode)" }
  }
}
