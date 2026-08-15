# Elevated helper for "restart" and "deploy": stops the HomeNVR service,
# kills its whole process tree, optionally swaps in a freshly built
# homenvrd.exe, then starts the service and reports the state to a result
# file under $env:TEMP so the caller can read it back.
param(
    [string]$ServiceName = 'homenvrd',
    [string]$InstallDir = '',
    [string]$NewExe = ''
)

$ErrorActionPreference = 'Stop'
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'packaging\service\homenvr.common.ps1')

$resPath = Join-Path $env:TEMP 'homenvr-restart-result.txt'
try {
    $info = Get-HomenvrInfo -ServiceName $ServiceName -InstallDir $InstallDir
    if (-not $info.InstallDir) { throw "Service '$ServiceName' not found; install it first (install-service.ps1)." }

    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($svc -and $svc.Status.ToString() -eq 'Running') {
        sc.exe stop $ServiceName | Out-Null
        [void](Wait-HomenvrService -ServiceName $ServiceName -Target 'Stopped' -TimeoutSec 30)
    }
    Start-Sleep -Seconds 2
    Stop-HomenvrProcesses -ServiceName $ServiceName

    $exe = Join-Path $info.InstallDir 'homenvrd.exe'
    if ($NewExe) {
        if (-not (Test-Path -LiteralPath $NewExe)) { throw "New binary not found: $NewExe" }
        if (Test-Path -LiteralPath $exe) { Move-Item -LiteralPath $exe -Destination "$exe.old" -Force }
        Copy-Item -LiteralPath $NewExe -Destination $exe -Force
    }

    sc.exe start $ServiceName | Out-Null
    $now = Wait-HomenvrService -ServiceName $ServiceName -Target 'Running' -TimeoutSec 60

    $counts = Get-HomenvrProcesses -ServiceName $ServiceName | Group-Object Name | ForEach-Object { "$($_.Name)=$($_.Count)" }
    $status = if ($now) { $now.Status } else { 'missing' }
    "OK status=$status processes=$($counts -join ' ')" | Set-Content -LiteralPath $resPath
    if ($now -and $now.Status.ToString() -ne 'Running') {
        "WARN final status $($now.Status)" | Add-Content -LiteralPath $resPath
    }
} catch {
    "ERR $($_.Exception.Message)" | Set-Content -LiteralPath $resPath
}
