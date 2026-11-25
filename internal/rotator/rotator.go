package rotator

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Default settings
const (
	DefaultMaxSize    = 2 * 1024 * 1024 // 2 MB
	DefaultMaxBackups = 2
)

// Options configures the rotating writer.
type Options struct {
	MaxSize    int64 // Maximum size in bytes before rotation (default: 2MB)
	MaxBackups int   // Maximum number of rotated files to retain (default: 2)
}

// Writer is an io.WriteCloser that writes to a file and rotates when
// the size reaches MaxSize, keeping up to MaxBackups rotated files.
type Writer struct {
	mu         sync.Mutex
	path       string
	maxSize    int64
	maxBackups int
	size       int64
	file       *os.File
}

// New creates or opens a rotating log writer.
// If the existing file is already larger than MaxSize, it is rotated immediately.
func New(path string, opts Options) (*Writer, error) {
	if opts.MaxSize <= 0 {
		opts.MaxSize = DefaultMaxSize
	}
	if opts.MaxBackups <= 0 {
		opts.MaxBackups = DefaultMaxBackups
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("mkdir log dir: %w", err)
	}

	w := &Writer{
		path:       path,
		maxSize:    opts.MaxSize,
		maxBackups: opts.MaxBackups,
	}

	// Check if existing file exceeds MaxSize
	if st, err := os.Stat(path); err == nil {
		if st.Size() >= w.maxSize {
			if err := w.rotateFiles(); err != nil {
				return nil, fmt.Errorf("initial rotation: %w", err)
			}
		} else {
			w.size = st.Size()
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	w.file = f
	return w, nil
}

// Write writes data to the log file, rotating if necessary.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
		if err != nil {
			return 0, err
		}
		w.file = f
	}

	if w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the underlying log file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}

// rotate closes the current file, cascades backup files, and opens a fresh file.
// Caller MUST hold w.mu.
func (w *Writer) rotate() error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}

	if err := w.rotateFiles(); err != nil {
		return err
	}

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return fmt.Errorf("open fresh log file: %w", err)
	}
	w.file = f
	w.size = 0
	return nil
}

// rotateFiles cascades existing files: .2 -> .3, .1 -> .2, .log -> .1
func (w *Writer) rotateFiles() error {
	// Remove anything beyond maxBackups
	excess := fmt.Sprintf("%s.%d", w.path, w.maxBackups)
	_ = os.Remove(excess)

	for i := w.maxBackups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", w.path, i)
		dst := fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}

	if _, err := os.Stat(w.path); err == nil {
		_ = os.Rename(w.path, fmt.Sprintf("%s.1", w.path))
	}
	return nil
}
