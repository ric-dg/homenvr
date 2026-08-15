# Reproduces the motion detector's ffmpeg rawvideo pipe outside the daemon,
# capturing stderr (which the daemon discards). Runs the exact same command
# the detector uses and reports whether frames actually flowed. Useful when
# the detector reports "stream lost" - it tells you whether the problem is the
# RTSP/go2rtc feed or the daemon's frame reading.
param(
    [string]$ServiceName = 'homenvrd',
    [string]$InstallDir = '',
    [string]$CameraName = '',
    [string]$Ffmpeg = 'ffmpeg',
    [int]$Seconds = 5
)

. (Join-Path (Split-Path -Parent $PSScriptRoot) 'packaging\service\homenvr.common.ps1')

$info = Get-HomenvrInfo -ServiceName $ServiceName -InstallDir $InstallDir
if (-not $info.Config) { Write-Host "No parseable config at $($info.ConfigPath)." -ForegroundColor Red; exit 1 }

$cam = $null
foreach ($c in $info.Config.cameras) {
    if ($CameraName -and $c.name -ne $CameraName) { continue }
    if ($c.motion -and $c.motion.enabled) { $cam = $c; break }
}
if (-not $cam) {
    Write-Host "No enabled motion camera found in config." -ForegroundColor Red
    exit 1
}

$rtspHost = if ($info.Config.paths.rtsp_host) { $info.Config.paths.rtsp_host } else { '127.0.0.1' }
$rtspPort = if ($info.Config.go2rtc.rtsp_port) { $info.Config.go2rtc.rtsp_port } else { 8554 }
$url = "rtsp://${rtspHost}:${rtspPort}/$($cam.name)?rw_timeout=8000000"
$w = $cam.motion.width; $h = $cam.motion.height; $fps = $cam.motion.fps

Write-Host "camera : $($cam.name)"
Write-Host "feed   : $url"
Write-Host "motion : ${w}x${h} @ ${fps}fps  ($Seconds s of rawvideo)"
Write-Host ""

$errFile = Join-Path $env:TEMP "homenvr-det-repro-$PID.err"
$argv = @('-hide_banner', '-loglevel', 'error',
    '-timeout', '8000000',
    '-rtsp_transport', 'tcp', '-rtsp_flags', 'prefer_tcp',
    '-i', $url,
    '-an',
    '-vf', "scale=${w}:${h},fps=${fps},format=gray",
    '-t', "$Seconds",
    '-f', 'rawvideo', '-pix_fmt', 'gray', 'pipe:1')

$raw = & $Ffmpeg @argv 2>$errFile
$exit = $LASTEXITCODE
$bytes = 0L
foreach ($chunk in @($raw)) { $bytes += $chunk.Length }
$frames = [math]::Floor($bytes / ($w * $h))

if ($exit -eq 0 -and $frames -ge 1) {
    Write-Host "OK: $frames frames ($bytes bytes) in $Seconds s => $([math]::Round($frames / $Seconds, 1)) fps" -ForegroundColor Green
} else {
    Write-Host "FAIL: exit=$exit frames=$frames" -ForegroundColor Red
}
$err = Get-Content -LiteralPath $errFile -ErrorAction SilentlyContinue
if ($err) {
    Write-Host "--- ffmpeg stderr ---" -ForegroundColor Cyan
    $err | Select-Object -First 15
}
Remove-Item -LiteralPath $errFile -ErrorAction SilentlyContinue
