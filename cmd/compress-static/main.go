// compress-static pre-compresses web static assets with gzip, brotli, and
// zstd so the server can serve them with zero runtime CPU cost. Run before
// go build so the compressed files get embedded via go:embed.
//
// Usage: go run ./cmd/compress-static <static-dir>
package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	compDir := filepath.Join(dir, "compressed")
	os.RemoveAll(compDir)
	os.MkdirAll(compDir, 0o755)

	totalSaved := 0
	fileCount := 0

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if strings.HasPrefix(rel, "compressed") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".js" && ext != ".css" && ext != ".html" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		origSize := len(data)

		outDir := filepath.Join(compDir, filepath.Dir(rel))
		os.MkdirAll(outDir, 0o755)

		// gzip level 9 — max ratio, build-time only.
		gzData := gzipCompress(data)
		if len(gzData) < origSize {
			os.WriteFile(filepath.Join(compDir, rel+".gz"), gzData, 0o644)
		}

		// brotli level 11 — max compression, build-time only.
		brData := brotliCompress(data)
		if len(brData) < origSize {
			os.WriteFile(filepath.Join(compDir, rel+".br"), brData, 0o644)
		}

		// zstd level 19 — best ratio, build-time only.
		zsData := zstdCompress(data)
		if len(zsData) < origSize {
			os.WriteFile(filepath.Join(compDir, rel+".zst"), zsData, 0o644)
		}

		best := origSize
		for _, d := range [][]byte{gzData, brData, zsData} {
			if len(d) > 0 && len(d) < best {
				best = len(d)
			}
		}
		saved := origSize - best
		totalSaved += saved
		fileCount++
		fmt.Printf("  %s: %dKB -> %dKB (gzip=%dKB brotli=%dKB zstd=%dKB)\n",
			rel, origSize/1024, best/1024, len(gzData)/1024, len(brData)/1024, len(zsData)/1024)
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  compressed %d files, saved %dKB total\n", fileCount, totalSaved/1024)
}

func gzipCompress(data []byte) []byte {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	w.Write(data)
	w.Close()
	return buf.Bytes()
}

func brotliCompress(data []byte) []byte {
	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, 11)
	w.Write(data)
	w.Close()
	return buf.Bytes()
}

func zstdCompress(data []byte) []byte {
	var buf bytes.Buffer
	w, _ := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	w.Write(data)
	w.Close()
	return buf.Bytes()
}
