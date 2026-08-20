// Package motion provides the two activity gates for event recording:
// a background-subtraction motion detector fed by an ffmpeg rawvideo pipe, and
// a sound trigger fed by the mic feeder's per-block RMS (ctl port). Both
// mirror motion.py: weighted background accumulation, pixel-diff threshold,
// 3x3 elliptical open, then a min-pixel count; and the noise-floor/ratio/hold/
// drop RMS state machine.
package motion

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"

	"github.com/ric-dg/homenvr/internal/config"
	"github.com/ric-dg/homenvr/internal/proc"
)

// Logger is the subset of logging the detector needs.
type Logger interface {
	Logf(format string, args ...any)
}

// Detector pulls low-res grayscale frames from an ffmpeg rawvideo pipe and
// computes the motion gate. The background model adapts every frame
// (cv2.accumulateWeighted), so it must be warmed up (the first frame only
// initializes it, as in v1).
type Detector struct {
	cfg    *config.File
	name   string
	log    Logger
	ffmpeg string
	logDir string

	proc    *proc.Child
	bg      []float32
	th      []bool
	er      []bool
	w, h    int
	jpeg    bool   // true when Detection == "jpeg"
	zoneMask []bool
	zoneKey  string // tracks zone config for cache invalidation
}

// NewDetector creates a detector for camera name. ffmpeg may be "" to fall
// back to PATH.
func NewDetector(cfg *config.File, name string, log Logger, ffmpeg, logDir string) *Detector {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	return &Detector{cfg: cfg, name: name, log: log, ffmpeg: ffmpeg, logDir: logDir}
}

// Start launches the detector pipe at width/height/fps (v1 spawn_detector),
// resetting the background model. Any previous instance is stopped first.
// When cam.Detection is "jpeg", ffmpeg outputs MJPEG frames instead of rawvideo.
func (d *Detector) Start(w, h, fps int) error {
	d.Close()
	d.w, d.h = w, h
	d.bg = nil
	d.zoneMask = nil
	d.zoneKey = ""
	cfg := d.cfg.Get()
	cam := cfg.Camera(d.name)
	if cam == nil {
		return fmt.Errorf("camera %q not in config", d.name)
	}
	d.jpeg = cam.Detection == "jpeg"
	argv := []string{
		d.ffmpeg, "-hide_banner", "-loglevel", "error",
		"-timeout", "8000000",
		"-rtsp_transport", "tcp", "-rtsp_flags", "prefer_tcp",
		"-i", cfg.CameraURL(*cam),
		"-an",
	}
	if d.jpeg {
		argv = append(argv,
			"-vf", fmt.Sprintf("scale=%d:%d,fps=%d", w, h, fps),
			"-f", "mjpeg", "pipe:1",
		)
	} else {
		argv = append(argv,
			"-vf", fmt.Sprintf("scale=%d:%d,fps=%d,format=gray", w, h, fps),
			"-f", "rawvideo", "-pix_fmt", "gray", "pipe:1",
		)
	}
	ch := proc.NewChild("detector-"+d.name, d.logDir)
	if err := ch.StartOpt(argv, proc.Options{Stdout: proc.StdoutPipe}); err != nil {
		return err
	}
	d.proc = ch
	return nil
}

// ReadFrame reads one w*h grayscale frame. In rawvideo mode, it reads exactly
// w*h bytes. In JPEG mode, it reads one JPEG frame from the pipe, decodes it,
// scales to w*h, and converts to grayscale.
func (d *Detector) ReadFrame() ([]byte, error) {
	if d.proc == nil {
		return nil, fmt.Errorf("detector not started")
	}
	if d.jpeg {
		return d.readJPEGFrame()
	}
	buf := make([]byte, d.w*d.h)
	if _, err := io.ReadFull(d.proc.StdoutReader(), buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// readJPEGFrame reads one JPEG frame from the pipe (SOI..EOI) and decodes
// it to w*h grayscale bytes.
func (d *Detector) readJPEGFrame() ([]byte, error) {
	r := bufio.NewReader(d.proc.StdoutReader())
	// Scan for JPEG SOI marker (0xFF 0xD8).
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != 0xFF {
			continue
		}
		b, err = r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == 0xD8 {
			break // found SOI
		}
	}
	// Collect bytes from SOI until EOI (0xFF 0xD9).
	var buf bytes.Buffer
	buf.WriteString("\xff\xd8")
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		buf.WriteByte(b)
		if b == 0xFF {
			b, err = r.ReadByte()
			if err != nil {
				return nil, err
			}
			buf.WriteByte(b)
			if b == 0xD9 {
				break // found EOI
			}
		}
	}
	img, _, err := image.Decode(&buf)
	if err != nil {
		return nil, fmt.Errorf("jpeg decode: %w", err)
	}
	// Scale to d.w x d.h and convert to grayscale.
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	w, h := d.w, d.h
	out := make([]byte, w*h)
	for dy := 0; dy < h; dy++ {
		sy := bounds.Min.Y + dy*srcH/h
		for dx := 0; dx < w; dx++ {
			sx := bounds.Min.X + dx*srcW/w
			r, g, b, _ := img.At(sx, sy).RGBA()
			// ITU-R BT.601 luma
			out[dy*w+dx] = uint8((r*299 + g*587 + b*114 + 500) / 1000 >> 8)
		}
	}
	return out, nil
}

// Analyze updates the background model with frame and reports whether the
// frame counts as motion. The first frame only seeds the background.
// When zones are defined, only pixels inside at least one zone are counted.
func (d *Detector) Analyze(frame []byte, m config.Motion) bool {
	n := d.w * d.h
	if len(d.bg) == 0 {
		d.bg = make([]float32, n)
		for i, b := range frame {
			d.bg[i] = float32(b)
		}
		return false
	}
	if d.th == nil || len(d.th) != n {
		d.th = make([]bool, n)
		d.er = make([]bool, n)
	}
	bg, th, er := d.bg, d.th, d.er
	alpha := float32(m.BgAlpha)
	oneMinus := 1 - alpha
	thr := float32(m.Threshold)

	for i, b := range frame {
		v := oneMinus*bg[i] + alpha*float32(b)
		bg[i] = v
		df := float32(b) - v
		if df < 0 {
			df = -df
		}
		th[i] = df >= thr
	}

	// Morphological open (erode then dilate) with a 3x3 ellipse kernel
	// [0 1 0; 1 1 1; 0 1 0], interior pixels only (border effects are
	// irrelevant at this resolution).
	w, h := d.w, d.h
	for y := 1; y < h-1; y++ {
		row := y * w
		for x := 1; x < w-1; x++ {
			i := row + x
			er[i] = th[i] && th[i-w] && th[i+w] && th[i-1] && th[i+1]
		}
	}

	// Build per-pixel zone membership mask when zones are defined.
	// Cache across frames when zones and resolution haven't changed.
	if len(m.Zones) > 0 {
		key := fmt.Sprintf("%dx%d:%d", w, h, len(m.Zones))
		if key != d.zoneKey {
			d.zoneMask = make([]bool, n)
			for y := 0; y < h; y++ {
				ny := float64(y) / float64(h-1)
				for x := 0; x < w; x++ {
					nx := float64(x) / float64(w-1)
					for _, z := range m.Zones {
						if pointInZone(nx, ny, z.Points) {
							d.zoneMask[y*w+x] = true
							break
						}
					}
				}
			}
			d.zoneKey = key
		}
	} else {
		d.zoneMask = nil
		d.zoneKey = ""
	}

	count := 0
	for y := 1; y < h-1; y++ {
		row := y * w
		for x := 1; x < w-1; x++ {
			i := row + x
			if er[i] || er[i-w] || er[i+w] || er[i-1] || er[i+1] {
				if d.zoneMask != nil && !d.zoneMask[i] {
					continue
				}
				count++
			}
		}
	}
	return count >= m.MinPixels
}

// pointInZone tests whether (x,y) in normalized [0,1] coordinates lies inside
// the polygon defined by pts using the ray-casting algorithm.
func pointInZone(x, y float64, pts []config.Point) bool {
	n := len(pts)
	if n < 3 {
		return false
	}
	inside := false
	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := pts[i].X, pts[i].Y
		xj, yj := pts[j].X, pts[j].Y
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
		j = i
	}
	return inside
}

// Close stops the detector pipe (idempotent).
func (d *Detector) Close() {
	if d.proc != nil {
		d.proc.Stop()
		d.proc = nil
	}
}
