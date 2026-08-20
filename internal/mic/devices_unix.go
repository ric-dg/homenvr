//go:build !windows

package mic

// listWasapiDevices returns nil on non-Windows (WASAPI is Windows-only).
func listWasapiDevices() []AudioDevice { return nil }
