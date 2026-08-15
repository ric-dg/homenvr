package recorder

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ric-dg/homenvr/internal/config"
	"github.com/ric-dg/homenvr/internal/proc"
)

// prerollRing is the rolling pre-roll buffer for one camera in event mode.
// One cheap ffmpeg (`-c:v copy` into small mpegts segments) keeps the last
// pre_roll_sec of video so events can include what happened before the
// trigger. Only the video feed is buffered; the final recording still gets its
// audio from the live mic feed. The ring costs no encoding, a few MB of disk,
// and one extra RTSP subscriber. It runs in the .preroll-<prefix> subdirectory
// of the camera's out_dir, which retention skips (it only touches .mp4 files).
type prerollRing struct {
	dir  string
	keep int
	ch   *proc.Child
}

// startRing launches the ring writer for cam, clearing any stale segments
// first so the segment indices always restart at 0. It returns nil when
// ffmpeg could not start; callers retry on the next tick.
func (r *Recorder) startRing(cam config.Camera) *prerollRing {
	segSec := cam.Record.SegmentSec
	if segSec <= 0 {
		segSec = 2
	}
	keep := int(math.Ceil(float64(cam.Record.PreRollSec)/float64(segSec))) + 1
	dir := filepath.Join(cam.Record.OutDir, ".preroll-"+cam.Record.Prefix)
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0o755)
	cfg := r.cfg.Get()
	argv := r.base(cfg, cam)
	argv = append(argv,
		"-map", "0:v:0", "-c:v", "copy",
		"-f", "segment", "-segment_time", strconv.Itoa(segSec),
		"-segment_start_number", "0",
		filepath.Join(dir, "%08d.ts"),
	)
	ch := r.spawn("preroll-"+cam.Record.Prefix, argv)
	if ch == nil {
		return nil
	}
	return &prerollRing{dir: dir, keep: keep, ch: ch}
}

// files returns the fully-written pre-roll segments in chronological order:
// the still-open (newest) segment is dropped because it may be mid-write, and
// at most keep-1 segments are returned. Fewer segments mean the ring is still
// warming up; callers then record without pre-roll rather than stalling.
func (p *prerollRing) files() []string {
	if p == nil {
		return nil
	}
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return nil
	}
	var idx []int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".ts")); err == nil {
			idx = append(idx, n)
		}
	}
	sort.Ints(idx)
	if len(idx) == 0 {
		return nil
	}
	idx = idx[:len(idx)-1]
	if n := len(idx) - (p.keep - 1); n > 0 {
		idx = idx[n:]
	}
	out := make([]string, len(idx))
	for i, n := range idx {
		out[i] = filepath.Join(p.dir, fmt.Sprintf("%08d.ts", n))
	}
	return out
}

// prune deletes segments the ring no longer needs, keeping keep+2 files so
// the disk stays bounded even if the writer hiccups for a segment or two.
func (p *prerollRing) prune() {
	if p == nil || p.keep <= 0 {
		return
	}
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return
	}
	var idx []int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".ts")); err == nil {
			idx = append(idx, n)
		}
	}
	sort.Ints(idx)
	limit := len(idx) - (p.keep + 2)
	for i := 0; i < limit; i++ {
		os.Remove(filepath.Join(p.dir, fmt.Sprintf("%08d.ts", idx[i])))
	}
}

// stop quits the ring writer gracefully, closing the current segment.
func (p *prerollRing) stop() {
	if p == nil {
		return
	}
	if p.ch != nil {
		stopRecorder(p.ch)
	}
	p.ch = nil
}
