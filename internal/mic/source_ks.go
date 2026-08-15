//go:build windows

// WDM-KS (Kernel Streaming) capture backend. This is the path PortAudio uses
// when WASAPI/MME/DirectShow expose no audio device (the sounddevice helper in
// v1 picked it via host-api fallback). It enumerates KSCATEGORY_AUDIO device
// interfaces with SetupAPI, opens the filter with CreateFile, probes its pins
// for a capture (KSPIN_DATAFLOW_IN) sink with the standard streaming interface
// and a PCM datarange, instantiates the pin with KsCreatePin (ksuser.dll),
// runs it through KSSTATE_ACQUIRE/PAUSE/RUN, and streams with asynchronous
// IOCTL_KS_READ_STREAM packets whose buffers are pinned for the stream's
// lifetime. The captured format (device native channels/rate/bits) is downmixed
// to mono and linearly resampled to the configured sample rate, mirroring the
// WASAPI source. Struct layouts and GUIDs are taken verbatim from the Windows
// SDK (ks.h/ksmedia.h).
package mic

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/ric-dg/homenvr/internal/config"
)

// ---- KS constants (ks.h / ksmedia.h, Windows SDK) --------------------------

const (
	// IOCTL codes: CTL_CODE(FILE_DEVICE_KS=0x2F, fn, METHOD_NEITHER=3, access).
	ioctlKSProperty   = 0x2F0003
	ioctlKSReadStream = 0x2F4017

	ksPropertyTypeGet = 0x00000001
	ksPropertyTypeSet = 0x00000002

	// KSPROPSETID_Pin property ids.
	ksPinCTypes        = 1
	ksPinDataflow      = 2
	ksPinDataRanges    = 3
	ksPinInterfaces    = 5
	ksPinMediums       = 6
	ksPinCommunication = 7

	// KSPROPSETID_Connection.
	ksPropertyConnectionState = 0

	ksInterfaceStandardStreaming = 0
	ksMediumTypeAnyInstance      = 0
	ksPriorityNormal             = 0x40000000

	ksPinDataflowIn  = 1
	ksPinDataflowOut = 2

	ksPinCommunicationSink = 1
	ksPinCommunicationBoth = 3

	ksStateStop    = 0
	ksStateAcquire = 1
	ksStatePause   = 2
	ksStateRun     = 3

	// SetupAPI.
	digcfPresent              = 0x2
	digcfDeviceInterface      = 0x10
	spdrpDevicedesc           = 0x00000000
	spdrpFriendlyname         = 0x0000000C
	devpropTypeString         = 0x13
	devpropTypeStringIndirect = 0x12

	errorInsufficientBuffer = syscall.Errno(122)
	errorMoreData           = syscall.Errno(234)
	errorIoPending          = syscall.Errno(997)
	errorInvalidParameter   = syscall.Errno(87)
	waitTimeout             = 0x00000102

	genericRead        = 0x80000000
	genericWrite       = 0x40000000
	fileShareRead      = 0x1
	fileShareWrite     = 0x2
	openExisting       = 3
	fileFlagOverlapped = 0x40000000

	// KSSTREAM_HEADER field offsets (amd64; the SDK layout).
	offHeaderSize         = 0
	offHeaderFrameExtent  = 32
	offHeaderDataUsed     = 36
	offHeaderData         = 40
	offHeaderOptionsFlags = 48
	ksStreamHeaderSize    = 56

	ksPinConnectSize     = 72
	ksDataFormatWfexSize = 82
	ksDataRangeAudioSize = 84 // fixed part of KSDATARANGE_AUDIO
)

var (
	ksCategoryAudio = guid{0x04, 0xAD, 0x94, 0x69, 0xEF, 0x93, 0xD0, 0x11, 0xA3, 0xCC, 0x00, 0xA0, 0xC9, 0x22, 0x31, 0x96}

	ksInterfaceSetStandard = guid{0xA0, 0x66, 0x87, 0x1A, 0xCE, 0x62, 0xCF, 0x11, 0xA5, 0xD6, 0x28, 0xDB, 0x04, 0xC1, 0x00, 0x00}
	ksMediumSetStandard    = guid{0x20, 0xB3, 0x47, 0x47, 0xCE, 0x62, 0xCF, 0x11, 0xA5, 0xD6, 0x28, 0xDB, 0x04, 0xC1, 0x00, 0x00}

	ksPropSetPin        = guid{0x60, 0x49, 0x13, 0x8C, 0xAD, 0x51, 0xCF, 0x11, 0x87, 0x8A, 0x94, 0xF8, 0x01, 0xC1, 0x00, 0x00}
	ksPropSetConnection = guid{0x20, 0xC9, 0x58, 0x1D, 0x9B, 0xAC, 0xCF, 0x11, 0xA5, 0xD6, 0x28, 0xDB, 0x04, 0xC1, 0x00, 0x00}

	ksFormatTypeAudio           = guid{0x61, 0x75, 0x64, 0x73, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}
	ksFormatSubtypePCM          = guid{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}
	ksFormatSubtypeIEEEFloat    = guid{0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}
	ksFormatSpecifierWaveFormat = guid{0x81, 0x9F, 0x58, 0x05, 0x56, 0xC3, 0xCE, 0x11, 0xBF, 0x01, 0x00, 0xAA, 0x00, 0x55, 0x59, 0x5A}

	devpkeyDeviceFriendlyName = propertyKey{
		fmtID: guid{0x4E, 0x25, 0x5C, 0xA4, 0x1C, 0xDF, 0xFD, 0x4E, 0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0},
		pid:   14,
	}
)

var (
	setupapi                          = syscall.NewLazyDLL("setupapi.dll")
	setupDiGetClassDevsW              = setupapi.NewProc("SetupDiGetClassDevsW")
	setupDiEnumDeviceInterfaces       = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	setupDiGetDeviceInterfaceDetailW  = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	setupDiDestroyDeviceInfoList      = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
	setupDiGetDevicePropertyW         = setupapi.NewProc("SetupDiGetDevicePropertyW")
	setupDiGetDeviceRegistryPropertyW = setupapi.NewProc("SetupDiGetDeviceRegistryPropertyW")

	ksuser          = syscall.NewLazyDLL("ksuser.dll")
	ksCreatePin     = ksuser.NewProc("KsCreatePin")
	createFileW     = kernel32.NewProc("CreateFileW")
	deviceIoControl = kernel32.NewProc("DeviceIoControl")
	resetEvent      = kernel32.NewProc("ResetEvent")
)

// ---- SetupAPI enumeration --------------------------------------------------

// ksDevice is one enumerated KSCATEGORY_AUDIO device interface.
type ksDevice struct {
	path string
	name string
}

// spDeviceInterfaceData matches SP_DEVICE_INTERFACE_DATA.
type spDeviceInterfaceData struct {
	cbSize    uint32
	classGuid [16]byte
	flags     uint32
	reserved  uintptr
}

// spDevInfoData matches SP_DEVINFO_DATA.
type spDevInfoData struct {
	cbSize    uint32
	classGuid [16]byte
	devInst   uint32
	reserved  uintptr
}

// ksInterfaces enumerates all KSCATEGORY_AUDIO device interfaces present.
func ksInterfaces() ([]ksDevice, error) {
	hdev, _, _ := setupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(&ksCategoryAudio[0])), 0, 0, digcfPresent|digcfDeviceInterface)
	if hdev == ^uintptr(0) {
		return nil, fmt.Errorf("SetupDiGetClassDevsW failed")
	}
	defer setupDiDestroyDeviceInfoList.Call(hdev)

	ifdSize := 24 + int(unsafe.Sizeof(uintptr(0)))
	didSize := 24 + int(unsafe.Sizeof(uintptr(0)))
	detailCb := int(unsafe.Sizeof(uintptr(0)))

	var out []ksDevice
	for i := 0; ; i++ {
		var ifd spDeviceInterfaceData
		ifd.cbSize = uint32(ifdSize)
		r, _, _ := setupDiEnumDeviceInterfaces.Call(
			hdev, 0, uintptr(unsafe.Pointer(&ksCategoryAudio[0])), uintptr(i), uintptr(unsafe.Pointer(&ifd)))
		if r == 0 {
			break
		}
		var need uint32
		setupDiGetDeviceInterfaceDetailW.Call(
			hdev, uintptr(unsafe.Pointer(&ifd)), 0, 0, uintptr(unsafe.Pointer(&need)), 0)
		if need == 0 {
			continue
		}
		buf := make([]byte, need)
		binary.LittleEndian.PutUint32(buf[0:], uint32(detailCb))
		var did spDevInfoData
		did.cbSize = uint32(didSize)
		r, _, _ = setupDiGetDeviceInterfaceDetailW.Call(
			hdev, uintptr(unsafe.Pointer(&ifd)), uintptr(unsafe.Pointer(&buf[0])),
			uintptr(need), 0, uintptr(unsafe.Pointer(&did)))
		if r == 0 {
			continue
		}
		path := syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[4])), (len(buf)-4)/2))
		out = append(out, ksDevice{path: path, name: ksDeviceFriendlyName(hdev, &did)})
	}
	return out, nil
}

// ksDeviceFriendlyName reads DEVPKEY_Device_FriendlyName, falling back to the
// SPDRP_FRIENDLYNAME / SPDRP_DEVICEDESC registry values. The property comes
// back as either DEVPROP_TYPE_STRING or DEVPROP_TYPE_STRING_INDIRECT (both
// decode to a plain wide string in the buffer; observed on this machine).
func ksDeviceFriendlyName(hdev uintptr, did *spDevInfoData) string {
	var propType uint32
	var need uint32
	r, _, _ := setupDiGetDevicePropertyW.Call(
		hdev, uintptr(unsafe.Pointer(did)), uintptr(unsafe.Pointer(&devpkeyDeviceFriendlyName)),
		uintptr(unsafe.Pointer(&propType)), 0, 0, uintptr(unsafe.Pointer(&need)), 0)
	if r == 0 {
		if need == 0 {
			return ksDeviceRegistryName(hdev, did)
		}
		buf := make([]byte, need)
		r, _, _ = setupDiGetDevicePropertyW.Call(
			hdev, uintptr(unsafe.Pointer(did)), uintptr(unsafe.Pointer(&devpkeyDeviceFriendlyName)),
			uintptr(unsafe.Pointer(&propType)), uintptr(unsafe.Pointer(&buf[0])), uintptr(need), 0, 0)
		if r != 0 && (propType == devpropTypeString || propType == devpropTypeStringIndirect) {
			return utf16String(buf)
		}
	}
	return ksDeviceRegistryName(hdev, did)
}

func ksDeviceRegistryName(hdev uintptr, did *spDevInfoData) string {
	for _, sp := range []uint32{spdrpFriendlyname, spdrpDevicedesc} {
		var typ uint32
		var need uint32
		r, _, _ := setupDiGetDeviceRegistryPropertyW.Call(
			hdev, uintptr(unsafe.Pointer(did)), uintptr(sp), uintptr(unsafe.Pointer(&typ)), 0, 0, uintptr(unsafe.Pointer(&need)))
		if r == 0 || need < 2 {
			continue
		}
		buf := make([]byte, need)
		r, _, _ = setupDiGetDeviceRegistryPropertyW.Call(
			hdev, uintptr(unsafe.Pointer(did)), uintptr(sp), uintptr(unsafe.Pointer(&typ)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(need), 0)
		if r != 0 {
			return utf16String(buf)
		}
	}
	return ""
}

func utf16String(b []byte) string {
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 && b[i+1] == 0 {
			b = b[:i]
			break
		}
	}
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&b[0])), len(b)/2))
}

// ---- KS property plumbing --------------------------------------------------

// devIo wraps DeviceIoControl; out may be nil for a size probe.
func devIo(h syscall.Handle, code uint32, in, out []byte) (uint32, error) {
	var inPtr, outPtr uintptr
	var inSize, outSize uintptr
	if len(in) > 0 {
		inPtr = uintptr(unsafe.Pointer(&in[0]))
		inSize = uintptr(len(in))
	}
	if len(out) > 0 {
		outPtr = uintptr(unsafe.Pointer(&out[0]))
		outSize = uintptr(len(out))
	}
	var n uint32
	r, _, err := deviceIoControl.Call(uintptr(h), uintptr(code), inPtr, inSize, outPtr, outSize,
		uintptr(unsafe.Pointer(&n)), 0)
	if r != 0 {
		return n, nil
	}
	return n, err
}

func errnoIs(err error, e syscall.Errno) bool {
	ee, ok := err.(syscall.Errno)
	return ok && ee == e
}

// ksPinPropGet reads a KSPROPSETID_Pin property for one pin into out.
func ksPinPropGet(h syscall.Handle, set *guid, id, pin uint32, out []byte) error {
	in := make([]byte, 32) // KSP_PIN
	copy(in[0:16], set[:])
	binary.LittleEndian.PutUint32(in[16:], id)
	binary.LittleEndian.PutUint32(in[20:], ksPropertyTypeGet)
	binary.LittleEndian.PutUint32(in[24:], pin)
	_, err := devIo(h, ioctlKSProperty, in, out)
	return err
}

// ksPinPropMulti reads a multi-valued KSPROPSETID_Pin property (KSMULTIPLE_ITEM
// blob: {Size, Count} then the items).
func ksPinPropMulti(h syscall.Handle, set *guid, id, pin uint32) ([]byte, error) {
	in := make([]byte, 32)
	copy(in[0:16], set[:])
	binary.LittleEndian.PutUint32(in[16:], id)
	binary.LittleEndian.PutUint32(in[20:], ksPropertyTypeGet)
	binary.LittleEndian.PutUint32(in[24:], pin)
	var n uint32
	r, _, err := deviceIoControl.Call(uintptr(h), ioctlKSProperty,
		uintptr(unsafe.Pointer(&in[0])), 32, 0, 0, uintptr(unsafe.Pointer(&n)), 0)
	if r == 0 && !errnoIs(err, errorInsufficientBuffer) && !errnoIs(err, errorMoreData) {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("KS property %d (pin %d) returned empty multi", id, pin)
	}
	out := make([]byte, n)
	if _, err := devIo(h, ioctlKSProperty, in, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ksSetConnectionState sets KSPROPERTY_CONNECTION_STATE on a pin handle.
func ksSetConnectionState(h syscall.Handle, state uint32) error {
	in := make([]byte, 24) // KSPROPERTY
	copy(in[0:16], ksPropSetConnection[:])
	binary.LittleEndian.PutUint32(in[16:], ksPropertyConnectionState)
	binary.LittleEndian.PutUint32(in[20:], ksPropertyTypeSet)
	out := make([]byte, 4)
	binary.LittleEndian.PutUint32(out, state)
	_, err := devIo(h, ioctlKSProperty, in, out)
	return err
}

func parseIdentifiers(multi []byte) int {
	if len(multi) < 8 {
		return 0
	}
	return int(binary.LittleEndian.Uint32(multi[4:]))
}

func identifierHas(multi []byte, i int, set *guid, id uint32) bool {
	off := 8 + i*24
	if off+24 > len(multi) {
		return false
	}
	if !guidsEqual(multi[off:off+16], set) {
		return false
	}
	return binary.LittleEndian.Uint32(multi[off+16:]) == id
}

// ksDataRangeAudio matches KSDATARANGE_AUDIO (88 bytes on amd64).
type ksDataRangeAudio struct {
	formatSize  uint32
	flags       uint32
	sampleSize  uint32
	reserved    uint32
	majorFormat [16]byte
	subFormat   [16]byte
	specifier   [16]byte
	maxChannels uint32
	minBits     uint32
	maxBits     uint32
	minFreq     uint32
	maxFreq     uint32
}

func parseDataRanges(multi []byte) []ksDataRangeAudio {
	if len(multi) < 8 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(multi[4:]))
	var out []ksDataRangeAudio
	off := 8 // after KSMULTIPLE_ITEM
	for i := 0; i < count; i++ {
		if off+ksDataRangeAudioSize > len(multi) {
			break
		}
		r := multi[off : off+ksDataRangeAudioSize]
		var d ksDataRangeAudio
		d.formatSize = binary.LittleEndian.Uint32(r[0:])
		// Ranges are variable-size: advance by each range's FormatSize
		// (KSDATARANGE_AUDIO is 84 bytes; the driver may pad to alignment).
		if d.formatSize < ksDataRangeAudioSize {
			break
		}
		d.flags = binary.LittleEndian.Uint32(r[4:])
		d.sampleSize = binary.LittleEndian.Uint32(r[8:])
		d.reserved = binary.LittleEndian.Uint32(r[12:])
		copy(d.majorFormat[:], r[16:32])
		copy(d.subFormat[:], r[32:48])
		copy(d.specifier[:], r[48:64])
		d.maxChannels = binary.LittleEndian.Uint32(r[64:])
		d.minBits = binary.LittleEndian.Uint32(r[68:])
		d.maxBits = binary.LittleEndian.Uint32(r[72:])
		d.minFreq = binary.LittleEndian.Uint32(r[76:])
		d.maxFreq = binary.LittleEndian.Uint32(r[80:])
		out = append(out, d)
		off += int(d.formatSize)
	}
	return out
}

func guidsEqual(a []byte, b *guid) bool {
	if len(a) < 16 {
		return false
	}
	for i := 0; i < 16; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- Format selection ------------------------------------------------------

// ksFormat is the negotiated pin format.
type ksFormat struct {
	rate, bits, channels, maxBits uint32
	subFormat                     [16]byte
}

var preferRates = []uint32{48000, 44100, 32000, 24000, 16000, 22050, 11025, 8000, 96000, 192000}

func chooseFormat(ranges []ksDataRangeAudio, wantRate uint32) (ksFormat, error) {
	var pcms []ksDataRangeAudio
	for _, r := range ranges {
		if guidsEqual(r.majorFormat[:], &ksFormatTypeAudio) && guidsEqual(r.subFormat[:], &ksFormatSubtypePCM) {
			pcms = append(pcms, r)
		}
	}
	if len(pcms) == 0 {
		for _, r := range ranges {
			if guidsEqual(r.majorFormat[:], &ksFormatTypeAudio) && guidsEqual(r.subFormat[:], &ksFormatSubtypeIEEEFloat) {
				return formatFor(r, preferWithin(r, wantRate), 32)
			}
		}
		return ksFormat{}, fmt.Errorf("no PCM/float audio datarange")
	}
	for _, r := range pcms {
		if wantRate >= r.minFreq && wantRate <= r.maxFreq && r.maxBits >= 16 {
			return formatFor(r, wantRate, 16)
		}
	}
	for _, r := range pcms {
		if r.maxBits >= 16 {
			return formatFor(r, preferWithin(r, wantRate), 16)
		}
	}
	r := pcms[0]
	bits := r.maxBits
	if bits == 0 {
		bits = 16
	}
	return formatFor(r, preferWithin(r, wantRate), bits)
}

func preferWithin(r ksDataRangeAudio, want uint32) uint32 {
	if want >= r.minFreq && want <= r.maxFreq {
		return want
	}
	for _, v := range preferRates {
		if v >= r.minFreq && v <= r.maxFreq {
			return v
		}
	}
	if r.minFreq > 0 {
		return r.minFreq
	}
	return 48000
}

func formatFor(r ksDataRangeAudio, rate, bits uint32) (ksFormat, error) {
	ch := r.maxChannels
	if ch == 0 {
		ch = 1
	}
	if ch > 8 {
		ch = 8
	}
	if rate == 0 {
		return ksFormat{}, fmt.Errorf("zero sample rate")
	}
	var sub [16]byte
	copy(sub[:], r.subFormat[:])
	return ksFormat{rate: rate, bits: bits, channels: ch, maxBits: r.maxBits, subFormat: sub}, nil
}

// kind classifies the raw sample encoding: 0 int16, 1 int24, 2 int32,
// 3 float32, 4 unsigned int8.
func (f ksFormat) kind() int {
	if guidsEqual(f.subFormat[:], &ksFormatSubtypeIEEEFloat) {
		return 3
	}
	switch f.bits {
	case 8:
		return 4
	case 24:
		return 1
	case 32:
		return 2
	default:
		return 0
	}
}

// ---- Stream ----------------------------------------------------------------

// ksSrc streams PCM from a WDM-KS pin and converts to s16le mono at the
// configured sample rate. readBlock drives the packet loop synchronously, like
// PortAudio's processing thread.
type ksSrc struct {
	log    Logger
	mic    config.Mic
	filter syscall.Handle
	pin    syscall.Handle
	evStop syscall.Handle
	pinner runtime.Pinner

	packets       [2]ksPacket
	format        ksFormat
	bytesPerFrame uint32

	mu     sync.Mutex
	closed bool

	in     []float32
	inPos  float64
	ratio  float64
	out    []int16
	outOff int
}

// ksPacket is one asynchronous IOCTL_KS_READ_STREAM request. The OVERLAPPED
// must stay allocated (and unpinned against collection) for the whole pending
// operation, so it lives with the packet instead of being rebuilt per submit.
type ksPacket struct {
	header  [ksStreamHeaderSize]byte
	data    []byte
	ov      []byte
	ev      syscall.Handle
	pending bool
}

// newKs opens the WDM-KS capture for mic.DeviceName (substring match against
// the KSCATEGORY_AUDIO friendly name).
func newKs(name string, log Logger, mic config.Mic) (captureSource, error) {
	if mic.DeviceName == "" {
		return nil, fmt.Errorf("mic device_name is empty")
	}
	if mic.SampleRate <= 0 {
		return nil, fmt.Errorf("mic sample_rate must be > 0")
	}
	devs, err := ksInterfaces()
	if err != nil {
		return nil, fmt.Errorf("KS enumeration: %v", err)
	}
	want := strings.ToLower(mic.DeviceName)
	var match ksDevice
	found := false
	for _, d := range devs {
		if strings.Contains(strings.ToLower(d.name), want) {
			match, found = d, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("no KS audio device matches %q", mic.DeviceName)
	}
	log.Logf("mic [%s] KS device=%q path=%s", name, match.name, match.path)

	s := &ksSrc{log: log, mic: mic}
	s.evStop = createEvent(1, 0)
	if s.evStop == 0 {
		return nil, fmt.Errorf("CreateEventW(evStop) failed")
	}
	if err := s.open(match); err != nil {
		s.close()
		return nil, err
	}
	log.Logf("mic [%s] KS capture opened rate=%d ch=%d bits=%d", name, s.format.rate, s.format.channels, s.format.bits)
	return s, nil
}

// open finds a capture pin, instantiates it and starts streaming.
func (s *ksSrc) open(d ksDevice) error {
	fh, err := createFileFilter(d.path)
	if err != nil {
		return fmt.Errorf("open KS filter: %w", err)
	}
	s.filter = fh

	pinID, format, err := s.findCapturePin()
	if err != nil {
		return err
	}
	s.format = format

	ph, err := s.createPin(pinID, format)
	if err != nil {
		return err
	}
	s.pin = ph

	for _, st := range []uint32{ksStateAcquire, ksStatePause} {
		if err := ksSetConnectionState(ph, st); err != nil {
			return fmt.Errorf("set KS state %d: %w", st, err)
		}
	}
	// Submit the read packets while the pin is PAUSEd, then RUN (PortAudio
	// does the same: PreparePinsForStart submits before StartPin runs the pin).
	if err := s.setupPackets(); err != nil {
		return err
	}
	if err := ksSetConnectionState(ph, ksStateRun); err != nil {
		return fmt.Errorf("set KS state %d: %w", ksStateRun, err)
	}
	return nil
}

func createFileFilter(path string) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	r, _, e := createFileW.Call(uintptr(unsafe.Pointer(p)), genericRead|genericWrite,
		fileShareRead|fileShareWrite, 0, openExisting, fileFlagOverlapped, 0)
	if r == ^uintptr(0) {
		return 0, e
	}
	return syscall.Handle(r), nil
}

// findCapturePin locates the first pin on the filter that is a capture sink
// with the standard streaming interface, a standard devio medium and a PCM
// datarange, returning its id and the negotiated format. For a capture device
// the client reads from a pin whose data flows OUT of the filter
// (KSPIN_DATAFLOW_OUT; PortAudio's "isInput = dataFlow == KSPIN_DATAFLOW_OUT").
func (s *ksSrc) findCapturePin() (uint32, ksFormat, error) {
	out := make([]byte, 4)
	if err := ksPinPropGet(s.filter, &ksPropSetPin, ksPinCTypes, 0, out); err != nil {
		return 0, ksFormat{}, fmt.Errorf("KSPROPERTY_PIN_CTYPES: %w", err)
	}
	n := binary.LittleEndian.Uint32(out)
	for pin := uint32(0); pin < n; pin++ {
		if err := ksPinPropGet(s.filter, &ksPropSetPin, ksPinDataflow, pin, out); err != nil {
			continue
		}
		if binary.LittleEndian.Uint32(out) != ksPinDataflowOut {
			continue
		}
		if err := ksPinPropGet(s.filter, &ksPropSetPin, ksPinCommunication, pin, out); err != nil {
			continue
		}
		switch comm := binary.LittleEndian.Uint32(out); comm {
		case ksPinCommunicationSink, ksPinCommunicationBoth:
		default:
			continue
		}
		ifs, err := ksPinPropMulti(s.filter, &ksPropSetPin, ksPinInterfaces, pin)
		if err != nil {
			continue
		}
		hasStream := false
		for i := 0; i < parseIdentifiers(ifs); i++ {
			if identifierHas(ifs, i, &ksInterfaceSetStandard, ksInterfaceStandardStreaming) {
				hasStream = true
				break
			}
		}
		if !hasStream {
			continue
		}
		meds, err := ksPinPropMulti(s.filter, &ksPropSetPin, ksPinMediums, pin)
		if err != nil {
			continue
		}
		hasMedium := false
		for i := 0; i < parseIdentifiers(meds); i++ {
			if identifierHas(meds, i, &ksMediumSetStandard, ksMediumTypeAnyInstance) {
				hasMedium = true
				break
			}
		}
		if !hasMedium {
			continue
		}
		ranges, err := ksPinPropMulti(s.filter, &ksPropSetPin, ksPinDataRanges, pin)
		if err != nil {
			continue
		}
		format, err := chooseFormat(parseDataRanges(ranges), uint32(s.mic.SampleRate))
		if err != nil {
			continue
		}
		s.log.Logf("mic [%s] KS pin %d: %dHz %dch %dbit", s.mic.DeviceName, pin, format.rate, format.channels, format.bits)
		return pin, format, nil
	}
	return 0, ksFormat{}, fmt.Errorf("no capture pin with PCM streaming on %q", s.mic.DeviceName)
}

// createPin tries a few format candidates through KsCreatePin (the device may
// reject a rate/bits its dataranges advertise).
func (s *ksSrc) createPin(pinID uint32, format ksFormat) (syscall.Handle, error) {
	var cands []ksFormat
	add := func(f ksFormat) {
		for _, c := range cands {
			if c == f {
				return
			}
		}
		cands = append(cands, f)
	}
	add(format)
	add(ksFormat{rate: 48000, bits: 16, channels: 1, subFormat: ksFormatSubtypePCM})
	add(ksFormat{rate: 44100, bits: 16, channels: 1, subFormat: ksFormatSubtypePCM})
	if format.bits != format.maxBits {
		add(ksFormat{rate: format.rate, bits: format.maxBits, channels: format.channels, subFormat: format.subFormat})
	}
	var lastErr error
	for _, f := range cands {
		ph, err := s.createPinOne(pinID, f)
		if err == nil {
			s.format = f
			return ph, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

// createPinOne builds KSPIN_CONNECT + KSDATAFORMAT_WAVEFORMATEX and instantiates
// the pin via KsCreatePin (ksuser.dll).
func (s *ksSrc) createPinOne(pinID uint32, format ksFormat) (syscall.Handle, error) {
	blockAlign := uint16(format.channels * format.bits / 8)
	tag := uint16(waveFormatPCM)
	if guidsEqual(format.subFormat[:], &ksFormatSubtypeIEEEFloat) {
		tag = waveFormatIEEEFloat
	}
	b := make([]byte, ksPinConnectSize+ksDataFormatWfexSize)
	copy(b[0:16], ksInterfaceSetStandard[:])
	binary.LittleEndian.PutUint32(b[16:], ksInterfaceStandardStreaming)
	copy(b[24:40], ksMediumSetStandard[:])
	binary.LittleEndian.PutUint32(b[40:], ksMediumTypeAnyInstance)
	binary.LittleEndian.PutUint32(b[48:], pinID)
	binary.LittleEndian.PutUint32(b[64:], ksPriorityNormal)
	binary.LittleEndian.PutUint32(b[68:], 1)
	binary.LittleEndian.PutUint32(b[72:], ksDataFormatWfexSize)
	binary.LittleEndian.PutUint32(b[80:], uint32(blockAlign))
	copy(b[88:104], ksFormatTypeAudio[:])
	copy(b[104:120], format.subFormat[:])
	copy(b[120:136], ksFormatSpecifierWaveFormat[:])
	binary.LittleEndian.PutUint16(b[136:], tag)
	binary.LittleEndian.PutUint16(b[138:], uint16(format.channels))
	binary.LittleEndian.PutUint32(b[140:], format.rate)
	binary.LittleEndian.PutUint32(b[144:], format.rate*uint32(blockAlign))
	binary.LittleEndian.PutUint16(b[148:], blockAlign)
	binary.LittleEndian.PutUint16(b[150:], uint16(format.bits))

	var ph syscall.Handle
	r, _, _ := ksCreatePin.Call(uintptr(s.filter), uintptr(unsafe.Pointer(&b[0])),
		uintptr(genericRead|genericWrite), uintptr(unsafe.Pointer(&ph)))
	if r != 0 {
		return 0, fmt.Errorf("KsCreatePin: 0x%08x (fmt %dHz %dch %dbit)", uint32(r), format.rate, format.channels, format.bits)
	}
	return ph, nil
}

// setupPackets allocates two pinned read packets and submits them. The packet
// size is tied to the configured block (v1's sounddevice latency was a fraction
// of a second; a whole-second host buffer would add up to 1s of latency).
func (s *ksSrc) setupPackets() error {
	s.bytesPerFrame = s.format.channels * s.format.bits / 8
	if s.bytesPerFrame == 0 {
		return fmt.Errorf("bad bytesPerFrame for %dch %dbit", s.format.channels, s.format.bits)
	}
	s.ratio = float64(s.format.rate) / float64(s.mic.SampleRate)
	if s.ratio <= 0 {
		return fmt.Errorf("bad resample ratio %v", s.ratio)
	}
	frames := int(s.mic.BlockSize)
	if frames < 16 {
		frames = 16
	}
	if frames > int(s.format.rate) {
		frames = int(s.format.rate)
	}
	packetBytes := frames * int(s.bytesPerFrame)
	for i := range s.packets {
		p := &s.packets[i]
		p.data = make([]byte, packetBytes)
		p.ov = make([]byte, ovSize)
		s.pinner.Pin(&p.data[0])
		s.pinner.Pin(&p.header)
		s.pinner.Pin(&p.ov[0])
		p.ev = createEvent(1, 0)
		if p.ev == 0 {
			return fmt.Errorf("CreateEventW(packet %d) failed", i)
		}
		*(*uintptr)(unsafe.Pointer(&p.ov[ovEventOff])) = uintptr(p.ev)
		binary.LittleEndian.PutUint32(p.header[offHeaderSize:], ksStreamHeaderSize)
		// FrameExtent tells the driver how many bytes the buffer holds; USB
		// audio won't fill reads with FrameExtent=0 (PortAudio sets it, too).
		binary.LittleEndian.PutUint32(p.header[offHeaderFrameExtent:], uint32(packetBytes))
		binary.LittleEndian.PutUint32(p.header[offHeaderOptionsFlags:], 0)
		// KSTIME {Time, Numerator, Denominator}: 1/1, as PortAudio sets.
		binary.LittleEndian.PutUint32(p.header[8+8:], 1)
		binary.LittleEndian.PutUint32(p.header[8+12:], 1)
		if err := s.submit(p); err != nil {
			return fmt.Errorf("submit packet %d: %w", i, err)
		}
	}
	return nil
}

// submit (re)issues one async IOCTL_KS_READ_STREAM request.
func (s *ksSrc) submit(p *ksPacket) error {
	binary.LittleEndian.PutUint32(p.header[offHeaderDataUsed:], 0)
	binary.LittleEndian.PutUint32(p.header[offHeaderOptionsFlags:], 0)
	*(*uintptr)(unsafe.Pointer(&p.header[offHeaderData])) = uintptr(unsafe.Pointer(&p.data[0]))
	var n uint32
	r, _, err := deviceIoControl.Call(uintptr(s.pin), ioctlKSReadStream,
		0, 0, uintptr(unsafe.Pointer(&p.header[0])), ksStreamHeaderSize,
		uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&p.ov[0])))
	if r != 0 {
		p.pending = true
		return nil
	}
	if errnoIs(err, errorIoPending) {
		p.pending = true
		return nil
	}
	return err
}

// ovSize is sizeof(OVERLAPPED): on amd64 32 bytes, on 386 20 bytes. The layout
// is Internal, InternalHigh, {Offset, OffsetHigh}, hEvent; the hEvent field is
// always at offset 3*sizeof(uintptr).
var ovSize = (3*int(unsafe.Sizeof(uintptr(0))) + 8 + int(unsafe.Sizeof(uintptr(0))) - 1) &^ (int(unsafe.Sizeof(uintptr(0))) - 1)

// ovEventOff is the byte offset of OVERLAPPED.hEvent.
var ovEventOff = 3 * int(unsafe.Sizeof(uintptr(0)))

// readBlock fills buf with s16le mono PCM at the configured rate, driving the
// KS packet loop until enough audio is converted.
func (s *ksSrc) readBlock(buf []byte) error {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return fmt.Errorf("mic capture closed")
		}
		if len(s.out)-s.outOff >= len(buf)/2 {
			copyInt16Bytes(buf, s.out[s.outOff:s.outOff+len(buf)/2])
			s.outOff += len(buf) / 2
			if s.outOff == len(s.out) {
				s.out, s.outOff = nil, 0
			}
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()

		var handles [3]uintptr = [3]uintptr{
			uintptr(s.packets[0].ev), uintptr(s.packets[1].ev), uintptr(s.evStop),
		}
		// A 3s cap turns a dead/stalled pin into a readBlock error, which the
		// feeder treats as a capture restart (a live stream completes a packet
		// well within the budget; only a stopped device times out).
		r, _, _ := waitForMultipleObjs.Call(3, uintptr(unsafe.Pointer(&handles[0])), 0, 3000)
		switch r {
		case 0, 1:
			if err := s.handlePacket(int(r)); err != nil {
				return err
			}
		case 2:
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return fmt.Errorf("mic capture closed")
			}
			resetEvent.Call(uintptr(s.evStop))
		case waitTimeout:
			return fmt.Errorf("KS read stream timed out (no packet for 3s)")
		default:
			return fmt.Errorf("WaitForMultipleObjects: %v", r)
		}
	}
}

// handlePacket consumes one completed read and resubmits the packet.
func (s *ksSrc) handlePacket(i int) error {
	p := &s.packets[i]
	resetEvent.Call(uintptr(p.ev))
	if !p.pending {
		return nil
	}
	p.pending = false

	dataUsed := int(binary.LittleEndian.Uint32(p.header[offHeaderDataUsed:]))
	if dataUsed == 0 {
		// Bogus event: some USB mics signal without filling the buffer on
		// startup (PortAudio handles the same). Resubmit and keep going.
		time.Sleep(5 * time.Millisecond)
		if err := s.submit(p); err != nil {
			return fmt.Errorf("resubmit after empty read: %w", err)
		}
		return nil
	}
	if dataUsed > len(p.data) {
		dataUsed = len(p.data)
	}
	if n := dataUsed % int(s.bytesPerFrame); n != 0 {
		dataUsed -= n
	}
	s.feed(p.data[:dataUsed], dataUsed/int(s.bytesPerFrame))
	if err := s.submit(p); err != nil {
		return fmt.Errorf("resubmit packet: %w", err)
	}
	return nil
}

// feed downmixes a device-format packet to mono float and resamples it into
// the output s16le buffer. Caller holds no lock.
func (s *ksSrc) feed(pcm []byte, frames int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := int(s.format.channels)
	switch s.format.kind() {
	case 0: // int16
		for f := 0; f < frames; f++ {
			var sum float64
			for c := 0; c < ch; c++ {
				sum += float64(int16(binary.LittleEndian.Uint16(pcm[(f*ch+c)*2:])))
			}
			s.in = append(s.in, float32(sum/float64(ch)/32768.0))
		}
	case 1: // int24
		for f := 0; f < frames; f++ {
			var sum float64
			for c := 0; c < ch; c++ {
				off := (f*ch + c) * 3
				v := int32(uint32(pcm[off]) | uint32(pcm[off+1])<<8 | uint32(pcm[off+2])<<16)
				if v&0x800000 != 0 {
					v |= ^0xFFFFFF
				}
				sum += float64(v)
			}
			s.in = append(s.in, float32(sum/float64(ch)/8388608.0))
		}
	case 2: // int32
		for f := 0; f < frames; f++ {
			var sum float64
			for c := 0; c < ch; c++ {
				sum += float64(int32(binary.LittleEndian.Uint32(pcm[(f*ch+c)*4:])))
			}
			s.in = append(s.in, float32(sum/float64(ch)/2147483648.0))
		}
	case 3: // float32
		for f := 0; f < frames; f++ {
			var sum float64
			for c := 0; c < ch; c++ {
				sum += float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[(f*ch+c)*4:])))
			}
			s.in = append(s.in, float32(sum/float64(ch)))
		}
	case 4: // unsigned int8
		for f := 0; f < frames; f++ {
			var sum float64
			for c := 0; c < ch; c++ {
				sum += (float64(pcm[f*ch+c]) - 128) / 128.0
			}
			s.in = append(s.in, float32(sum/float64(ch)))
		}
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

func (s *ksSrc) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	setEvent(uintptr(s.evStop))
	s.cleanup()
	s.pinner.Unpin()
}

func (s *ksSrc) cleanup() {
	if s.pin != 0 {
		// Wind down PAUSE->STOP, mirroring PortAudio's StopPin.
		ksSetConnectionState(s.pin, ksStatePause)
		ksSetConnectionState(s.pin, ksStateStop)
		closeHandle.Call(uintptr(s.pin))
		s.pin = 0
	}
	if s.filter != 0 {
		closeHandle.Call(uintptr(s.filter))
		s.filter = 0
	}
	if s.evStop != 0 {
		closeHandle.Call(uintptr(s.evStop))
		s.evStop = 0
	}
	for i := range s.packets {
		if s.packets[i].ev != 0 {
			closeHandle.Call(uintptr(s.packets[i].ev))
			s.packets[i].ev = 0
		}
	}
}

// enumerateKsDevices lists every KSCATEGORY_AUDIO interface, for diagnostics.
func enumerateKsDevices() string {
	devs, err := ksInterfaces()
	if err != nil {
		return fmt.Sprintf("KS enumeration failed: %v", err)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d KS audio interfaces:", len(devs))
	for i, d := range devs {
		name := d.name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&out, " [%d] %q (%s)", i, name, d.path)
	}
	return out.String()
}
