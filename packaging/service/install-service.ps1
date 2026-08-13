# Installs HomeNVR as a WinSW-managed Windows service ("homenvrd").
#
# Usage (Admin):
#   .\install-service.ps1 -WinSW C:\path\to\WinSW-x64.exe
#     [-ServiceDir C:\Tools\homenvr] [-ConfigPath C:\Tools\homenvr\config.jsonc]
#     [-YAMLPath C:\Tools\homenvr\go2rtc.yaml] [-NoBuild]
#
# Uninstall:
#   .\install-service.ps1 -Uninstall -WinSW C:\path\to\WinSW-x64.exe -ServiceDir C:\Tools\homenvr
param(
    [Parameter(Mandatory)][string]$WinSW,
    [string]$ServiceDir = "C:\Tools\homenvr",
    [string]$ConfigPath = "C:\Tools\homenvr\config.jsonc",
    [string]$YAMLPath = "C:\Tools\homenvr\go2rtc.yaml",
    [switch]$NoBuild,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)

if (-not (Test-Path $WinSW)) {
    throw "WinSW binary not found: $WinSW (grab it from https://github.com/winsw/winsw/releases)"
}

if ($Uninstall) {
    $svcExe = Join-Path $ServiceDir "homenvrd-service.exe"
    if (-not (Test-Path $svcExe)) {
        Write-Host "No homenvrd-service.exe in $ServiceDir; nothing to uninstall."
        exit 0
    }
    & $svcExe uninstall
    if ($LASTEXITCODE -ne 0) { throw "WinSW uninstall failed (code $LASTEXITCODE)" }
    Write-Host "Service homenvrd uninstalled." -ForegroundColor Green
    exit 0
}

# 1. Build the daemon (unless -NoBuild).
$exe = Join-Path $repo "homenvrd.exe"
if (-not $NoBuild) {
    Write-Host "Building homenvrd.exe..."
    & go build -o $exe ./cmd/homenvrd
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
}

# 2. Stage everything under $ServiceDir.
New-Item -ItemType Directory -Force -Path $ServiceDir | Out-Null
$svcExe = Join-Path $ServiceDir "homenvrd-service.exe"
Copy-Item $WinSW $svcExe -Force
Copy-Item $exe (Join-Path $ServiceDir "homenvrd.exe") -Force
$logDir = Join-Path $ServiceDir "logs"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null

# 3. Render the WinSW service XML.
$tmpl = Get-Content -Raw (Join-Path $PSScriptRoot "homenvrd-service.xml.tmpl")
$xml = $tmpl
$xml = $xml.Replace("@EXE@", (Join-Path $ServiceDir "homenvrd.exe"))
$xml = $xml.Replace("@CONFIG@", $ConfigPath)
$xml = $xml.Replace("@YAML@", $YAMLPath)
$xml = $xml.Replace("@LOGDIR@", $logDir)
$xmlPath = Join-Path $ServiceDir "homenvrd-service.xml"
Set-Content -Path $xmlPath -Value $xml -Encoding UTF8

# 4. Install the service (requires the shell to be elevated).
& $svcExe install
if ($LASTEXITCODE -ne 0) { throw "WinSW install failed (code $LASTEXITCODE)" }

Write-Host ""
Write-Host "Installed. Service id: homenvrd" -ForegroundColor Green
Write-Host "  exe:     $ServiceDir\homenvrd.exe"
Write-Host "  config:  $ConfigPath   (create it before starting; see config.example.jsonc)"
Write-Host "  panel:   http://localhost:8080  (config: web.port)"
Write-Host "  live:    http://localhost:1984  (config: go2rtc.api_port)"
Write-Host "Start it with: sc.exe start homenvrd  (or homenvr-on.ps1)"
