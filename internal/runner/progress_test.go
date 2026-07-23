package runner

import (
	"bytes"
	"strings"
	"testing"
)

func TestProgressWriterEmitsSanitizedSummaryAndPreservesRawLog(t *testing.T) {
	var raw bytes.Buffer
	var progress bytes.Buffer
	writer := newProgressWriter(&raw, &progress, "transfer")
	line := `{"object":"private-name.zip","stats":{"bytes":5368709120,"totalBytes":10737418240,"speed":20971520,"eta":256,"errors":0}}` + "\n"

	if _, err := writer.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if raw.String() != line {
		t.Fatalf("raw log changed: %q", raw.String())
	}
	got := progress.String()
	for _, want := range []string{"phase=transfer", "progress=50.0%", "transferred=5.00GiB", "total=10.00GiB", "speed=20.00MiB/s", "eta=4m16s", "errors=0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "private-name") {
		t.Fatalf("progress exposed object name: %q", got)
	}
}

func TestProgressWriterIgnoresNonStatsLines(t *testing.T) {
	var raw bytes.Buffer
	var progress bytes.Buffer
	writer := newProgressWriter(&raw, &progress, "verification")
	line := `{"level":"info","msg":"checking private-name.zip"}` + "\n"
	if _, err := writer.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if progress.Len() != 0 {
		t.Fatalf("unexpected progress output: %q", progress.String())
	}
}
