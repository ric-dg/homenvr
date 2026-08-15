# Prints a diagnostic snapshot: service state, process tree, the daemon's own
# service log, the go2rtc log, and the go2rtc /api/streams JSON. Everything is
# located from the installed service and config.jsonc - no host-specific paths.
param(
    [string]$ServiceName = 'homenvrd',
    [string]$InstallDir = '',
    [int]$Tail = 15,
    [switch]$NoStreams
)

. (Join-Path (Split-Path -Parent $PSScriptRoot) 'packaging\service\homenvr.common.ps1')

$info = Get-HomenvrInfo -ServiceName $ServiceName -InstallDir $InstallDir
if (-not $info.InstallDir) {
    Write-Host "Service '$ServiceName' not found." -ForegroundColor Red
    exit 1
}

Write-Host "== service ==" -ForegroundColor Cyan
if ($info.Service) {
    $state = Get-Service -Name $ServiceName | Select-Object -ExpandProperty Status
    Write-Host "$ServiceName : $state  (bin: $($info.Service.PathName))"
} else {
    Write-Host "$ServiceName : not installed (install dir from -InstallDir only)" -ForegroundColor Yellow
}
Write-Host "install dir: $($info.InstallDir)"
if (-not $info.Config) { Write-Host "config.jsonc not parseable - only generic info below." -ForegroundColor Yellow }

Write-Host ""
Write-Host "== processes ==" -ForegroundColor Cyan
$procs = Get-HomenvrProcesses -ServiceName $ServiceName
if ($procs.Count -eq 0) {
    Write-Host "(none)" -ForegroundColor Yellow
} else {
    $procs | ForEach-Object { Write-Host "  $($_.Name) pid=$($_.ProcessId) ppid=$($_.ParentProcessId)" }
}

$logDir = ''
if ($info.Config -and $info.Config.paths -and $info.Config.paths.log_dir) {
    $logDir = $info.Config.paths.log_dir
    if (-not [System.IO.Path]::IsPathRooted($logDir)) { $logDir = Join-Path $info.InstallDir $logDir }
} elseif ($info.InstallDir) {
    $logDir = Join-Path $info.InstallDir 'logs'
}

foreach ($f in @('service.log', 'go2rtc.out.log', 'go2rtc.err.log')) {
    $p = Join-Path $logDir $f
    if (Test-Path -LiteralPath $p) {
        Write-Host ""
        Write-Host "== $f (tail $Tail) ==" -ForegroundColor Cyan
        Get-Content -LiteralPath $p -Tail $Tail
    }
}

if (-not $NoStreams -and $info.Config -and $info.Config.go2rtc) {
    $api = $info.Config.go2rtc.api_port
    if (-not $api) { $api = 1984 }
    Write-Host ""
    Write-Host "== go2rtc /api/streams ==" -ForegroundColor Cyan
    try {
        $streams = Invoke-RestMethod -Uri "http://127.0.0.1:$api/api/streams" -TimeoutSec 5
        $streams.PSObject.Properties | ForEach-Object {
            $v = $_.Value
            $srcs = @($v.source | ForEach-Object { $_.producers.Count })
            $mux = @($v.moderators | ForEach-Object { $_ })
            Write-Host "  $($_.Name): producers=$($v.producers.Count) sources=$($srcs -join ',') moderators=$($mux -join ',')"
        }
    } catch {
        Write-Host "  (unreachable: $($_.Exception.Message))" -ForegroundColor Yellow
    }
}
