package main

import (
	"bytes"
	"testing"
)

func TestWriteVersion(t *testing.T) {
	original := version
	version = "v0.1.0-test"
	t.Cleanup(func() { version = original })

	var output bytes.Buffer
	if err := writeVersion(&output); err != nil {
		t.Fatalf("writeVersion: %v", err)
	}
	if got, want := output.String(), "ac-cli v0.1.0-test\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}
