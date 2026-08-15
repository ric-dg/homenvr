// Package mic captures each camera's microphone and fans the gained PCM out
// over TCP: live_port and rec_port carry the audio blocks for go2rtc /
// recordings, ctl_port carries one little-endian u16 RMS level per block for
// sound detection. On Windows the capture is native WASAPI, falling back to
// WDM-KS (kernel streaming) when the audio policy layer exposes no endpoint
// for the configured mic (the Brio mic is never a DirectShow device, so the
// ffmpeg -f dshow path cannot open it); other platforms capture via an ffmpeg
// PCM pipe. It replaces v1 mic_daemon.py without Python or sounddevice.
package mic

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/ric-dg/homenvr/internal/config"
	"github.com/ric-dg/homenvr/internal/sleepctx"
)

// captureSource is a raw s16le (config sample rate, mono) PCM producer. A
// readBlock fills buf completely or returns an error that causes the feeder
// to restart the capture, mirroring how a dying ffmpeg pipe restarted v1.
type captureSource interface {
	readBlock(buf []byte) error
	close()
}

// Logger is the subset of logging the feeder needs.
type Logger interface {
	Logf(format string, args ...any)
}

// Feeder captures one camera's microphone and serves its PCM/RMS ports.
// Gain hot-reloads per block; device/ports/format/enabled changes restart the
// capture (v1's mic daemon exited with code 3 on the same changes).
type Feeder struct {
	cfg    *config.File
	name   string
	log    Logger
	ffmpeg string
	logDir string

	mu        sync.Mutex
	curKey    string
	src       captureSource
	listeners []net.Listener
	clients   map[string]map[*client]bool
	quit      chan struct{}
}

type client struct {
	conn net.Conn
	ch   chan []byte
}

// New creates the feeder for camera name. ffmpeg may be "" to fall back to
// PATH (v1's `tools.ffmpeg or "ffmpeg"`).
func New(cfg *config.File, name string, log Logger, ffmpeg, logDir string) *Feeder {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	return &Feeder{
		cfg: cfg, name: name, log: log, ffmpeg: ffmpeg, logDir: logDir,
		clients: map[string]map[*client]bool{
			"live": {}, "rec": {}, "ctl": {},
		},
		quit: make(chan struct{}),
	}
}

// Run feeds PCM until ctx is cancelled or the camera disappears from config.
func (f *Feeder) Run(ctx context.Context) {
	f.log.Logf("mic capture [%s] starting", f.name)
	defer close(f.quit)
	go func() {
		select {
		case <-ctx.Done():
			f.stopAll()
		case <-f.quit:
		}
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		cfg := f.cfg.Get()
		cam := cfg.Camera(f.name)
		if cam == nil {
			f.log.Logf("mic capture [%s] camera not in config, exiting", f.name)
			return
		}
		mic := cam.Mic
		if !mic.Enabled {
			if f.curKey != "" {
				f.log.Logf("mic [%s] disabled -> stopping mic capture", f.name)
				f.stopAll()
				f.curKey = ""
			}
			sleepctx.Sleep(ctx, time.Second)
			continue
		}
		key := micKey(f.ffmpeg, mic)
		if key != f.curKey {
			if f.curKey != "" {
				f.log.Logf("mic [%s] config changed -> restarting capture", f.name)
			}
			f.stopAll()
			f.curKey = key
			if err := f.start(mic); err != nil {
				f.log.Logf("mic [%s] start failed: %v (retrying in 2s)", f.name, err)
				f.curKey = ""
				sleepctx.Sleep(ctx, 2*time.Second)
				continue
			}
			f.log.Logf("mic capture [%s] started device=%s ports=[live %d rec %d ctl %d] gain=%.1f",
				f.name, mic.DeviceName, mic.LivePort, mic.RecPort, mic.CtlPort, mic.Gain)
		}
		if f.src == nil {
			f.log.Logf("mic [%s] capture stream lost, restarting in 2s", f.name)
			f.stopAll()
			f.curKey = ""
			sleepctx.Sleep(ctx, 2*time.Second)
			continue
		}

		block := make([]byte, mic.BlockSize*mic.Channels*2)
		if err := f.src.readBlock(block); err != nil {
			f.log.Logf("mic [%s] capture read failed: %v", f.name, err)
			f.stopAll()
			f.curKey = ""
			sleepctx.Sleep(ctx, 2*time.Second)
			continue
		}

		cam = cfg.Camera(f.name)
		if cam == nil {
			return
		}
		gained := applyGain(int16s(block), cam.Mic.Gain)
		rms := blockRMS(gained)
		f.fanout("live", bytes16(gained))
		f.fanout("rec", bytes16(gained))
		var ctl [2]byte
		binary.LittleEndian.PutUint16(ctl[:], rms)
		f.fanout("ctl", ctl[:])
	}
}

// start opens the capture source and the three TCP servers.
func (f *Feeder) start(mic config.Mic) error {
	if mic.DeviceName == "" {
		return fmt.Errorf("mic device_name is empty")
	}
	src, err := newCapture(f.cfg, f.name, f.log, f.ffmpeg, f.logDir, mic)
	if err != nil {
		return err
	}
	var lns []net.Listener
	for _, p := range []struct {
		kind string
		port int
	}{{"live", mic.LivePort}, {"rec", mic.RecPort}, {"ctl", mic.CtlPort}} {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port)))
		if err != nil {
			for _, l := range lns {
				l.Close()
			}
			src.close()
			return err
		}
		lns = append(lns, ln)
		go f.acceptLoop(ln, p.kind)
	}
	f.src = src
	f.listeners = lns
	return nil
}

// stopAll idempotently stops the capture, listeners and clients.
func (f *Feeder) stopAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.src != nil {
		f.src.close()
		f.src = nil
	}
	for _, l := range f.listeners {
		l.Close()
	}
	f.listeners = nil
	for kind, set := range f.clients {
		for cl := range set {
			cl.conn.Close()
		}
		f.clients[kind] = map[*client]bool{}
	}
}

func (f *Feeder) acceptLoop(ln net.Listener, kind string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go f.serveClient(kind, conn)
	}
}

// serveClient drains a per-client queue (capacity 128, mirroring v1's
// queue.Queue(maxsize=128)) to the socket, dropping the client on error.
func (f *Feeder) serveClient(kind string, conn net.Conn) {
	ch := make(chan []byte, 128)
	cl := &client{conn: conn, ch: ch}
	f.mu.Lock()
	f.clients[kind][cl] = true
	f.mu.Unlock()
	defer func() {
		conn.Close()
		f.mu.Lock()
		delete(f.clients[kind], cl)
		f.mu.Unlock()
	}()

	for b := range ch {
		if _, err := conn.Write(b); err != nil {
			return
		}
	}
}

// fanout delivers b to every client of kind, dropping when a queue is full
// (mirroring v1 put_nowait / except queue.Full).
func (f *Feeder) fanout(kind string, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for cl := range f.clients[kind] {
		select {
		case cl.ch <- b:
		default:
		}
	}
}

// micKey covers everything that needs a capture restart (v1's structural
// change check), including the ffmpeg binary itself.
func micKey(ffmpeg string, m config.Mic) string {
	return fmt.Sprintf("%s|%v|%s|%s|%d|%d|%d|%d|%d|%d",
		ffmpeg, m.Enabled, m.Backend, m.DeviceName, m.SampleRate, m.Channels,
		m.BlockSize, m.LivePort, m.RecPort, m.CtlPort)
}

// int16s reinterprets little-endian PCM bytes as samples.
func int16s(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return out
}

// bytes16 packs samples back into little-endian PCM bytes.
func bytes16(s []int16) []byte {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

// applyGain applies the digital mic gain and clips to int16 range, mirroring
// v1's np.clip(indata.astype(np.int32) * gain, -32768, 32767).astype(np.int16).
func applyGain(samples []int16, gain float64) []int16 {
	out := make([]int16, len(samples))
	for i, s := range samples {
		v := float64(int32(s)) * gain
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		out[i] = int16(int32(v))
	}
	return out
}

// blockRMS computes the per-block RMS packed as a u16, mirroring v1's
// int(np.sqrt(np.mean(samples.astype(np.float32) ** 2))) capped at 65535.
// The float32 accumulation matters: at full scale (32767) numpy's float32
// arithmetic rounds the squared mean up so int() yields 32767, not 32766.
func blockRMS(samples []int16) uint16 {
	var sum float32
	for _, s := range samples {
		f := float32(s)
		sum += f * f
	}
	r := int(float32(math.Sqrt(float64(sum / float32(len(samples))))))
	if r > 65535 {
		r = 65535
	}
	return uint16(r)
}
