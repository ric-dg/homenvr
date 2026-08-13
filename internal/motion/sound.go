package motion

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/ric-dg/homenvr/internal/config"
	"github.com/ric-dg/homenvr/internal/sleepctx"
)

// SoundTracker is the pure RMS trigger state machine from motion.py's
// SoundListener: a noise floor adapted with tau while idle, a trigger when RMS
// stays above max(floor*ratio, abs_floor) for hold_blocks, released after
// drop_blocks. It is deliberately separate so it can be tested without sockets.
type SoundTracker struct {
	noiseFloor float64
	hold       int
	drop       int
	triggered  bool
}

// NewSoundTracker starts with v1's initial noise floor of 300.
func NewSoundTracker() *SoundTracker {
	return &SoundTracker{noiseFloor: 300.0}
}

// Update advances the machine with one RMS value and returns the new state.
func (t *SoundTracker) Update(rms uint16, s config.Sound) bool {
	if t.triggered {
		t.drop++
		if t.drop >= s.DropBlocks {
			t.triggered = false
			t.drop = 0
		}
	} else {
		t.noiseFloor = (1-s.Tau)*t.noiseFloor + s.Tau*float64(rms)
		thresh := t.noiseFloor * s.Ratio
		if s.AbsFloor > thresh {
			thresh = s.AbsFloor
		}
		if float64(rms) >= thresh {
			t.hold++
			if t.hold >= s.HoldBlocks {
				t.triggered = true
				t.hold = 0
			}
		} else {
			t.hold = 0
		}
	}
	return t.triggered
}

// SoundLevel reads per-block RMS values from the camera's ctl port and runs a
// SoundTracker, mirroring motion.py's SoundListener thread. It reconnects on
// failure and exits when the camera's sound/mic config disables it.
type SoundLevel struct {
	cfg  *config.File
	name string
	log  Logger

	mu        sync.Mutex
	rms       uint16
	triggered bool
	trk       *SoundTracker
}

// NewSoundLevel creates the sound listener for camera name.
func NewSoundLevel(cfg *config.File, name string, log Logger) *SoundLevel {
	return &SoundLevel{cfg: cfg, name: name, log: log, trk: NewSoundTracker()}
}

// Triggered reports the current gate state (thread-safe).
func (s *SoundLevel) Triggered() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.triggered
}

// Run feeds the tracker until ctx is cancelled.
func (s *SoundLevel) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		cfg := s.cfg.Get()
		cam := cfg.Camera(s.name)
		if cam == nil {
			return
		}
		if !cam.Sound.Enabled || !cam.Mic.Enabled {
			if !sleepctx.Sleep(ctx, time.Second) {
				return
			}
			continue
		}
		port := cam.Mic.CtlPort
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
		if err != nil {
			if !sleepctx.Sleep(ctx, 2*time.Second) {
				return
			}
			continue
		}
		err = s.readLoop(ctx, conn, port)
		conn.Close()
		if err != nil {
			if !sleepctx.Sleep(ctx, time.Second) {
				return
			}
		}
	}
}

// readLoop reads 2-byte RMS messages until the connection fails or the
// config no longer points at this port.
func (s *SoundLevel) readLoop(ctx context.Context, conn net.Conn, port int) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var buf [2]byte
		if _, err := io.ReadFull(conn, buf[:]); err != nil {
			return err
		}
		cfg := s.cfg.Get()
		cam := cfg.Camera(s.name)
		if cam == nil {
			return nil
		}
		if !cam.Sound.Enabled || cam.Mic.CtlPort != port {
			return nil
		}
		rms := binary.LittleEndian.Uint16(buf[:])
		trig := s.trk.Update(rms, cam.Sound)
		s.mu.Lock()
		s.rms = rms
		s.triggered = trig
		s.mu.Unlock()
	}
}
