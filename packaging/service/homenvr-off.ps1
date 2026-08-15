# Stops the HomeNVR service (homenvrd) and kills any leftover processes in
# its process tree. Admin only. Re-run with -Confirm to proceed.
param(
    [string]$ServiceName = 'homenvrd',
    [string]$InstallDir = '',
    [string]$ResultFile = '',
    [switch]$Confirm
)

. (Join-Path $PSScriptRoot 'homenvr.common.ps1')

if (-not $Confirm) {
    Write-Host "This will STOP the HomeNVR service ($ServiceName)."
    Write-Host "Re-run with -Confirm to proceed, or use: homenvr-on.ps1"
    exit 1
}

$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($svc -and $svc.Status.ToString() -eq 'Running') {
    sc.exe stop $ServiceName | Out-Null
    [void](Wait-HomenvrService -ServiceName $ServiceName -Target 'Stopped' -TimeoutSec 30)
}
Start-Sleep -Seconds 2
Stop-HomenvrProcesses -ServiceName $ServiceName

$left = @(Get-HomenvrProcesses -ServiceName $ServiceName)
if ($left.Count -eq 0) {
    $msg = 'OK HomeNVR fully stopped (no leftover processes).'
    Write-Host $msg -ForegroundColor Green
    if ($ResultFile) { $msg | Set-Content -LiteralPath $ResultFile }
} else {
    $msg = "WARN $($left.Count) HomeNVR process(es) still running"
    Write-Host $msg -ForegroundColor Red
    $left | ForEach-Object { Write-Host "  $($_.Name) pid=$($_.ProcessId)" -ForegroundColor Red }
    if ($ResultFile) { $msg | Set-Content -LiteralPath $ResultFile }
    exit 1
}
