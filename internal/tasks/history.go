package tasks

import (
	"sync"
)

type HistoryStore struct {
	mu       sync.RWMutex
	capacity int
	records  []*RunRecord
}

func NewHistoryStore(capacity int) *HistoryStore {
	if capacity <= 0 {
		capacity = 50
	}
	return &HistoryStore{
		capacity: capacity,
		records:  make([]*RunRecord, 0, capacity),
	}
}

// Add prepends a new record to the store, dropping oldest if capacity is exceeded.
func (s *HistoryStore) Add(rec *RunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Newest first
	s.records = append([]*RunRecord{rec}, s.records...)
	if len(s.records) > s.capacity {
		s.records = s.records[:s.capacity]
	}
}

// List returns records (newest first). If taskID is non-empty, only matching records are returned.
func (s *HistoryStore) List(taskID string, limit int) []*RunRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > s.capacity {
		limit = s.capacity
	}

	res := make([]*RunRecord, 0, limit)
	for _, r := range s.records {
		if taskID != "" && r.TaskID != taskID {
			continue
		}
		dup := *r
		res = append(res, &dup)
		if len(res) >= limit {
			break
		}
	}
	return res
}
