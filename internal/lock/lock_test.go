package lock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	l, err := Try(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Try(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("second Try: %v, want ErrHeld", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	l2, err := Try(path)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l2.Release()
}
