# Stops the HomeNVR service (homenvrd) and kills any leftover processes.
# Admin only. Re-run with -Confirm to proceed.
param([switch]$Confirm)

$svc = 'homenvrd'
$markers = @(
    'homenvrd.exe',
    'C:\Tools\homenvr\config.jsonc',
    'C:\Tools\homenvr\go2rtc.yaml',
    'E:\CCTV',
    'tcp://127.0.0.1:9010',
    'tcp://127.0.0.1:9011'
)

function Find-HomenvrProcs {
    Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        if (-not $_.CommandLine) { return $false }
        foreach ($m in $markers) { if ($_.CommandLine.Contains($m)) { return $true } }
        return $false
    }
}

function Clear-HomenvrProcesses {
    $procs = @(Find-HomenvrProcs)
    foreach ($p in $procs) {
        $proc = Get-Process -Id $p.ProcessId -ErrorAction SilentlyContinue
        if ($proc) {
            try { $proc.Kill($true) } catch { try { $proc.Kill() } catch {} }
        }
    }
    Start-Sleep -Seconds 2
}

if (-not $Confirm) {
    Write-Host "This will STOP the HomeNVR service (homenvrd)."
    Write-Host "Re-run with -Confirm to proceed, or use: homenvr-on.ps1"
    exit 1
}

$state = (Get-Service -Name $svc -ErrorAction SilentlyContinue).Status
if ($state -eq 'Running') {
    sc.exe stop $svc | Out-Null
    $t = 0
    while ((Get-Service -Name $svc).Status -ne 'Stopped' -and $t -lt 30) {
        Start-Sleep -Seconds 1; $t++
    }
}
Start-Sleep -Seconds 2
Clear-HomenvrProcesses
$left = @(Find-HomenvrProcs)
if ($left.Count -eq 0) {
    Write-Host "HomeNVR fully stopped (no leftover processes)." -ForegroundColor Green
} else {
    Write-Host "WARNING: $($left.Count) HomeNVR process(es) still running:" -ForegroundColor Red
    $left | ForEach-Object { Write-Host "  $($_.Name) pid=$($_.ProcessId)" -ForegroundColor Red }
}
