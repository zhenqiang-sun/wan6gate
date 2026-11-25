package tasks

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"wan6gate/internal/config"
)

func TestManagerRunTask(t *testing.T) {
	cfg := config.New()
	task := &config.Task{
		ID:             "task_echo",
		Name:           "Echo Test",
		Enabled:        true,
		Type:           "shell",
		Cron:           "* * * * *",
		ShellCommand:   "echo 'manager test output'",
		TimeoutSeconds: 5,
	}
	if err := cfg.UpsertTask(task); err != nil {
		t.Fatalf("upsert task failed: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(cfg, "", logger, nil)

	rec, err := mgr.RunTask("task_echo", "manual")
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}
	if rec.Status != "success" || rec.ExitCode != 0 {
		t.Fatalf("expected success, got status=%s code=%d", rec.Status, rec.ExitCode)
	}

	// Verify history store has record
	history := mgr.History("task_echo", 10)
	if len(history) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(history))
	}
	if history[0].ID != rec.ID {
		t.Fatalf("expected history ID %s, got %s", rec.ID, history[0].ID)
	}

	// Verify config updated
	saved, _ := cfg.GetTask("task_echo")
	if saved.LastRunStatus != "success" || saved.LastRunAt == "" {
		t.Fatalf("expected config run state updated: %+v", saved)
	}
}

func TestManagerConcurrencyLock(t *testing.T) {
	cfg := config.New()
	task := &config.Task{
		ID:             "task_sleep",
		Name:           "Sleep Test",
		Enabled:        true,
		Type:           "shell",
		Cron:           "* * * * *",
		ShellCommand:   "sleep 1",
		TimeoutSeconds: 5,
	}
	cfg.UpsertTask(task)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(cfg, "", logger, nil)

	// Start task in background
	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = mgr.RunTask("task_sleep", "manual")
	}()

	<-started
	time.Sleep(50 * time.Millisecond) // Ensure it started executing

	// Concurrently attempt to run same task
	_, err := mgr.RunTask("task_sleep", "manual")
	if err == nil {
		t.Fatalf("expected concurrency error when task is already running, got nil")
	}
}
