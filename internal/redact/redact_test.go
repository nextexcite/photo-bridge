package redact

import (
	"bytes"
	"strings"
	"testing"
)

func TestStringRedactsSensitiveShapes(t *testing.T) {
	input := "email user@example.com /Users/example-user/work access_token=abc123 password:secret"
	got := String(input)
	for _, leaked := range []string{"user@example.com", "/Users/example-user", "abc123", "secret"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("redaction leaked %q in %q", leaked, got)
		}
	}
}

func TestLineWriterHandlesSplitWrites(t *testing.T) {
	var out bytes.Buffer
	w := NewLineWriter(&out)
	_, _ = w.Write([]byte("user@exam"))
	_, _ = w.Write([]byte("ple.com\nnext"))
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "user@example.com") {
		t.Fatalf("email was not redacted: %q", out.String())
	}
	if !strings.Contains(out.String(), "next") {
		t.Fatalf("trailing data was lost: %q", out.String())
	}
}
