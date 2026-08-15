# HomeNVR dev-ops helper. Everything is located from the installed service and
# config.jsonc, so this works on any host. Run from a non-admin prompt; the
# commands that touch the service elevate themselves (accept the UAC prompt).
#
#   .\homenvr.ps1 status                  service + process tree snapshot
#   .\homenvr.ps1 probe [-Tail 20]        status + log tails + go2rtc streams
#   .\homenvr.ps1 det-repro [-Seconds 5]  run the detector ffmpeg pipe standalone
#   .\homenvr.ps1 start | stop            start/stop the service (elevates)
#   .\homenvr.ps1 restart                 stop, kill orphans, start (elevates)
#   .\homenvr.ps1 deploy                  build ./cmd/homenvrd, swap binary,
#                                         restart (elevates)
#
# Optional overrides (applied to the underlying script): -ServiceName,
# -InstallDir, and for deploy -GoArgs/-BuildDir.
param(
    [Parameter(Position = 0)][string]$Command = 'status',
    [string]$ServiceName = 'homenvrd',
    [string]$InstallDir = '',
    [int]$Tail = 15,
    [int]$Seconds = 5,
    [switch]$NoStreams
)

$root = Split-Path -Parent $PSScriptRoot
. (Join-Path $root 'packaging\service\homenvr.common.ps1')

function Invoke-ElevatedScript {
    param([string]$FilePath, [string[]]$Args2)
    $argList = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $FilePath) + $Args2
    $p = Start-Process pwsh -Verb RunAs -Wait -PassThru -ArgumentList $argList
    return $p.ExitCode
}

switch ($Command.ToLower()) {
    { $_ -in 'status', 'stats' } {
        $info = Get-HomenvrInfo -ServiceName $ServiceName -InstallDir $InstallDir
        if (-not $info.InstallDir) {
            Write-Host "Service '$ServiceName' not found. Use -InstallDir, or install first." -ForegroundColor Red
            exit 1
        }
        $state = if ($info.Service) { (Get-Service -Name $ServiceName).Status } else { 'not installed' }
        Write-Host "service : $ServiceName : $state"
        Write-Host "install : $($info.InstallDir)"
        $procs = Get-HomenvrProcesses -ServiceName $ServiceName
        Write-Host "processes: $($procs.Count)"
        $procs | Group-Object Name | ForEach-Object { Write-Host "  $($_.Name) x$($_.Count)" }
    }

    'probe' {
        & (Join-Path $PSScriptRoot 'probe.ps1') -ServiceName $ServiceName -InstallDir $InstallDir -Tail $Tail $(if ($NoStreams) { '-NoStreams' })
    }

    'det-repro' {
        & (Join-Path $PSScriptRoot 'det_repro.ps1') -ServiceName $ServiceName -InstallDir $InstallDir -Seconds $Seconds
    }

    { $_ -in 'start', 'stop' } {
        $script = if ($_ -eq 'start') { Join-Path $root 'packaging\service\homenvr-on.ps1' } else { Join-Path $root 'packaging\service\homenvr-off.ps1' }
        $resPath = Join-Path $env:TEMP 'homenvr-onoff-result.txt'
        Remove-Item -LiteralPath $resPath -ErrorAction SilentlyContinue
        [void](Invoke-ElevatedScript -FilePath $script -Args2 @('-ServiceName', $ServiceName, '-Confirm', '-ResultFile', $resPath))
        Get-Content -LiteralPath $resPath -ErrorAction SilentlyContinue
        if (-not (Test-Path -LiteralPath $resPath)) { Write-Host 'Elevated script produced no result (UAC declined?).' -ForegroundColor Yellow }
    }

    { $_ -in 'restart', 'deploy' } {
        if ($_ -eq 'deploy') {
            $staged = Join-Path $env:TEMP 'homenvr-deploy\homenvrd.exe'
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $staged) | Out-Null
            Push-Location $root
            try { & go build -o $staged ./cmd/homenvrd } finally { Pop-Location }
            if ($LASTEXITCODE -ne 0) { Write-Host 'go build failed - is Go on PATH?' -ForegroundColor Red; exit 1 }
            Write-Host "built $staged"
            $newExe = @('-NewExe', $staged)
        } else { $newExe = @() }

        $resPath = Join-Path $env:TEMP 'homenvr-restart-result.txt'
        Remove-Item -LiteralPath $resPath -ErrorAction SilentlyContinue
        $script = Join-Path $PSScriptRoot 'homenvr-restart.ps1'
        [void](Invoke-ElevatedScript -FilePath $script -Args2 (@('-ServiceName', $ServiceName) + $newExe))
        Get-Content -LiteralPath $resPath -ErrorAction SilentlyContinue
        if (-not (Test-Path -LiteralPath $resPath)) { Write-Host 'Elevated script produced no result (UAC declined?).' -ForegroundColor Yellow }
    }

    default {
        Write-Host "Unknown command '$Command'." -ForegroundColor Yellow
        Get-Content -LiteralPath $PSCommandPath | Select-String -Pattern '^\s*#   \.\/homenvr' | ForEach-Object { $_.Line }
    }
}
