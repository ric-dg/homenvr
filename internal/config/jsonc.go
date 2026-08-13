package config

import "strings"

// stripComments removes "//" line and "/* */" block comments from JSONC text
// while respecting string literals (including escaped quotes). This is a
// faithful port of v1 config.py strip_comments: a comment marker inside a
// string (e.g. an "rtsp://" URL) is preserved.
func stripComments(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	n := len(text)
	inStr := false
	for i := 0; i < n; {
		c := text[i]
		if inStr {
			b.WriteByte(c)
			if c == '\\' && i+1 < n {
				b.WriteByte(text[i+1])
				i += 2
				continue
			}
			if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			i++
		} else if c == '/' && i+1 < n && text[i+1] == '/' {
			for i < n && text[i] != '\n' {
				i++
			}
		} else if c == '/' && i+1 < n && text[i+1] == '*' {
			i += 2
			for i+1 < n && !(text[i] == '*' && text[i+1] == '/') {
				i++
			}
			i += 2
		} else {
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
