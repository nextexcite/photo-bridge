package redact

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"sync"
)

var patterns = []struct {
	replacement string
	re          *regexp.Regexp
}{
	{"[REDACTED_EMAIL]", regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)},
	{"/Users/[REDACTED]", regexp.MustCompile(`/Users/[^/\s"']+`)},
	{"/home/[REDACTED]", regexp.MustCompile(`/home/[^/\s"']+`)},
	{"$1[REDACTED]", regexp.MustCompile(`(?i)(authorization["'=: ]+(?:bearer[ ]+)?)\S+`)},
	{"$1[REDACTED]", regexp.MustCompile(`(?i)((?:access_token|refresh_token|client_secret|password|cookie)["'=: ]+)\S+`)},
	{"$1[REDACTED]", regexp.MustCompile(`(?i)([?&](?:token|key|signature|sig|auth)=)[^&\s]+`)},
}

func String(input string) string {
	result := input
	for _, pattern := range patterns {
		result = pattern.re.ReplaceAllString(result, pattern.replacement)
	}
	return result
}

type LineWriter struct {
	mu     sync.Mutex
	target io.Writer
	buf    bytes.Buffer
}

func NewLineWriter(target io.Writer) *LineWriter {
	return &LineWriter{target: target}
}

func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n := len(p)
	_, _ = w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			_, _ = w.buf.WriteString(line)
			break
		}
		if _, err := io.WriteString(w.target, String(line)); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func (w *LineWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() == 0 {
		return nil
	}
	_, err := io.WriteString(w.target, String(strings.TrimSuffix(w.buf.String(), "\x00")))
	w.buf.Reset()
	return err
}
