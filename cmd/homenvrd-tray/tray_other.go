//go:build !windows

// homenvrd-tray is a Windows-only tray helper (see main.go). This stub keeps
// the package buildable on Linux/macOS so `go build ./...` and `go vet ./...`
// pass on every platform. The control panel and the daemon themselves are
// fully cross-platform; only the optional tray convenience binary is not.
package main

import "fmt"

func main() {
	fmt.Println("homenvrd-tray is Windows-only; use the control panel at http://127.0.0.1:8080")
}
