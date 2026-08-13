//go:build windows

// WASAPI (Core Audio) capture backend. The supported webcams only expose
// their mic as a WASAPI endpoint (e.g. "Microphone (Brio 100)") and never as
// a DirectShow audio device, so ffmpeg -f dshow cannot open it. This mirrors
// v1's sounddevice (PortAudio over WASAPI) with no third-party modules: pure
// syscall COM bindings for IMMDeviceEnumerator/IMMDevice/IAudioClient/
// IAudioCaptureClient/IPropertyStore, event-driven shared-mode capture of the
// mix format, downmix to mono, float->int16, and linear resample to the
// configured sample rate.
package mic

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ric-dg/homenvr/internal/config"
)

// ---- Windows API plumbing --------------------------------------------------

var (
	ole32            = syscall.NewLazyDLL("ole32.dll")
	coCreateInstance = ole32.NewProc("CoCreateInstance")
	coInitializeEx   = ole32.NewProc("CoInitializeEx")
	coTaskMemFree    = ole32.NewProc("CoTaskMemFree")

	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	createEventW        = kernel32.NewProc("CreateEventW")
	waitForMultipleObjs = kernel32.NewProc("WaitForMultipleObjects")
	closeHandle         = kernel32.NewProc("CloseHandle")
)

// createEvent wraps CreateEventW: manual=false auto-resets, initial=0 clears.
func createEvent(manual, initial int) syscall.Handle {
	r, _, _ := createEventW.Call(0, uintptr(manual), uintptr(initial), 0)
	return syscall.Handle(r)
}

func setEvent(h uintptr) {
	kernel32.NewProc("SetEvent").Call(h)
}

const (
	coinitMultithreaded = 0x0
	clsctxInprocServer  = 0x1
	eCapture            = 0x1
	devstateActive      = 0x00000001
	shareModeShared     = 0
	streamEventCallback = 0x00040000
	stgmRead            = 0x00000000
	vtLPWSTR            = 31
	infinite            = 0xFFFFFFFF

	waveFormatPCM        = 0x0001
	waveFormatIEEEFloat  = 0x0003
	waveFormatExtensible = 0xFFFE
)

// guid is the 16-byte in-memory layout of a COM GUID (little-endian DWORD1,
// WORD2, WORD3, then the byte array).
type guid [16]byte

var (
	clsidMMDeviceEnumerator = guid{0x95, 0x03, 0xDE, 0xBC, 0x2F, 0xE5, 0x7C, 0x46, 0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}
	iidMMDeviceEnumerator   = guid{0xD2, 0x64, 0x56, 0xA9, 0x14, 0x96, 0x35, 0x4F, 0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}
	iidMMDevice             = guid{0x3F, 0x06, 0x66, 0xD6, 0x87, 0x15, 0x43, 0x4E, 0x81, 0xF1, 0xB9, 0x48, 0xE8, 0x07, 0x36, 0x3F}
	iidAudioClient          = guid{0x4C, 0xAD, 0xB9, 0x1C, 0xFA, 0xDB, 0x32, 0x4C, 0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}
	iidAudioCaptureClient   = guid{0x64, 0xBD, 0xAD, 0xC8, 0x1E, 0xE7, 0xA0, 0x48, 0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}
	iidPropertyStore        = guid{0xEB, 0x8E, 0x6D, 0x88, 0xF2, 0x8C, 0x46, 0x44, 0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}
	subtypeIEEEFloat        = guid{0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}
	subtypePCM              = guid{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}
)

type propertyKey struct {
	fmtID guid
	pid   uint32
}

var pkeyDeviceFriendlyName = propertyKey{
	fmtID: guid{0x4E, 0x25, 0x5C, 0xA4, 0x1C, 0xDF, 0xFD, 0x4E, 0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0},
	pid:   14,
}

type propVariant struct {
	vt   uint16
	r1   uint16
	r2   uint16
	r3   uint16
	val  uintptr
	val2 uintptr
	val3 uintptr
}

type waveFormatEx struct {
	wFormatTag      uint16
	nChannels       uint16
	nSamplesPerSec  uint32
	nAvgBytesPerSec uint32
	nBlockAlign     uint16
	wBitsPerSample  uint16
	cbSize          uint16
}

// ---- COM interfaces --------------------------------------------------------

type iMMDeviceEnumerator struct{ lpVtbl *iMMDeviceEnumeratorVtbl }

type iMMDeviceEnumeratorVtbl struct {
	queryInterface                         uintptr
	addRef                                 uintptr
	release                                uintptr
	enumAudioEndpoints                     uintptr
	getDefaultAudioEndpoint                uintptr
	getDevice                              uintptr
	registerEndpointNotificationCallback   uintptr
	unregisterEndpointNotificationCallback uintptr
}

func (e *iMMDeviceEnumerator) enumAudioEndpoints(dataFlow, stateMask uint32, devs **iMMDeviceCollection) uint32 {
	r, _, _ := syscall.SyscallN(e.lpVtbl.enumAudioEndpoints, uintptr(unsafe.Pointer(e)), uintptr(dataFlow), uintptr(stateMask), uintptr(unsafe.Pointer(devs)))
	return uint32(r)
}

func (e *iMMDeviceEnumerator) release() {
	syscall.SyscallN(e.lpVtbl.release, uintptr(unsafe.Pointer(e)))
}

type iMMDeviceCollection struct{ lpVtbl *iMMDeviceCollectionVtbl }

type iMMDeviceCollectionVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	getCount       uintptr
	item           uintptr
}

func (c *iMMDeviceCollection) getCount(n *uint32) uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.getCount, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(n)))
	return uint32(r)
}

func (c *iMMDeviceCollection) item(i uint32, dev **iMMDevice) uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.item, uintptr(unsafe.Pointer(c)), uintptr(i), uintptr(unsafe.Pointer(dev)))
	return uint32(r)
}

func (c *iMMDeviceCollection) release() {
	syscall.SyscallN(c.lpVtbl.release, uintptr(unsafe.Pointer(c)))
}

type iMMDevice struct{ lpVtbl *iMMDeviceVtbl }

type iMMDeviceVtbl struct {
	queryInterface    uintptr
	addRef            uintptr
	release           uintptr
	activate          uintptr
	openPropertyStore uintptr
	getID             uintptr
	getState          uintptr
}

func (d *iMMDevice) activate(iid *guid, ctx uint32, ppv *uintptr) uint32 {
	r, _, _ := syscall.SyscallN(d.lpVtbl.activate, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(iid)), uintptr(ctx), 0, uintptr(unsafe.Pointer(ppv)))
	return uint32(r)
}

func (d *iMMDevice) openPropertyStore(access uint32, ps **iPropertyStore) uint32 {
	r, _, _ := syscall.SyscallN(d.lpVtbl.openPropertyStore, uintptr(unsafe.Pointer(d)), uintptr(access), uintptr(unsafe.Pointer(ps)))
	return uint32(r)
}

func (d *iMMDevice) release() {
	syscall.SyscallN(d.lpVtbl.release, uintptr(unsafe.Pointer(d)))
}

type iPropertyStore struct{ lpVtbl *iPropertyStoreVtbl }

type iPropertyStoreVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	getCount       uintptr
	getAt          uintptr
	getValue       uintptr
	setValue       uintptr
	commit         uintptr
}

func (s *iPropertyStore) getValue(key *propertyKey, pv *propVariant) uint32 {
	r, _, _ := syscall.SyscallN(s.lpVtbl.getValue, uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(pv)))
	return uint32(r)
}

func (s *iPropertyStore) release() {
	syscall.SyscallN(s.lpVtbl.release, uintptr(unsafe.Pointer(s)))
}

type iAudioClient struct{ lpVtbl *iAudioClientVtbl }

type iAudioClientVtbl struct {
	queryInterface    uintptr
	addRef            uintptr
	release           uintptr
	initialize        uintptr
	getBufferSize     uintptr
	getStreamLatency  uintptr
	getCurrentPadding uintptr
	isFormatSupported uintptr
	getMixFormat      uintptr
	getDevicePeriod   uintptr
	start             uintptr
	stop              uintptr
	reset             uintptr
	setEventHandle    uintptr
	getService        uintptr
}

func (c *iAudioClient) initialize(shareMode, flags, dur, period uint32, format *waveFormatEx) uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.initialize, uintptr(unsafe.Pointer(c)), uintptr(shareMode), uintptr(flags), uintptr(dur), uintptr(period), uintptr(unsafe.Pointer(format)), 0)
	return uint32(r)
}

func (c *iAudioClient) getMixFormat(fmt **waveFormatEx) uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.getMixFormat, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(fmt)))
	return uint32(r)
}

func (c *iAudioClient) setEventHandle(h uintptr) uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.setEventHandle, uintptr(unsafe.Pointer(c)), h)
	return uint32(r)
}

func (c *iAudioClient) getService(iid *guid, ppv *uintptr) uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.getService, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(ppv)))
	return uint32(r)
}

func (c *iAudioClient) start() uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.start, uintptr(unsafe.Pointer(c)))
	return uint32(r)
}

func (c *iAudioClient) stop() uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.stop, uintptr(unsafe.Pointer(c)))
	return uint32(r)
}

func (c *iAudioClient) release() {
	syscall.SyscallN(c.lpVtbl.release, uintptr(unsafe.Pointer(c)))
}

type iAudioCaptureClient struct{ lpVtbl *iAudioCaptureClientVtbl }

type iAudioCaptureClientVtbl struct {
	queryInterface    uintptr
	addRef            uintptr
	release           uintptr
	getBuffer         uintptr
	releaseBuffer     uintptr
	getNextPacketSize uintptr
}

func (c *iAudioCaptureClient) getNextPacketSize(frames *uint32) uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.getNextPacketSize, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(frames)))
	return uint32(r)
}

func (c *iAudioCaptureClient) getBuffer(data **byte, frames *uint32) uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.getBuffer, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(data)), uintptr(unsafe.Pointer(frames)))
	return uint32(r)
}

func (c *iAudioCaptureClient) releaseBuffer(frames uint32) uint32 {
	r, _, _ := syscall.SyscallN(c.lpVtbl.releaseBuffer, uintptr(unsafe.Pointer(c)), uintptr(frames))
	return uint32(r)
}

func (c *iAudioCaptureClient) release() {
	syscall.SyscallN(c.lpVtbl.release, uintptr(unsafe.Pointer(c)))
}

// ---- WASAPI capture source -------------------------------------------------

// wasapiSrc is an event-driven shared-mode WASAPI capture that produces raw
// s16le mono at the configured sample rate, one block per readBlock.
type wasapiSrc struct {
	log Logger
	mic config.Mic

	cl             *iAudioClient
	cap            *iAudioCaptureClient
	evData, evStop syscall.Handle

	mu     sync.Mutex
	closed bool

	mixRate, mixCh uint32
	kind           int // 0 float32, 1 int16, 2 int32

	in     []float32 // mono mix-rate samples pending resample
	inPos  float64
	ratio  float64
	out    []int16 // mono output-rate samples pending block
	outOff int
}

func newCapture(_ *config.File, name string, log Logger, _ string, _ string, mic config.Mic) (captureSource, error) {
	if mic.DeviceName == "" {
		return nil, fmt.Errorf("mic device_name is empty")
	}
	if hr, _, _ := coInitializeEx.Call(0, coinitMultithreaded); hr != 0 && hr != 1 /*S_FALSE*/ && hr != 0x80010106 /*RPC_E_CHANGED_MODE*/ {
		return nil, fmt.Errorf("CoInitializeEx: 0x%08x", hr)
	}

	var e *iMMDeviceEnumerator
	if hr, _, _ := coCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidMMDeviceEnumerator)), uintptr(unsafe.Pointer(&e))); hr != 0 {
		return nil, fmt.Errorf("CoCreateInstance(MMDeviceEnumerator): 0x%08x", hr)
	}

	var col *iMMDeviceCollection
	if hr := e.enumAudioEndpoints(eCapture, devstateActive, &col); hr != 0 {
		e.release()
		return nil, fmt.Errorf("EnumAudioEndpoints: 0x%08x", hr)
	}

	want := strings.ToLower(mic.DeviceName)
	var dev *iMMDevice
	var n uint32
	col.getCount(&n)
	for i := uint32(0); i < n; i++ {
		var d *iMMDevice
		if hr := col.item(i, &d); hr != 0 {
			continue
		}
		name := friendlyName(d)
		if name != "" && strings.Contains(strings.ToLower(name), want) {
			dev = d
			break
		}
		d.release()
	}
	col.release()
	if dev == nil {
		e.release()
		return nil, fmt.Errorf("no WASAPI capture device matches %q", mic.DeviceName)
	}

	var cl *iAudioClient
	if hr := dev.activate(&iidAudioClient, clsctxInprocServer, (*uintptr)(unsafe.Pointer(&cl))); hr != 0 {
		dev.release()
		e.release()
		return nil, fmt.Errorf("IAudioClient activate: 0x%08x", hr)
	}

	var wfx *waveFormatEx
	if hr := cl.getMixFormat(&wfx); hr != 0 {
		cl.release()
		dev.release()
		e.release()
		return nil, fmt.Errorf("GetMixFormat: 0x%08x", hr)
	}
	src := &wasapiSrc{log: log, mic: mic, cl: cl}
	if err := src.analyzeFormat(wfx); err != nil {
		coTaskMemFree.Call(uintptr(unsafe.Pointer(wfx)))
		cl.release()
		dev.release()
		e.release()
		return nil, err
	}
	if hr := cl.initialize(shareModeShared, streamEventCallback, 0, 0, wfx); hr != 0 {
		coTaskMemFree.Call(uintptr(unsafe.Pointer(wfx)))
		cl.release()
		dev.release()
		e.release()
		return nil, fmt.Errorf("Initialize: 0x%08x", hr)
	}
	coTaskMemFree.Call(uintptr(unsafe.Pointer(wfx)))

	evData := createEvent(0, 0)
	evStop := createEvent(1, 0)
	if evData == 0 || evStop == 0 {
		if evData != 0 {
			closeHandle.Call(uintptr(evData))
		}
		if evStop != 0 {
			closeHandle.Call(uintptr(evStop))
		}
		cl.release()
		dev.release()
		e.release()
		return nil, fmt.Errorf("CreateEventW failed")
	}
	src.evData, src.evStop = evData, evStop

	fail := func(hr uint32, what string) (captureSource, error) {
		cl.release()
		dev.release()
		e.release()
		closeHandle.Call(uintptr(evData))
		closeHandle.Call(uintptr(evStop))
		return nil, fmt.Errorf("%s: 0x%08x", what, hr)
	}
	if hr := cl.setEventHandle(uintptr(evData)); hr != 0 {
		return fail(hr, "SetEventHandle")
	}
	var cp *iAudioCaptureClient
	if hr := cl.getService(&iidAudioCaptureClient, (*uintptr)(unsafe.Pointer(&cp))); hr != 0 {
		return fail(hr, "GetService(IAudioCaptureClient)")
	}
	src.cap = cp

	if hr := cl.start(); hr != 0 {
		cp.release()
		return fail(hr, "Start")
	}
	dev.release()
	e.release()

	log.Logf("mic [%s] WASAPI capture device=%s rate=%d ch=%d", name, mic.DeviceName, src.mixRate, src.mixCh)
	return src, nil
}

// analyzeFormat classifies the mix format and sets the resample ratio. For
// WAVEFORMATEXTENSIBLE the actual tag is the Data1 of the subformat GUID
// (3 = IEEE float, 1 = PCM); the base wFormatTag stays 0xFFFE.
func (s *wasapiSrc) analyzeFormat(wfx *waveFormatEx) error {
	sub := subtypePCM
	if wfx.wFormatTag == waveFormatExtensible {
		// WAVEFORMATEXTENSIBLE: wValidBits(2) + dwChannelMask(4) pad the base
		// struct, then the subformat GUID at byte offset 24.
		for i := 0; i < 16; i++ {
			sub[i] = *(*byte)(unsafe.Add(unsafe.Pointer(wfx), 24+i))
		}
	}
	tag := uint16(sub[0]) | uint16(sub[1])<<8
	s.mixRate = wfx.nSamplesPerSec
	s.mixCh = uint32(wfx.nChannels)
	switch {
	case tag == waveFormatIEEEFloat || sub == subtypeIEEEFloat:
		s.kind = 0
	case tag == waveFormatPCM && sub == subtypePCM && wfx.wBitsPerSample == 16:
		s.kind = 1
	case tag == waveFormatPCM && sub == subtypePCM && wfx.wBitsPerSample == 32:
		s.kind = 2
	default:
		return fmt.Errorf("unsupported mix format tag=0x%x bits=%d", tag, wfx.wBitsPerSample)
	}
	if s.mixRate == 0 || s.mixCh == 0 {
		return fmt.Errorf("bad mix format rate=%d ch=%d", s.mixRate, s.mixCh)
	}
	s.ratio = float64(s.mixRate) / float64(s.mic.SampleRate)
	return nil
}

func (s *wasapiSrc) readBlock(buf []byte) error {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return fmt.Errorf("mic capture closed")
		}
		if len(s.out)-s.outOff >= len(buf) {
			copyInt16Bytes(buf, s.out[s.outOff:])
			s.outOff += len(buf)
			if s.outOff == len(s.out) {
				s.out, s.outOff = nil, 0
			}
			s.mu.Unlock()
			return nil
		}
		s.drain()
		if len(s.out)-s.outOff >= len(buf) {
			copyInt16Bytes(buf, s.out[s.outOff:])
			s.outOff += len(buf)
			if s.outOff == len(s.out) {
				s.out, s.outOff = nil, 0
			}
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()

		var handles [2]uintptr = [2]uintptr{uintptr(s.evData), uintptr(s.evStop)}
		waitForMultipleObjs.Call(2, uintptr(unsafe.Pointer(&handles[0])), 0, infinite)
	}
}

// drain pulls every pending capture packet, converts to mono float, resamples
// to the configured rate and appends int16 samples. Caller holds s.mu.
func (s *wasapiSrc) drain() {
	for {
		var frames uint32
		if hr := s.cap.getNextPacketSize(&frames); hr != 0 || frames == 0 {
			break
		}
		var p *byte
		if hr := s.cap.getBuffer(&p, &frames); hr != 0 {
			break
		}
		s.in = append(s.in, s.packetMono(p, frames)...)
		s.cap.releaseBuffer(frames)
	}
	for len(s.in)-int(s.inPos) >= 2 {
		i := int(s.inPos)
		frac := s.inPos - float64(i)
		v := float64(s.in[i])*(1-frac) + float64(s.in[i+1])*frac
		sv := v * 32767.0
		if sv > 32767.0 {
			sv = 32767.0
		} else if sv < -32768.0 {
			sv = -32768.0
		}
		s.out = append(s.out, int16(int32(sv)))
		s.inPos += s.ratio
	}
	if n := int(s.inPos); n > 0 {
		s.in = s.in[n:]
		s.inPos -= float64(n)
	}
}

// packetMono downmixes one capture packet (interleaved mixCh samples) to mono
// floats in [-1, 1].
func (s *wasapiSrc) packetMono(p *byte, frames uint32) []float32 {
	out := make([]float32, frames)
	switch s.kind {
	case 0:
		samp := unsafe.Slice((*float32)(unsafe.Pointer(p)), int(frames)*int(s.mixCh))
		for f := uint32(0); f < frames; f++ {
			var sum float64
			for c := uint32(0); c < s.mixCh; c++ {
				sum += float64(samp[f*s.mixCh+c])
			}
			out[f] = float32(sum / float64(s.mixCh))
		}
	case 1:
		samp := unsafe.Slice((*int16)(unsafe.Pointer(p)), int(frames)*int(s.mixCh))
		for f := uint32(0); f < frames; f++ {
			var sum float64
			for c := uint32(0); c < s.mixCh; c++ {
				sum += float64(samp[f*s.mixCh+c])
			}
			out[f] = float32(sum / float64(s.mixCh) / 32768.0)
		}
	case 2:
		samp := unsafe.Slice((*int32)(unsafe.Pointer(p)), int(frames)*int(s.mixCh))
		for f := uint32(0); f < frames; f++ {
			var sum float64
			for c := uint32(0); c < s.mixCh; c++ {
				sum += float64(samp[f*s.mixCh+c])
			}
			out[f] = float32(sum / float64(s.mixCh) / 2147483648.0)
		}
	}
	return out
}

func (s *wasapiSrc) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	setEvent(uintptr(s.evStop))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cl.stop()
	s.cap.release()
	s.cl.release()
	closeHandle.Call(uintptr(s.evData))
	closeHandle.Call(uintptr(s.evStop))
	s.evData, s.evStop = 0, 0
}

func friendlyName(d *iMMDevice) string {
	var ps *iPropertyStore
	if hr := d.openPropertyStore(stgmRead, &ps); hr != 0 {
		return ""
	}
	defer ps.release()
	var pv propVariant
	if hr := ps.getValue(&pkeyDeviceFriendlyName, &pv); hr != 0 {
		return ""
	}
	defer coTaskMemFree.Call(pv.val)
	if pv.vt != vtLPWSTR || pv.val == 0 {
		return ""
	}
	p := (*uint16)(*(*unsafe.Pointer)(unsafe.Pointer(&pv.val)))
	var buf []uint16
	for i := 0; i < 1024; i++ {
		v := *(*uint16)(unsafe.Add(unsafe.Pointer(p), 2*i))
		if v == 0 {
			break
		}
		buf = append(buf, v)
	}
	return syscall.UTF16ToString(buf)
}

// enumerateCaptureNames lists the friendly names of all active capture
// endpoints, used for diagnostics when the configured device does not match.
// It returns a human-readable summary including step HRESULTs.
func enumerateCaptureNames() string {
	var out strings.Builder
	hr0, _, _ := coInitializeEx.Call(0, coinitMultithreaded)
	fmt.Fprintf(&out, "CoInitializeEx=0x%08x ", hr0)
	var e *iMMDeviceEnumerator
	hr, _, _ := coCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidMMDeviceEnumerator)), uintptr(unsafe.Pointer(&e)))
	fmt.Fprintf(&out, "CoCreateInstance=0x%08x e=%p ", hr, unsafe.Pointer(e))
	if e == nil {
		return out.String()
	}
	defer e.release()
	var col *iMMDeviceCollection
	hr2 := e.enumAudioEndpoints(eCapture, devstateActive, &col)
	fmt.Fprintf(&out, "EnumAudioEndpoints=0x%08x col=%p ", hr2, unsafe.Pointer(col))
	if col == nil {
		return out.String()
	}
	defer col.release()
	var n uint32
	hr3 := col.getCount(&n)
	fmt.Fprintf(&out, "GetCount=0x%08x n=%d [", hr3, n)
	for i := uint32(0); i < n; i++ {
		var d *iMMDevice
		if hr4 := col.item(i, &d); hr4 != 0 {
			fmt.Fprintf(&out, " item%d=0x%08x", i, hr4)
			continue
		}
		name := friendlyName(d)
		if name != "" {
			out.WriteString(name)
		} else {
			out.WriteString("(unnamed)")
		}
		if i+1 < n {
			out.WriteString(" | ")
		}
		d.release()
	}
	out.WriteString("]")
	return out.String()
}

// copyInt16Bytes packs little-endian s16le into b (len(b) must be 2*len(s)).
func copyInt16Bytes(b []byte, s []int16) {
	for i, v := range s {
		b[i*2] = byte(v)
		b[i*2+1] = byte(v >> 8)
	}
}
