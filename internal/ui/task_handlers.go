package ui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"wan6gate/internal/config"
	"wan6gate/internal/tasks"
)

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, s.cfg.ListTasks())
	case http.MethodPost:
		var task config.Task
		if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request body")
			return
		}
		if task.ID == "" {
			buf := make([]byte, 6)
			_, _ = rand.Read(buf)
			task.ID = fmt.Sprintf("task_%s", hex.EncodeToString(buf))
		}
		// Pre-validate cron expression
		if _, err := tasks.ParseCron(task.Cron); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid cron expression: %v", err))
			return
		}
		if err := s.cfg.UpsertTask(&task); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if s.cfgPath != "" {
			_ = s.cfg.Save(s.cfgPath)
		}
		s.log.Info("task created", "id", task.ID, "name", task.Name, "cron", task.Cron, "type", task.Type)
		writeOK(w, &task)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleTaskItem(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if sub == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	parts := strings.Split(sub, "/")
	id := parts[0]
	if id == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	// /api/tasks/{id}/run
	if len(parts) == 2 && parts[1] == "run" {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if s.taskMgr == nil {
			writeErr(w, http.StatusInternalServerError, "task manager not initialized")
			return
		}
		rec, err := s.taskMgr.RunTask(id, "manual")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, rec)
		return
	}

	// /api/tasks/{id}/history
	if len(parts) == 2 && parts[1] == "history" {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if s.taskMgr == nil {
			writeOK(w, []*tasks.RunRecord{})
			return
		}
		writeOK(w, s.taskMgr.History(id, 50))
		return
	}

	if len(parts) != 1 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		task, ok := s.cfg.GetTask(id)
		if !ok {
			writeErr(w, http.StatusNotFound, "task not found")
			return
		}
		writeOK(w, task)
	case http.MethodPut, http.MethodPatch:
		existing, ok := s.cfg.GetTask(id)
		if !ok {
			writeErr(w, http.StatusNotFound, "task not found")
			return
		}
		var updated config.Task
		if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request body")
			return
		}
		updated.ID = id
		// Pre-validate cron expression
		if _, err := tasks.ParseCron(updated.Cron); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("invalid cron expression: %v", err))
			return
		}
		// Preserve createdAt if not sent
		if updated.CreatedAt.IsZero() {
			updated.CreatedAt = existing.CreatedAt
		}
		if err := s.cfg.UpsertTask(&updated); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if s.cfgPath != "" {
			_ = s.cfg.Save(s.cfgPath)
		}
		s.log.Info("task updated", "id", updated.ID, "name", updated.Name, "cron", updated.Cron)
		writeOK(w, &updated)
	case http.MethodDelete:
		if err := s.cfg.DeleteTask(id); err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		if s.cfgPath != "" {
			_ = s.cfg.Save(s.cfgPath)
		}
		s.log.Info("task deleted", "id", id)
		writeOK(w, map[string]string{"deleted": id})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
