package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

type progressWriter struct {
	mu       sync.Mutex
	raw      io.Writer
	progress io.Writer
	phase    string
	buf      bytes.Buffer
}

type rcloneProgressLine struct {
	Stats *struct {
		Bytes      int64   `json:"bytes"`
		TotalBytes int64   `json:"totalBytes"`
		Speed      float64 `json:"speed"`
		ETA        *int64  `json:"eta"`
		Errors     int64   `json:"errors"`
	} `json:"stats"`
}

func newProgressWriter(raw, progress io.Writer, phase string) *progressWriter {
	return &progressWriter{raw: raw, progress: progress, phase: phase}
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.raw.Write(p); err != nil {
		return 0, err
	}
	_, _ = w.buf.Write(p)
	for {
		line, err := w.buf.ReadBytes('\n')
		if err != nil {
			_, _ = w.buf.Write(line)
			break
		}
		w.writeProgress(bytes.TrimSpace(line))
	}
	return len(p), nil
}

func (w *progressWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.writeProgress(bytes.TrimSpace(w.buf.Bytes()))
		w.buf.Reset()
	}
	return nil
}

func (w *progressWriter) writeProgress(line []byte) {
	if len(line) == 0 {
		return
	}
	var event rcloneProgressLine
	if json.Unmarshal(line, &event) != nil || event.Stats == nil || event.Stats.TotalBytes <= 0 {
		return
	}
	percent := float64(event.Stats.Bytes) * 100 / float64(event.Stats.TotalBytes)
	eta := "unknown"
	if event.Stats.ETA != nil && *event.Stats.ETA >= 0 {
		eta = (time.Duration(*event.Stats.ETA) * time.Second).Round(time.Second).String()
	}
	_, _ = fmt.Fprintf(
		w.progress,
		"photo-bridge: phase=%s progress=%.1f%% transferred=%s total=%s speed=%s/s eta=%s errors=%d\n",
		w.phase,
		percent,
		formatBytes(event.Stats.Bytes),
		formatBytes(event.Stats.TotalBytes),
		formatBytes(int64(event.Stats.Speed)),
		eta,
		event.Stats.Errors,
	)
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%dB", value)
	}
	divisor, exponent := int64(unit), 0
	for amount := value / unit; amount >= unit && exponent < 5; amount /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.2f%ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}
