package ui

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"wan6gate/internal/config"
	"wan6gate/internal/tasks"
)

func newTestServerWithTasks(t *testing.T) (*Server, *config.Config, *tasks.Manager) {
	cfg := config.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	taskMgr := tasks.NewManager(cfg, "", logger, nil)
	srv := &Server{
		cfg:     cfg,
		taskMgr: taskMgr,
		log:     logger,
	}
	return srv, cfg, taskMgr
}

func TestTaskHandlers(t *testing.T) {
	srv, cfg, _ := newTestServerWithTasks(t)

	// 1. GET /api/tasks (initially empty)
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rec := httptest.NewRecorder()
	srv.handleTasks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	// 2. POST /api/tasks with invalid cron -> 400
	badBody := []byte(`{"name":"Bad Task","type":"shell","cron":"invalid","shell_command":"reboot"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(badBody))
	rec = httptest.NewRecorder()
	srv.handleTasks(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid cron, got %d", rec.Code)
	}

	// 3. POST /api/tasks with valid task -> 200 OK
	validBody := []byte(`{"id":"task_1","name":"Echo Task","type":"shell","cron":"0 4 * * 1","shell_command":"echo test"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(validBody))
	rec = httptest.NewRecorder()
	srv.handleTasks(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for valid task, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(cfg.ListTasks()) != 1 {
		t.Fatalf("expected 1 task in config, got %d", len(cfg.ListTasks()))
	}

	// 4. POST /api/tasks/task_1/run -> 200 OK
	req = httptest.NewRequest(http.MethodPost, "/api/tasks/task_1/run", nil)
	rec = httptest.NewRecorder()
	srv.handleTaskItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on run, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		OK   bool             `json:"ok"`
		Data *tasks.RunRecord `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.OK || resp.Data.Status != "success" {
		t.Fatalf("expected run success, got %+v", resp)
	}

	// 5. GET /api/tasks/task_1/history -> 200 OK
	req = httptest.NewRequest(http.MethodGet, "/api/tasks/task_1/history", nil)
	rec = httptest.NewRecorder()
	srv.handleTaskItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on history, got %d", rec.Code)
	}

	// 6. DELETE /api/tasks/task_1 -> 200 OK
	req = httptest.NewRequest(http.MethodDelete, "/api/tasks/task_1", nil)
	rec = httptest.NewRecorder()
	srv.handleTaskItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on delete, got %d", rec.Code)
	}
	if len(cfg.ListTasks()) != 0 {
		t.Fatalf("expected 0 tasks after delete, got %d", len(cfg.ListTasks()))
	}
}
