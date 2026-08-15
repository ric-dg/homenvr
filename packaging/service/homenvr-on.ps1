# Starts the HomeNVR service (homenvrd). Admin only.
# Everything is auto-derived from the installed service and config.jsonc.
# Re-run with -Confirm to proceed. See homenvr-off.ps1.
param(
    [string]$ServiceName = 'homenvrd',
    [string]$InstallDir = '',
    [switch]$Confirm
)

. (Join-Path $PSScriptRoot 'homenvr.common.ps1')

$info = Get-HomenvrInfo -ServiceName $ServiceName -InstallDir $InstallDir
if (-not $info.InstallDir -or -not $info.Service) {
    Write-Host "Service '$ServiceName' not found. Install it first: .\install-service.ps1 -WinSW <path>" -ForegroundColor Red
    exit 1
}
if (-not (Test-Path -LiteralPath $info.ConfigPath)) {
    Write-Host "WARNING: $($info.ConfigPath) not found - the daemon will use defaults." -ForegroundColor Yellow
}

if (-not $Confirm) {
    Write-Host "This will START the HomeNVR service ($ServiceName)."
    Write-Host "Re-run with -Confirm to proceed, or use: homenvr-off.ps1"
    exit 1
}

Stop-HomenvrProcesses -ServiceName $ServiceName
sc.exe start $ServiceName | Out-Null
$svc = Wait-HomenvrService -ServiceName $ServiceName -Target 'Running' -TimeoutSec 60

if ($svc -and $svc.Status.ToString() -eq 'Running') {
    $web = 8080; $api = 1984
    if ($info.Config) {
        if ($info.Config.web) { $web = $info.Config.web.port }
        if ($info.Config.go2rtc) { $api = $info.Config.go2rtc.api_port }
    }
    Write-Host "HomeNVR running. Panel: http://localhost:$web  Live: http://localhost:$api" -ForegroundColor Green
} else {
    Write-Host "WARNING: still $($svc.Status). Check: sc.exe query $ServiceName" -ForegroundColor Red
    exit 1
}
