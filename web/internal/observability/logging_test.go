package observability

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingWriterArchivesLargeLog(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, activeLogName)
	if err := os.WriteFile(active, bytes.Repeat([]byte("x"), maxLogBytes), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := newRotatingWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("next\n")); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(active)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len("next\n")) {
		t.Fatalf("active log size = %d, want %d", info.Size(), len("next\n"))
	}

	matches, err := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("archive count = %d, want 1", len(matches))
	}
}
