# Starts the HomeNVR service (homenvrd). Admin only (sc.exe start).
# Re-run with -Confirm to proceed. See homenvr-off.ps1.
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
    Write-Host "This will START the HomeNVR service (homenvrd)."
    Write-Host "Re-run with -Confirm to proceed, or use: homenvr-off.ps1"
    exit 1
}

Clear-HomenvrProcesses
sc.exe start $svc | Out-Null
$t = 0
while ((Get-Service -Name $svc).Status -ne 'Running' -and $t -lt 60) {
    Start-Sleep -Seconds 1; $t++
}
$now = Get-Service -Name $svc
if ($now.Status -eq 'Running') {
    Write-Host "HomeNVR running. Panel: http://localhost:8080  Live: http://localhost:1984" -ForegroundColor Green
} else {
    Write-Host "WARNING: still $($now.Status). Check: sc.exe query $svc" -ForegroundColor Red
}
