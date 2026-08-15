// Package recorder runs one per-camera recording loop in record.mode
// (event / continuous / combined), mirroring v1 motion.py. Event mode reads
// motion from an ffmpeg rawvideo pipe and sound from the mic ctl port, starts
// an ffmpeg MP4 recorder on activity, and finalizes files as
// <prefix>-<motion|sound|both|unknown>-<ts>[-N].mp4. Continuous and combined
// modes run rotating ffmpeg segments instead.
package recorder

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ric-dg/homenvr/internal/config"
	"github.com/ric-dg/homenvr/internal/motion"
	"github.com/ric-dg/homenvr/internal/proc"
	"github.com/ric-dg/homenvr/internal/retention"
	"github.com/ric-dg/homenvr/internal/sleepctx"
)

// Logger is the subset of logging the recorder needs.
type Logger interface {
	Logf(format string, args ...any)
}

// Recorder owns the recording loop for one camera.
type Recorder struct {
	cfg    *config.File
	name   string
	log    Logger
	ffmpeg string
	logDir string
}

// New creates the recorder for camera name. ffmpeg may be "" to fall back to
// PATH.
func New(cfg *config.File, name string, log Logger, ffmpeg, logDir string) *Recorder {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	return &Recorder{cfg: cfg, name: name, log: log, ffmpeg: ffmpeg, logDir: logDir}
}

// Run dispatches to the active record mode, re-selecting when the mode
// changes (v1 main() catching ModeChanged).
func (r *Recorder) Run(ctx context.Context) {
	r.log.Logf("motion process ready (camera=%s)", r.name)
	for {
		if ctx.Err() != nil {
			return
		}
		cfg := r.cfg.Get()
		mode := cfg.Record.Mode
		if mode != "continuous" && mode != "combined" {
			mode = "event"
		}
		var err error
		switch mode {
		case "continuous":
			err = r.runContinuous(ctx)
		case "combined":
			err = r.runCombined(ctx)
		default:
			err = r.runEvent(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.log.Logf("recorder loop error: %v", err)
			return
		}
		r.log.Logf("recording mode changed -> %s", r.cfg.Get().Record.Mode)
	}
}

// ------------- shared ffmpeg command builders (motion.py ports) -------------

// base returns the shared RTSP input prefix for detector and recorders.
func (r *Recorder) base(cfg *config.Config, cam config.Camera) []string {
	return []string{
		r.ffmpeg, "-hide_banner", "-loglevel", "error",
		"-timeout", "8000000",
		"-rtsp_transport", "tcp", "-rtsp_flags", "prefer_tcp",
		"-i", cfg.CameraURL(cam),
	}
}

// audioInputArgs is the mic rec-port input used by recorders (motion.py
// spawn_event_recorder/spawn_continuous/spawn_combined).
func audioInputArgs(a config.Mic) []string {
	return []string{
		"-thread_queue_size", "512", "-f", "s16le",
		"-ar", strconv.Itoa(a.SampleRate), "-ac", strconv.Itoa(a.Channels),
		"-i", fmt.Sprintf("tcp://127.0.0.1:%d", a.RecPort),
	}
}

// audioAvailable probes whether the mic rec port is currently accepting
// clients (motion.py audio_available), so recorders include audio only when
// the mic feeder is actually up.
func audioAvailable(port int) bool {
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// drawtext renders the timestamped label for event/continuous recordings
// (motion.py drawtext). The strftime codes are passed through literally.
func drawtext(label string) string {
	return "drawtext=fontfile='C\\:/Windows/Fonts/arial.ttf'" +
		":fontsize=32:fontcolor=white:box=1:boxcolor=black@0.6:boxborderw=8:x=20:y=20" +
		fmt.Sprintf(":text='%s %%{localtime\\:%%Y-%%m-%%d %%H-%%M-%%S}'", label)
}

// drawtextTile renders the per-tile label for the combined recorder.
func drawtextTile(label string) string {
	return "drawtext=fontfile='C\\:/Windows/Fonts/arial.ttf'" +
		":fontsize=28:fontcolor=white:box=1:boxcolor=black@0.6:boxborderw=6:x=10:y=10" +
		fmt.Sprintf(":text='%s'", label)
}

// segmentArgs is the 600-second rotating-segment output (motion.py
// _segment_args), creating outDir first.
func segmentArgs(outDir, prefix string) []string {
	os.MkdirAll(outDir, 0o755)
	return []string{
		"-f", "segment", "-segment_time", "600", "-reset_timestamps", "1",
		"-strftime", "1", filepath.Join(outDir, prefix+"-%Y%m%d-%H%M%S.mp4"),
	}
}

// eventCmd builds the per-event recorder command (spawn_event_recorder).
func (r *Recorder) eventCmd(cfg *config.Config, cam config.Camera, path string) []string {
	hasAudio := cam.Mic.Enabled && audioAvailable(cam.Mic.RecPort)
	cmd := r.base(cfg, cam)
	if hasAudio {
		cmd = append(cmd, audioInputArgs(cam.Mic)...)
	}
	cmd = append(cmd, "-map", "0:v:0")
	if hasAudio {
		cmd = append(cmd, "-map", "1:a:0")
	}
	// Stream copy ("codec: copy", the low-RAM SBC path) cannot take a video
	// filter, so the timestamp label is dropped there.
	if codec := cfg.ResolvedCodec(cam.Record.Video.Codec); codec != "copy" {
		cmd = append(cmd, "-vf", drawtext(cam.Name))
	}
	cmd = append(cmd, config.VideoEncodeArgs(cfg, cam.Record.Video)...)
	if hasAudio {
		cmd = append(cmd, "-c:a", cam.Record.Audio.Codec, "-b:a", cam.Record.Audio.Bitrate)
	}
	cmd = append(cmd, "-movflags", "frag_keyframe+empty_moov", "-f", "mp4", path)
	return cmd
}

// preRollEventCmd builds the per-event recorder command when record.pre_roll_sec
// is on: the completed ring segments are spliced in front of the live feed
// through a re-encoding concat filter, so the recording starts pre_roll_sec
// before the trigger. Because the concat filter must re-encode, a "copy" codec
// setting degrades to libx264 here (as in combined mode). Only the live portion
// carries the drawtext timestamp label; pre-roll frames are left untouched.
// hasAudio is the caller's live mic availability probe result.
func (r *Recorder) preRollEventCmd(cfg *config.Config, cam config.Camera, path string, pre []string, hasAudio bool) []string {
	cmd := r.base(cfg, cam)
	for _, f := range pre {
		cmd = append(cmd, "-i", f)
	}
	mic := len(pre) + 1
	if hasAudio {
		cmd = append(cmd, audioInputArgs(cam.Mic)...)
	}

	fps := cam.FPS
	if fps <= 0 {
		fps = 15
	}
	fpsS := strconv.Itoa(fps)
	var parts []string
	for i := range pre {
		parts = append(parts, fmt.Sprintf("[%d:v:0]setpts=PTS,fps=%s[pr%d]", i+1, fpsS, i))
	}
	parts = append(parts, fmt.Sprintf("[0:v:0]setpts=PTS,fps=%s,%s[vL]", fpsS, drawtext(cam.Name)))
	parts = append(parts, joinTags("pr", len(pre))+"[vL]"+
		fmt.Sprintf("concat=n=%d:v=1:a=0[vout]", len(pre)+1))
	if hasAudio {
		parts = append(parts, fmt.Sprintf("[%d:a:0]anull[aout]", mic))
	}
	cmd = append(cmd, "-filter_complex", strings.Join(parts, ";"),
		"-map", "[vout]")
	if hasAudio {
		cmd = append(cmd, "-map", "[aout]")
	}
	v := cam.Record.Video
	if cfg.ResolvedCodec(v.Codec) == "copy" {
		v.Codec = "libx264"
	}
	cmd = append(cmd, config.VideoEncodeArgs(cfg, v)...)
	if hasAudio {
		cmd = append(cmd, "-c:a", cam.Record.Audio.Codec, "-b:a", cam.Record.Audio.Bitrate)
	}
	cmd = append(cmd, "-movflags", "frag_keyframe+empty_moov", "-f", "mp4", path)
	return cmd
}

// continuousCmd builds the rotating-segment recorder (spawn_continuous).
func (r *Recorder) continuousCmd(cfg *config.Config, cam config.Camera) []string {
	hasAudio := cam.Mic.Enabled && audioAvailable(cam.Mic.RecPort)
	cmd := r.base(cfg, cam)
	if hasAudio {
		cmd = append(cmd, audioInputArgs(cam.Mic)...)
	}
	cmd = append(cmd, "-map", "0:v:0")
	if hasAudio {
		cmd = append(cmd, "-map", "1:a:0")
	}
	if codec := cfg.ResolvedCodec(cam.Record.Video.Codec); codec != "copy" {
		cmd = append(cmd, "-vf", drawtext(cam.Name))
	}
	cmd = append(cmd, config.VideoEncodeArgs(cfg, cam.Record.Video)...)
	if hasAudio {
		cmd = append(cmd, "-c:a", cam.Record.Audio.Codec, "-b:a", cam.Record.Audio.Bitrate)
	}
	cmd = append(cmd, segmentArgs(cam.Record.OutDir, cam.Record.Prefix)...)
	return cmd
}

// combinedCmd builds the tiled xstack recorder (spawn_combined) over all
// record-enabled cameras, returning the owner's name for the err log.
func (r *Recorder) combinedCmd(cfg *config.Config) ([]string, string) {
	var cams []config.Camera
	for _, cam := range cfg.Cameras {
		if cam.Record.Enabled {
			cams = append(cams, cam)
		}
	}
	if len(cams) == 0 {
		return nil, ""
	}
	const tw, th = 960, 540
	cols := int(math.Ceil(math.Sqrt(float64(len(cams)))))

	var cmd []string
	var vin, ain []int
	inp := 0
	for _, cam := range cams {
		cmd = append(cmd, r.base(cfg, cam)...)
		vin = append(vin, inp)
		inp++
		if cam.Mic.Enabled && audioAvailable(cam.Mic.RecPort) {
			cmd = append(cmd, audioInputArgs(cam.Mic)...)
			ain = append(ain, inp)
			inp++
		}
	}

	var parts []string
	for i, v := range vin {
		parts = append(parts, fmt.Sprintf(
			"[%d:v:0]scale=%d:%d,fps=15,format=yuv420p,setsar=1,%s[v%d]",
			v, tw, th, drawtextTile(cams[i].Name), i))
	}
	layout := make([]string, len(vin))
	for i := range vin {
		layout[i] = fmt.Sprintf("%d_%d", (i%cols)*tw, (i/cols)*th)
	}
	parts = append(parts, joinTags("v", len(vin))+
		fmt.Sprintf("xstack=inputs=%d:layout=%s[vout]", len(vin), strings.Join(layout, "|")))
	if len(ain) > 0 {
		for j, an := range ain {
			parts = append(parts, fmt.Sprintf("[%d:a:0]anull[a%d]", an, j))
		}
		parts = append(parts, joinTags("a", len(ain))+
			fmt.Sprintf("amix=inputs=%d:normalize=0[aout]", len(ain)))
	}

	cmd = append(cmd, "-filter_complex", strings.Join(parts, ";"), "-map", "[vout]")
	if len(ain) > 0 {
		cmd = append(cmd, "-map", "[aout]")
	}
	// The combined recorder always re-encodes (xstack + labels), so a
	// "codec: copy" setting degrades to libx264 rather than erroring.
	v := cams[0].Record.Video
	if cfg.ResolvedCodec(v.Codec) == "copy" {
		v.Codec = "libx264"
	}
	cmd = append(cmd, config.VideoEncodeArgs(cfg, v)...)
	if len(ain) > 0 {
		cmd = append(cmd, "-c:a", "aac", "-b:a", "128k")
	}
	combinedDir := filepath.Join(cams[0].Record.OutDir, "combined")
	cmd = append(cmd, segmentArgs(combinedDir, "combined")...)
	return cmd, cams[0].Name
}

func joinTags(letter string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "[%s%d]", letter, i)
	}
	return b.String()
}

// spawn launches one recorder ffmpeg with stderr to <logDir>/recorder-<n>.err.log
// and stdin piped for the graceful 'q' stop.
func (r *Recorder) spawn(name string, argv []string) *proc.Child {
	ch := proc.NewChild("recorder-"+name, r.logDir)
	if err := ch.StartOpt(argv, proc.Options{Stderr: proc.StderrFile, Stdin: true}); err != nil {
		r.log.Logf("recorder start failed: %v", err)
		return nil
	}
	return ch
}

func (r *Recorder) spawnEvent(cfg *config.Config, cam config.Camera, path string) *proc.Child {
	return r.spawn(cam.Name, r.eventCmd(cfg, cam, path))
}

func (r *Recorder) spawnContinuous(cfg *config.Config, cam config.Camera) *proc.Child {
	return r.spawn(cam.Name, r.continuousCmd(cfg, cam))
}

func (r *Recorder) spawnCombined(cfg *config.Config) *proc.Child {
	argv, owner := r.combinedCmd(cfg)
	if argv == nil {
		return nil
	}
	return r.spawn(owner, argv)
}

// stopRecorder quits ffmpeg gracefully with 'q', then force-kills the tree if
// it does not exit within 10s (motion.py stop_recorder).
func stopRecorder(ch *proc.Child) {
	if ch == nil {
		return
	}
	ch.WriteStdin([]byte("q"))
	ch.CloseStdin()
	deadline := time.Now().Add(10 * time.Second)
	for !ch.Exited() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !ch.Exited() {
		ch.Stop()
	}
}

// eventName builds the finalized event filename: <prefix>-<activity>-<ts>.mp4,
// or with a -<n> suffix when the target exists (n>=2).
func eventName(prefix, activity, ts string, n int) string {
	if n < 2 {
		return fmt.Sprintf("%s-%s-%s.mp4", prefix, activity, ts)
	}
	return fmt.Sprintf("%s-%s-%s-%d.mp4", prefix, activity, ts, n)
}

// cleanup prunes old recordings for this camera (v1 motion.py cleanup), using
// the shared retention pass so messages land in this camera's motion log.
func (r *Recorder) cleanup(now time.Time) {
	cfg := r.cfg.Get()
	cam := cfg.Camera(r.name)
	if cam == nil {
		return
	}
	dirs := []string{cam.Record.OutDir}
	if cfg.Record.Mode == "combined" {
		dirs = append(dirs, filepath.Join(cam.Record.OutDir, "combined"))
	}
	retention.Run(func(msg string) { r.log.Logf("%s", msg) }, dirs, cfg.Record.RetainHours, cfg.Record.RetainMB, now)
}

// ------------- event mode (v1 run_event) -------------

func (r *Recorder) runEvent(ctx context.Context) error {
	r.log.Logf("motion detector starting (mode=event)")
	cfg := r.cfg.Get()
	cam := cfg.Camera(r.name)
	if cam == nil {
		return nil
	}
	m := cam.Motion

	det := motion.NewDetector(r.cfg, r.name, r.log, r.ffmpeg, r.logDir)
	sound := motion.NewSoundLevel(r.cfg, r.name, r.log)
	soundCtx, soundCancel := context.WithCancel(ctx)
	defer soundCancel()
	go sound.Run(soundCtx)

	detW, detH, detFPS := m.Width, m.Height, m.FPS
	if err := det.Start(detW, detH, detFPS); err != nil {
		r.log.Logf("detector start failed: %v", err)
		return err
	}
	defer det.Close()
	// Kill the detector pipe on shutdown so a blocked ReadFrame returns.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			det.Close()
		case <-done:
		}
	}()

	var (
		rec         *proc.Child
		stopPending bool
		eventStart  time.Time
		lastStart   time.Time
		curPath     string
		sawMotion   bool
		sawSound    bool
		finalTS     string
		lastMotion  time.Time
		lastSound   time.Time
		lastCleanup time.Time
		ring        *prerollRing
		lastPrune   time.Time
	)

	startEvent := func(trigger string) {
		now := time.Now()
		cfg := r.cfg.Get()
		cam := cfg.Camera(r.name)
		if cam == nil {
			return
		}
		if now.Sub(lastStart) < time.Duration(cam.Motion.RestartBackoff*float64(time.Second)) {
			return
		}
		if !cam.Record.Enabled {
			return
		}
		lastStart = now
		if cam.Record.PreRollSec > 0 {
			finalTS = now.Add(-time.Duration(cam.Record.PreRollSec) * time.Second).Format("20060102-150405")
		} else {
			finalTS = now.Format("20060102-150405")
		}
		name := cam.Record.Prefix + "-" + finalTS + ".mp4"
		os.MkdirAll(cam.Record.OutDir, 0o755)
		path := filepath.Join(cam.Record.OutDir, name)
		var rc *proc.Child
		if pre := ring.files(); len(pre) > 0 {
			r.log.Logf("pre-roll: splicing %d segment(s) into %s", len(pre), name)
			hasAudio := cam.Mic.Enabled && audioAvailable(cam.Mic.RecPort)
			rc = r.spawn(cam.Name, r.preRollEventCmd(cfg, *cam, path, pre, hasAudio))
		} else {
			r.log.Logf("pre-roll: no segments (ring=%v, pre_roll_sec=%d) -> live-only %s", ring != nil, cam.Record.PreRollSec, name)
			rc = r.spawnEvent(cfg, *cam, path)
		}
		if rc == nil {
			return
		}
		rec = rc
		stopPending = false
		eventStart = now
		curPath = path
		sawMotion = trigger == "motion"
		sawSound = trigger == "sound"
		r.log.Logf("event start [%s] -> %s", trigger, name)
	}

	finalize := func() {
		if curPath == "" {
			return
		}
		activity := "unknown"
		if sawMotion && sawSound {
			activity = "both"
		} else if sawMotion {
			activity = "motion"
		} else if sawSound {
			activity = "sound"
		}
		cfg := r.cfg.Get()
		cam := cfg.Camera(r.name)
		if cam == nil {
			return
		}
		newPath := filepath.Join(cam.Record.OutDir,
			eventName(cam.Record.Prefix, activity, finalTS, 1))
		suffix := 2
		for newPath != curPath {
			if _, err := os.Stat(newPath); err != nil {
				break
			}
			newPath = filepath.Join(cam.Record.OutDir,
				eventName(cam.Record.Prefix, activity, finalTS, suffix))
			suffix++
		}
		if newPath != curPath {
			if _, err := os.Stat(curPath); err == nil {
				if err := os.Rename(curPath, newPath); err != nil {
					newPath = curPath
				}
			}
		}
		curPath = ""
		r.log.Logf("event ended -> %s", filepath.Base(newPath))
	}

	stopEvent := func() {
		stopPending = true
		if rec != nil {
			stopRecorder(rec)
			rec = nil
		}
		finalize()
	}

	for {
		if ctx.Err() != nil {
			break
		}
		cfg := r.cfg.Get()
		cam := cfg.Camera(r.name)
		if cam == nil {
			break
		}
		if cfg.Record.Mode != "event" {
			return nil
		}
		m := cam.Motion

		if m.Width != detW || m.Height != detH || m.FPS != detFPS {
			r.log.Logf("detector config changed -> restarting detector")
			det.Close()
			if !sleepctx.Sleep(ctx, 2*time.Second) {
				break
			}
			if err := det.Start(m.Width, m.Height, m.FPS); err != nil {
				break
			}
			detW, detH, detFPS = m.Width, m.Height, m.FPS
			continue
		}

		frame, err := det.ReadFrame()
		if err != nil {
			det.Close()
			delay := time.Duration(m.RestartBackoff * float64(time.Second))
			if delay <= 0 {
				delay = 2 * time.Second
			}
			r.log.Logf("detector stream lost, restarting in %.0fs: %v", delay.Seconds(), err)
			if !sleepctx.Sleep(ctx, delay) {
				break
			}
			if err := det.Start(detW, detH, detFPS); err != nil {
				break
			}
			continue
		}
		moved := m.Enabled && det.Analyze(frame, m)

		now := time.Now()
		wantRing := cam.Record.Enabled && cam.Record.PreRollSec > 0
		if wantRing {
			if ring == nil {
				ring = r.startRing(*cam)
				if ring != nil {
					r.log.Logf("preroll ring started (dir=%s)", ring.dir)
				} else if !sleepctx.Sleep(ctx, 2*time.Second) {
					break
				}
			} else if ring.ch.Exited() {
				r.log.Logf("preroll ring exited code=%d, restarting", ring.ch.ExitCode())
				ring.ch = nil
				ring = nil
				if !sleepctx.Sleep(ctx, 2*time.Second) {
					break
				}
			} else if now.Sub(lastPrune) > 2*time.Second {
				ring.prune()
				lastPrune = now
			}
		} else if ring != nil {
			r.log.Logf("preroll disabled -> stopping ring")
			ring.stop()
			ring = nil
		}
		if !cam.Record.Enabled {
			if rec != nil {
				r.log.Logf("record disabled mid-event -> stopping")
				stopEvent()
			}
		}
		if moved {
			lastMotion = now
		}
		if sound.Triggered() {
			lastSound = now
		}

		postRoll := time.Duration(m.PostRollSec * float64(time.Second))
		if rec != nil {
			if moved {
				sawMotion = true
			}
			if sound.Triggered() {
				sawSound = true
			}
			if rec.Exited() {
				code := rec.ExitCode()
				rec = nil
				stopPending = false
				r.log.Logf("recorder exited code=%d", code)
				finalize()
				if now.Sub(lastMotion) <= postRoll || now.Sub(lastSound) <= postRoll {
					trigger := "sound"
					if moved {
						trigger = "motion"
					}
					startEvent(trigger)
				}
			} else if !stopPending {
				if now.Sub(eventStart) >= time.Duration(m.EventCapSec*float64(time.Second)) {
					r.log.Logf("event cap reached")
					stopEvent()
				} else if now.Sub(lastMotion) > postRoll && now.Sub(lastSound) > postRoll {
					r.log.Logf("activity ended")
					stopEvent()
				}
			}
		} else if moved || sound.Triggered() {
			trigger := "sound"
			if moved {
				trigger = "motion"
			}
			startEvent(trigger)
		}

		if now.Sub(lastCleanup) > 600*time.Second {
			lastCleanup = now
			r.cleanup(time.Now())
		}
	}

	if rec != nil {
		stopRecorder(rec)
		rec = nil
	}
	finalize()
	if ring != nil {
		ring.stop()
	}
	det.Close()
	r.log.Logf("motion detector stopped")
	return nil
}

// ------------- continuous / combined modes (v1 run_continuous / run_combined) -------------

func (r *Recorder) runContinuous(ctx context.Context) error {
	r.log.Logf("continuous recorder starting (mode=continuous)")
	var proc *proc.Child
	lastCleanup := time.Time{}
	for {
		if ctx.Err() != nil {
			break
		}
		cfg := r.cfg.Get()
		if cfg.Record.Mode != "continuous" {
			if proc != nil {
				stopRecorder(proc)
				proc = nil
			}
			r.log.Logf("continuous recorder stopping")
			return nil
		}
		cam := cfg.Camera(r.name)
		if cam == nil {
			return nil
		}
		if proc == nil || proc.Exited() {
			if proc != nil {
				r.log.Logf("continuous recorder exited code=%d", proc.ExitCode())
				proc = nil
			}
			if !sleepctx.Sleep(ctx, 2*time.Second) {
				break
			}
			proc = r.spawnContinuous(cfg, *cam)
			if proc != nil {
				r.log.Logf("continuous recorder started")
			}
		}
		now := time.Now()
		if now.Sub(lastCleanup) > 600*time.Second {
			lastCleanup = now
			r.cleanup(now)
		}
		if !sleepctx.Sleep(ctx, 5*time.Second) {
			break
		}
	}
	if proc != nil {
		stopRecorder(proc)
	}
	return nil
}

func (r *Recorder) runCombined(ctx context.Context) error {
	cfg := r.cfg.Get()
	owner := firstEnabledRecordIndex(cfg)
	if cameraIndex(cfg, r.name) != owner {
		r.log.Logf("combined mode: camera %s owns the combined recorder; waiting",
			cfg.Cameras[owner].Name)
		for {
			if ctx.Err() != nil {
				return nil
			}
			if r.cfg.Get().Record.Mode != "combined" {
				return nil
			}
			if !sleepctx.Sleep(ctx, 5*time.Second) {
				return nil
			}
		}
	}
	r.log.Logf("combined recorder starting (mode=combined)")
	var proc *proc.Child
	lastCleanup := time.Time{}
	for {
		if ctx.Err() != nil {
			break
		}
		cfg := r.cfg.Get()
		if cfg.Record.Mode != "combined" {
			if proc != nil {
				stopRecorder(proc)
				proc = nil
			}
			r.log.Logf("combined recorder stopping")
			return nil
		}
		if proc == nil || proc.Exited() {
			if proc != nil {
				r.log.Logf("combined recorder exited code=%d", proc.ExitCode())
				proc = nil
			}
			if !sleepctx.Sleep(ctx, 2*time.Second) {
				break
			}
			proc = r.spawnCombined(cfg)
			if proc != nil {
				r.log.Logf("combined recorder started")
			}
		}
		now := time.Now()
		if now.Sub(lastCleanup) > 600*time.Second {
			lastCleanup = now
			r.cleanup(now)
		}
		if !sleepctx.Sleep(ctx, 5*time.Second) {
			break
		}
	}
	if proc != nil {
		stopRecorder(proc)
	}
	return nil
}

// cameraIndex returns the index of the camera with name, falling back to 0 as
// in v1 config.py camera_index (callers only run for existing cameras).
func cameraIndex(cfg *config.Config, name string) int {
	for i, cam := range cfg.Cameras {
		if cam.Name == name {
			return i
		}
	}
	return 0
}

// firstEnabledRecordIndex returns the index of the first record-enabled camera
// (v1: the owner of the combined recorder, falling back to 0).
func firstEnabledRecordIndex(cfg *config.Config) int {
	for i, cam := range cfg.Cameras {
		if cam.Record.Enabled {
			return i
		}
	}
	return 0
}
