# Installs HomeNVR as a WinSW-managed Windows service (default id "homenvrd").
#
# Usage (Admin):
#   .\install-service.ps1 -WinSW C:\path\to\WinSW-x64.exe
#     [-ServiceDir C:\Tools\homenvr] [-ConfigPath C:\Tools\homenvr\config.jsonc]
#     [-YAMLPath C:\Tools\homenvr\go2rtc.yaml] [-NoBuild]
#     [-StartupShortcut] [-DesktopShortcut]
#
#   -StartupShortcut / -DesktopShortcut (default: both on) create a
#   "HomeNVR Tray.lnk" pointing at the tray binary, in the startup folder
#   and/or on the desktop. Disable with -StartupShortcut:$false etc.
#
#   ServiceDir defaults to the install dir of an existing service (from its
#   binary path), or must be given on a fresh install.
#
# Uninstall:
#   .\install-service.ps1 -Uninstall -WinSW C:\path\to\WinSW-x64.exe
param(
    [Parameter(Mandatory)][string]$WinSW,
    [string]$ServiceName = 'homenvrd',
    [string]$ServiceDir = '',
    [string]$ConfigPath = '',
    [string]$YAMLPath = '',
    [switch]$NoBuild,
    [bool]$StartupShortcut = $true,
    [bool]$DesktopShortcut = $true,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
. (Join-Path $PSScriptRoot 'homenvr.common.ps1')

function New-Lnk {
    param([string]$Path, [string]$Target)
    $shell = New-Object -ComObject WScript.Shell
    $lnk = $shell.CreateShortcut($Path)
    $lnk.TargetPath = $Target
    $lnk.Save()
}

function New-TrayShortcuts {
    param([string]$TrayExe)
    if (-not (Test-Path $TrayExe)) {
        throw "Tray binary not found: $TrayExe"
    }
    if ($StartupShortcut) {
        $p = Join-Path ([Environment]::GetFolderPath('Startup')) "HomeNVR Tray.lnk"
        New-Lnk -Path $p -Target $TrayExe
        Write-Host "Startup shortcut: $p" -ForegroundColor Green
    }
    if ($DesktopShortcut) {
        $p = Join-Path ([Environment]::GetFolderPath('Desktop')) "HomeNVR Tray.lnk"
        New-Lnk -Path $p -Target $TrayExe
        Write-Host "Desktop shortcut: $p" -ForegroundColor Green
    }
}

function Remove-TrayShortcuts {
    foreach ($dir in @([Environment]::GetFolderPath('Startup'), [Environment]::GetFolderPath('Desktop'))) {
        $p = Join-Path $dir "HomeNVR Tray.lnk"
        if (Test-Path $p) {
            Remove-Item $p -Force
            Write-Host "Removed shortcut: $p"
        }
    }
}

if (-not (Test-Path $WinSW)) {
    throw "WinSW binary not found: $WinSW (grab it from https://github.com/winsw/winsw/releases)"
}

if (-not $ServiceDir) {
    $ServiceDir = Get-HomenvrInstallDir -ServiceName $ServiceName
}
if (-not $ServiceDir) {
    throw "No existing '$ServiceName' service to derive the install dir from. Pass -ServiceDir."
}
$ConfigPath = if ($ConfigPath) { $ConfigPath } else { Join-Path $ServiceDir 'config.jsonc' }
$YAMLPath  = if ($YAMLPath)  { $YAMLPath }  else { Join-Path $ServiceDir 'go2rtc.yaml' }

if ($Uninstall) {
    $svcExe = Join-Path $ServiceDir "homenvrd-service.exe"
    if (-not (Test-Path $svcExe)) {
        Write-Host "No homenvrd-service.exe in $ServiceDir; nothing to uninstall."
        exit 0
    }
    & $svcExe uninstall
    if ($LASTEXITCODE -ne 0) { throw "WinSW uninstall failed (code $LASTEXITCODE)" }
    Write-Host "Service $ServiceName uninstalled." -ForegroundColor Green
    Remove-TrayShortcuts
    exit 0
}

# 1. Build the daemon and tray (unless -NoBuild).
$exe = Join-Path $repo "homenvrd.exe"
$trayExe = Join-Path $repo "homenvrd-tray.exe"
if (-not $NoBuild) {
    Write-Host "Building homenvrd.exe..."
    & go build -o $exe ./cmd/homenvrd
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    Write-Host "Building homenvrd-tray.exe..."
    & go build -o $trayExe ./cmd/homenvrd-tray
    if ($LASTEXITCODE -ne 0) { throw "go build (tray) failed" }
}

# 2. Stage everything under $ServiceDir.
New-Item -ItemType Directory -Force -Path $ServiceDir | Out-Null
$svcExe = Join-Path $ServiceDir "homenvrd-service.exe"
Copy-Item $WinSW $svcExe -Force
Copy-Item $exe (Join-Path $ServiceDir "homenvrd.exe") -Force
if (Test-Path $trayExe) {
    Copy-Item $trayExe (Join-Path $ServiceDir "homenvrd-tray.exe") -Force
}
$logDir = Join-Path $ServiceDir "logs"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null

# 3. Render the WinSW service XML.
$tmpl = Get-Content -Raw (Join-Path $PSScriptRoot "homenvrd-service.xml.tmpl")
$xml = $tmpl
$xml = $xml.Replace("@ID@", $ServiceName)
$xml = $xml.Replace("@EXE@", (Join-Path $ServiceDir "homenvrd.exe"))
$xml = $xml.Replace("@CONFIG@", $ConfigPath)
$xml = $xml.Replace("@YAML@", $YAMLPath)
$xml = $xml.Replace("@LOGDIR@", $logDir)
$xmlPath = Join-Path $ServiceDir "homenvrd-service.xml"
Set-Content -Path $xmlPath -Value $xml -Encoding UTF8

# 4. Install the service (requires the shell to be elevated).
& $svcExe install
if ($LASTEXITCODE -ne 0) { throw "WinSW install failed (code $LASTEXITCODE)" }

# 5. Tray shortcuts (startup folder and/or desktop).
$stagedTray = Join-Path $ServiceDir "homenvrd-tray.exe"
if ($StartupShortcut -or $DesktopShortcut) {
    if (Test-Path $stagedTray) {
        New-TrayShortcuts -TrayExe $stagedTray
    } else {
        Write-Host "Skipping tray shortcuts: $stagedTray not present." -ForegroundColor Yellow
    }
}

Write-Host ""
Write-Host "Installed. Service id: $ServiceName" -ForegroundColor Green
Write-Host "  exe:     $ServiceDir\homenvrd.exe"
Write-Host "  config:  $ConfigPath   (create it before starting; see config.example.jsonc)"
Write-Host "  panel:   http://localhost:8080  (config: web.port)"
Write-Host "  live:    http://localhost:1984  (config: go2rtc.api_port)"
Write-Host "Start it with: sc.exe start $ServiceName  (or homenvr-on.ps1)"
