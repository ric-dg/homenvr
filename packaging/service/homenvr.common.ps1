# Shared helpers for the HomeNVR service scripts. Everything is derived
# dynamically: the install dir comes from the installed service's binary
# path, and all per-host values (mic ports, record dirs, web ports, URLs)
# come from config.jsonc. Nothing here is host-specific.

function Get-HomenvrInstallDir {
    param([string]$ServiceName = 'homenvrd', [string]$InstallDir = '')
    if ($InstallDir) { return $InstallDir }
    $svc = Get-CimInstance Win32_Service -Filter "Name='$ServiceName'" -ErrorAction SilentlyContinue
    if (-not $svc -or -not $svc.PathName) { return '' }
    $bin = $svc.PathName.Trim('"')
    return Split-Path -Parent $bin
}

# Removes // and /* */ comments from JSONC while leaving strings (e.g. rtsp://
# URLs) intact.
function Remove-JsonComments {
    param([string[]]$Lines)
    $out = [System.Collections.Generic.List[string]]::new()
    $inBlock = $false
    foreach ($raw in $Lines) {
        $sb = [System.Text.StringBuilder]::new()
        $i = 0
        $len = $raw.Length
        $inStr = $false
        while ($i -lt $len) {
            $c = $raw[$i]
            if ($inBlock) {
                if ($c -eq '*' -and $i + 1 -lt $len -and $raw[$i + 1] -eq '/') { $inBlock = $false; $i += 2; continue }
                $i++
                continue
            }
            if ($inStr) {
                [void]$sb.Append($c)
                if ($c -eq '\' -and $i + 1 -lt $len) { [void]$sb.Append($raw[$i + 1]); $i += 2; continue }
                if ($c -eq '"') { $inStr = $false }
                $i++
                continue
            }
            if ($c -eq '"') { $inStr = $true; [void]$sb.Append($c); $i++; continue }
            if ($c -eq '/' -and $i + 1 -lt $len -and $raw[$i + 1] -eq '/') { break }
            if ($c -eq '/' -and $i + 1 -lt $len -and $raw[$i + 1] -eq '*') { $inBlock = $true; $i += 2; continue }
            [void]$sb.Append($c)
            $i++
        }
        $out.Add($sb.ToString())
    }
    return $out
}

# Returns the parsed config.jsonc for the service's install dir, or $null.
function Get-HomenvrConfig {
    param([string]$InstallDir)
    $configPath = Join-Path $InstallDir 'config.jsonc'
    if (-not (Test-Path -LiteralPath $configPath)) { return $null }
    $clean = Remove-JsonComments (Get-Content -LiteralPath $configPath)
    try { return ($clean -join [Environment]::NewLine | ConvertFrom-Json) } catch { return $null }
}

# Returns a snapshot of the service plus its resolved install dir / config.
function Get-HomenvrInfo {
    param([string]$ServiceName = 'homenvrd', [string]$InstallDir = '')
    $installDir = Get-HomenvrInstallDir -ServiceName $ServiceName -InstallDir $InstallDir
    $svc = Get-CimInstance Win32_Service -Filter "Name='$ServiceName'" -ErrorAction SilentlyContinue
    $config = if ($installDir) { Get-HomenvrConfig -InstallDir $installDir } else { $null }
    return [pscustomobject]@{
        ServiceName = $ServiceName
        Service     = $svc
        InstallDir  = $installDir
        ConfigPath  = if ($installDir) { Join-Path $installDir 'config.jsonc' } else { '' }
        Config      = $config
    }
}

# Finds every process in the service's process tree (wrapper + daemon +
# go2rtc + ffmpeg). Falls back to matching homenvrd.exe by name when the
# service is not installed (manual runs).
function Get-HomenvrProcesses {
    param([string]$ServiceName = 'homenvrd')
    $all = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue)
    $svc = Get-CimInstance Win32_Service -Filter "Name='$ServiceName'" -ErrorAction SilentlyContinue
    $rootIds = @()
    if ($svc -and $svc.ProcessId -and $svc.ProcessId -ne 0) {
        $rootIds = @($svc.ProcessId)
    } else {
        $rootIds = @($all | Where-Object { $_.Name -eq 'homenvrd.exe' } | ForEach-Object ProcessId)
    }
    $seen = [System.Collections.Generic.HashSet[int]]::new()
    $pending = [System.Collections.Generic.List[int]]::new()
    foreach ($id in $rootIds) { $pending.Add($id) }
    $idx = 0
    while ($idx -lt $pending.Count) {
        $cur = $pending[$idx]
        if ($seen.Add($cur)) {
            foreach ($c in ($all | Where-Object { $_.ParentProcessId -eq $cur })) { $pending.Add([int]$c.ProcessId) }
        }
        $idx++
    }
    return @($all | Where-Object { $seen.Contains([int]$_.ProcessId) })
}

function Stop-HomenvrProcesses {
    param([string]$ServiceName = 'homenvrd')
    foreach ($p in Get-HomenvrProcesses -ServiceName $ServiceName) {
        $proc = Get-Process -Id $p.ProcessId -ErrorAction SilentlyContinue
        if (-not $proc) { continue }
        try { $proc.Kill($true) } catch { try { $proc.Kill() } catch {} }
    }
    Start-Sleep -Seconds 2
}

function Wait-HomenvrService {
    param([string]$ServiceName = 'homenvrd', [string]$Target = 'Running', [int]$TimeoutSec = 60)
    $t = 0
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    while ($svc -and $svc.Status.ToString() -ne $Target -and $t -lt $TimeoutSec) {
        Start-Sleep -Seconds 1; $t++
        $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    }
    return $svc
}
