package tasks

import (
	"fmt"
	"testing"
	"time"
)

func TestHistoryStore(t *testing.T) {
	hs := NewHistoryStore(50)

	// Add 60 records across 2 tasks
	for i := 1; i <= 60; i++ {
		taskID := "task_1"
		if i%2 == 0 {
			taskID = "task_2"
		}
		hs.Add(&RunRecord{
			ID:          fmt.Sprintf("run_%03d", i),
			TaskID:      taskID,
			TaskName:    "Test Task",
			TriggerType: "cron",
			Status:      "success",
			ExitCode:    0,
			DurationMs:  int64(i * 10),
			StartedAt:   time.Now().Add(time.Duration(i) * time.Minute),
			FinishedAt:  time.Now().Add(time.Duration(i)*time.Minute + 100*time.Millisecond),
		})
	}

	// 1. Total capacity check
	all := hs.List("", 100)
	if len(all) != 50 {
		t.Fatalf("expected 50 records in store, got %d", len(all))
	}

	// 2. Newest first check: first record should be run_060
	if all[0].ID != "run_060" {
		t.Fatalf("expected newest record run_060 first, got %s", all[0].ID)
	}

	// 3. Filter by task_1
	task1Runs := hs.List("task_1", 100)
	if len(task1Runs) != 25 {
		t.Fatalf("expected 25 records for task_1, got %d", len(task1Runs))
	}
	for _, r := range task1Runs {
		if r.TaskID != "task_1" {
			t.Fatalf("unexpected task ID: %s", r.TaskID)
		}
	}

	// 4. Limit check
	limited := hs.List("", 10)
	if len(limited) != 10 {
		t.Fatalf("expected 10 records with limit, got %d", len(limited))
	}
}
