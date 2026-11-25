// Package historystore persists hourly metric snapshots as JSONL files,
// one per month, retained for retentionDays (default 90).
//
// File path: <baseDir>/YYYY-MM.jsonl
// Each line is a Snapshot JSON record.
package historystore

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"wan6gate/internal/metrics"
)

const (
	defaultRetentionDays = 90
	gcEvery              = 24 * time.Hour
)

// CounterRecord matches the persisted shape per service.
type CounterRecord struct {
	Accepted int64 `json:"accepted"`
	Errors   int64 `json:"errors"`
	BytesIn  int64 `json:"bytes_in"`
	BytesOut int64 `json:"bytes_out"`
}

// Snapshot is one hourly record.
type Snapshot struct {
	TS       int64                    `json:"ts"` // unix seconds
	Services map[string]CounterRecord `json:"services"`
}

// Store appends snapshots to disk and exposes range queries.
type Store struct {
	baseDir       string
	retentionDays int
	log           *slog.Logger
	stop          chan struct{}
}

func New(baseDir string, log *slog.Logger) *Store {
	return &Store{
		baseDir:       baseDir,
		retentionDays: defaultRetentionDays,
		log:           log.With("module", "historystore"),
		stop:          make(chan struct{}),
	}
}

// Run starts the periodic appender. Blocks until ctx is done or Stop() called.
// Snapshot fires on each hour boundary; first one ~immediately so we have a baseline.
func (s *Store) Run() {
	if err := os.MkdirAll(s.baseDir, 0700); err != nil {
		s.log.Warn("mkdir basedir", "err", err)
		return
	}
	// Initial baseline within 30s of startup so reboots leave a marker.
	time.Sleep(30 * time.Second)
	s.AppendNow()

	for {
		// Sleep until next top-of-hour
		next := nextHour(time.Now())
		select {
		case <-s.stop:
			return
		case <-time.After(time.Until(next)):
		}
		s.AppendNow()
		s.gcOnce()
	}
}

func (s *Store) Stop() { close(s.stop) }

// AppendNow takes a current Default registry snapshot and appends one record.
func (s *Store) AppendNow() {
	now := time.Now().UTC()
	rec := Snapshot{
		TS:       now.Unix(),
		Services: make(map[string]CounterRecord),
	}
	for id, snap := range metrics.Default.SnapshotAll() {
		rec.Services[id] = CounterRecord{
			Accepted: snap.Accepted,
			Errors:   snap.Errors,
			BytesIn:  snap.BytesIn,
			BytesOut: snap.BytesOut,
		}
	}
	if err := s.append(now, rec); err != nil {
		s.log.Warn("append", "err", err)
	}
}

func (s *Store) append(t time.Time, rec Snapshot) error {
	path := s.fileFor(t)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// QueryRange returns records whose ts falls within [from, to] for any of the IDs.
func (s *Store) QueryRange(from, to time.Time, idFilter map[string]bool) ([]Snapshot, error) {
	var out []Snapshot
	files, err := s.listFilesBetween(from, to)
	if err != nil {
		return nil, err
	}
	for _, fp := range files {
		recs, err := s.readFile(fp)
		if err != nil {
			s.log.Warn("read", "file", fp, "err", err)
			continue
		}
		for _, r := range recs {
			t := time.Unix(r.TS, 0)
			if t.Before(from) || t.After(to) {
				continue
			}
			if idFilter != nil && len(idFilter) > 0 {
				filtered := make(map[string]CounterRecord)
				for k, v := range r.Services {
					if idFilter[k] {
						filtered[k] = v
					}
				}
				r.Services = filtered
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

// gcOnce removes files older than retention.
func (s *Store) gcOnce() {
	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)
	dir, err := os.Open(s.baseDir)
	if err != nil {
		return
	}
	names, _ := dir.Readdirnames(-1)
	dir.Close()
	for _, name := range names {
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		t, ok := parseFileMonth(name)
		if !ok {
			continue
		}
		// Keep month if any day in it is within retention.
		monthEnd := t.AddDate(0, 1, 0).Add(-time.Second)
		if monthEnd.Before(cutoff) {
			os.Remove(filepath.Join(s.baseDir, name))
			s.log.Info("history gc removed", "file", name)
		}
	}
}

func (s *Store) fileFor(t time.Time) string {
	return filepath.Join(s.baseDir, t.UTC().Format("2006-01")+".jsonl")
}

func (s *Store) listFilesBetween(from, to time.Time) ([]string, error) {
	dir, err := os.Open(s.baseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	names, _ := dir.Readdirnames(-1)
	dir.Close()
	sort.Strings(names)
	var out []string
	for _, n := range names {
		if !strings.HasSuffix(n, ".jsonl") {
			continue
		}
		t, ok := parseFileMonth(n)
		if !ok {
			continue
		}
		monthEnd := t.AddDate(0, 1, 0).Add(-time.Second)
		if monthEnd.Before(from) || t.After(to) {
			continue
		}
		out = append(out, filepath.Join(s.baseDir, n))
	}
	return out, nil
}

func (s *Store) readFile(path string) ([]Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Snapshot
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var r Snapshot
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func nextHour(t time.Time) time.Time {
	return t.Truncate(time.Hour).Add(time.Hour)
}

func parseFileMonth(name string) (time.Time, bool) {
	stem := strings.TrimSuffix(name, ".jsonl")
	t, err := time.Parse("2006-01", stem)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
