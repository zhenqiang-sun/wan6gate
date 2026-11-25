package config

import (
	"testing"
)

func TestTaskConfigCRUD(t *testing.T) {
	cfg := New()
	if len(cfg.ListTasks()) != 0 {
		t.Fatalf("expected 0 tasks initially, got %d", len(cfg.ListTasks()))
	}

	task := &Task{
		ID:             "task_test_01",
		Name:           "Reboot Sub Router",
		Enabled:        true,
		Type:           "shell",
		Cron:           "0 4 * * 1",
		TimeoutSeconds: 60,
		NotifyPolicy:   "on_failure",
		ShellCommand:   "reboot",
	}

	if err := cfg.UpsertTask(task); err != nil {
		t.Fatalf("upsert task failed: %v", err)
	}

	got, ok := cfg.GetTask("task_test_01")
	if !ok || got.Name != "Reboot Sub Router" {
		t.Fatalf("failed to retrieve task: %+v", got)
	}

	cfg.UpdateTaskRunState("task_test_01", "success", 120, 0, "")
	got, _ = cfg.GetTask("task_test_01")
	if got.LastRunStatus != "success" || got.LastRunDurationMs != 120 {
		t.Fatalf("failed to update run state: %+v", got)
	}

	if err := cfg.DeleteTask("task_test_01"); err != nil {
		t.Fatalf("delete task failed: %v", err)
	}
	if _, ok := cfg.GetTask("task_test_01"); ok {
		t.Fatalf("task should have been deleted")
	}
}
