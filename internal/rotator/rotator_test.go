package rotator

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRotator_BasicWriteAndRotate(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")

	// 100 bytes max size, keep 2 backups
	w, err := New(logPath, Options{
		MaxSize:    100,
		MaxBackups: 2,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	// Write 60 bytes
	chunk1 := []byte("123456789012345678901234567890123456789012345678901234567890") // 60
	if n, err := w.Write(chunk1); err != nil || n != 60 {
		t.Fatalf("Write chunk1: n=%d, err=%v", n, err)
	}

	// Should not rotate yet
	if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
		t.Fatalf("expected .1 to not exist yet")
	}

	// Write another 60 bytes -> total 120 > 100 -> should rotate
	chunk2 := []byte("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz12345678") // 60
	if n, err := w.Write(chunk2); err != nil || n != 60 {
		t.Fatalf("Write chunk2: n=%d, err=%v", n, err)
	}

	// Now test.log.1 must exist (with chunk1) and test.log must have chunk2
	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Fatalf("expected .1 to exist: %v", err)
	}

	// Write 60 bytes again -> should rotate again: .1 becomes .2, test.log becomes .1
	if _, err := w.Write(chunk1); err != nil {
		t.Fatalf("Write chunk3: %v", err)
	}

	if _, err := os.Stat(logPath + ".2"); err != nil {
		t.Fatalf("expected .2 to exist: %v", err)
	}

	// Write 60 bytes again -> should rotate again: .2 should be removed (since maxBackups=2)
	if _, err := w.Write(chunk2); err != nil {
		t.Fatalf("Write chunk4: %v", err)
	}

	// Ensure .3 does NOT exist
	if _, err := os.Stat(logPath + ".3"); !os.IsNotExist(err) {
		t.Fatalf("expected .3 to NOT exist, maxBackups=2")
	}
}

func TestRotator_InitialRotationIfTooLarge(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "large.log")

	// Pre-create 200 bytes file
	if err := os.WriteFile(logPath, make([]byte, 200), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// New with MaxSize 100
	w, err := New(logPath, Options{
		MaxSize:    100,
		MaxBackups: 2,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	// Initial rotation should have moved large.log to large.log.1
	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Fatalf("expected initial rotation to create .1: %v", err)
	}

	// Write fresh small data
	if _, err := w.Write([]byte("fresh")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	st, err := os.Stat(logPath)
	if err != nil || st.Size() != 5 {
		t.Fatalf("expected current file size 5, got err=%v, size=%d", err, st.Size())
	}
}

func TestRotator_ConcurrentWrites(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "concurrent.log")

	w, err := New(logPath, Options{
		MaxSize:    500,
		MaxBackups: 3,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				msg := fmt.Sprintf("worker %d line %d\n", workerID, j)
				_, _ = w.Write([]byte(msg))
			}
		}(i)
	}
	wg.Wait()

	// Verification that close works and files exist
	if err := w.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected log file to exist: %v", err)
	}
}
