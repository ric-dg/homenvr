package web

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// MicCalResult holds the analysis from a mic calibration sample.
type MicCalResult struct {
	Samples  int     `json:"samples"`
	Duration float64 `json:"duration_sec"`
	Min      uint16  `json:"min"`
	Max      uint16  `json:"max"`
	Mean     float64 `json:"mean"`
	P5       uint16  `json:"p5"`
	P50      uint16  `json:"p50"`
	P95      uint16  `json:"p95"`
	P99      uint16  `json:"p99"`
	NoiseFloor uint16 `json:"noise_floor"`
	PeakLvl  uint16  `json:"peak_level"`
	SugGain  float64 `json:"suggested_gain"`
	SugGate  uint16  `json:"suggested_gate"`
}

// GET /api/mic-calibrate?cam=brio&dur=3
func (s *Server) handleMicCalibrate(w http.ResponseWriter, r *http.Request) {
	camName := r.URL.Query().Get("cam")
	if camName == "" {
		camName = "brio"
	}
	durSec := 3.0
	if v := r.URL.Query().Get("dur"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0.5 && f <= 10 {
			durSec = f
		}
	}

	cfg := s.cfg.Get()
	cam := cfg.Camera(camName)
	if cam == nil {
		writeErr(w, http.StatusNotFound, "camera not found")
		return
	}
	if !cam.Mic.Enabled || cam.Mic.CtlPort == 0 {
		writeErr(w, http.StatusBadRequest, "mic not enabled for camera")
		return
	}

	// Connect to ctl_port.
	addr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("127.0.0.1:%d", cam.Mic.CtlPort))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "resolve ctl port: "+err.Error())
		return
	}
	tc, err := net.DialTCP("tcp", nil, addr)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "cannot connect to mic ctl port (is the mic running?)")
		return
	}
	defer tc.Close()

	// Read RMS values for durSec. Discard the first 30 blocks (~600ms) as
	// warmup for the DC blocker to converge on large offsets.
	deadline := time.Now().Add(time.Duration(durSec * float64(time.Second)))
	var rmsVals []uint16
	buf := make([]byte, 2)
	warmup := 30
	for time.Now().Before(deadline) {
		tc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := io.ReadFull(tc, buf); err != nil {
			continue
		}
		if warmup > 0 {
			warmup--
			continue
		}
		rmsVals = append(rmsVals, binary.LittleEndian.Uint16(buf))
	}

	if len(rmsVals) < 30 {
		writeErr(w, http.StatusServiceUnavailable, "too few samples collected (mic may be too slow or DC blocker still settling)")
		return
	}

	sort.Slice(rmsVals, func(i, j int) bool { return rmsVals[i] < rmsVals[j] })

	// Compute stats.
	var sum float64
	for _, v := range rmsVals {
		sum += float64(v)
	}
	mean := sum / float64(len(rmsVals))

	pct := func(p float64) uint16 {
		idx := int(math.Round(p * float64(len(rmsVals)-1)))
		if idx >= len(rmsVals) {
			idx = len(rmsVals) - 1
		}
		return rmsVals[idx]
	}

	noiseFloor := pct(0.05)
	peak := pct(0.99)
	if peak == 0 {
		peak = 1
	}

	// Suggest gain: target speech peaks at ~20000 (60% of full scale).
	const targetPeak = 20000.0
	gain := targetPeak / float64(peak)
	if gain < 1 {
		gain = 1
	}
	if gain > 10 {
		gain = 10
	}
	gain = math.Round(gain*10) / 10

	// Suggest gate: 1.5× noise floor, clamped to [200, 5000]. Too low gates
	// nothing; too high mutes speech. 5000 is ~30% of full scale — safe max.
	gate := uint16(float64(noiseFloor) * 1.5)
	if gate < 200 {
		gate = 200
	}
	if gate > 5000 {
		gate = 5000
	}

	result := MicCalResult{
		Samples:    len(rmsVals),
		Duration:   durSec,
		Min:        rmsVals[0],
		Max:        rmsVals[len(rmsVals)-1],
		Mean:       math.Round(mean*10) / 10,
		P5:         pct(0.05),
		P50:        pct(0.50),
		P95:        pct(0.95),
		P99:        pct(0.99),
		NoiseFloor: noiseFloor,
		PeakLvl:    peak,
		SugGain:    gain,
		SugGate:    gate,
	}
	writeJSON(w, result)
}
