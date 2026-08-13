//go:build !windows

package mic

import (
	"fmt"
	"io"

	"github.com/ric-dg/homenvr/internal/config"
	"github.com/ric-dg/homenvr/internal/proc"
)

// ffmpegSrc captures PCM via an ffmpeg pipe (e.g. ALSA/Pulse on Linux,
// CoreAudio via a helper on macOS). Windows uses the WASAPI source instead
// because DirectShow exposes no audio device for the supported webcams.
type ffmpegSrc struct {
	ch *proc.Child
}

func (s *ffmpegSrc) readBlock(buf []byte) error {
	_, err := io.ReadFull(s.ch.StdoutReader(), buf)
	return err
}

func (s *ffmpegSrc) close() {
	s.ch.Stop()
}

// newCapture starts an ffmpeg PCM capture for camera mic. The device is
// addressed with the platform-native input syntax.
func newCapture(_ *config.File, name string, log Logger, ffmpeg, logDir string, mic config.Mic) (captureSource, error) {
	argv := []string{
		ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "dshow", "-i", "audio=" + mic.DeviceName,
		"-ar", fmt.Sprint(mic.SampleRate),
		"-ac", fmt.Sprint(mic.Channels),
		"-f", "s16le", "pipe:1",
	}
	ch := proc.NewChild("daemon-"+name, logDir)
	if err := ch.StartOpt(argv, proc.Options{Stdout: proc.StdoutPipe, Stderr: proc.StderrFile}); err != nil {
		return nil, err
	}
	log.Logf("mic [%s] ffmpeg capture started", name)
	return &ffmpegSrc{ch: ch}, nil
}
