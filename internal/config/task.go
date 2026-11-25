package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Task struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	Type           string    `json:"type"`            // "shell" | "http"
	Cron           string    `json:"cron"`            // 5-field cron expression
	TimeoutSeconds int       `json:"timeout_seconds"` // default 60
	NotifyPolicy   string    `json:"notify_policy"`   // "never" | "on_failure" | "always"
	ShellCommand   string    `json:"shell_command,omitempty"`
	HTTPMethod     string    `json:"http_method,omitempty"`
	HTTPURL        string    `json:"http_url,omitempty"`
	HTTPHeaders    string    `json:"http_headers,omitempty"`
	HTTPBody       string    `json:"http_body,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	LastRunAt         string `json:"last_run_at,omitempty"`
	LastRunStatus     string `json:"last_run_status,omitempty"` // "success" | "failure" | "running"
	LastRunDurationMs int64  `json:"last_run_duration_ms,omitempty"`
	LastRunExitCode   int    `json:"last_run_exit_code,omitempty"`
	LastRunError      string `json:"last_run_error,omitempty"`
}

func (t *Task) Validate() error {
	if !validIDRegex.MatchString(t.ID) {
		return errors.New("id must match [a-zA-Z0-9_-]{1,32}")
	}
	if len(strings.TrimSpace(t.Name)) == 0 {
		return errors.New("name is required")
	}
	if len(t.Name) > 64 {
		return errors.New("name too long (>64)")
	}
	typ := strings.ToLower(strings.TrimSpace(t.Type))
	if typ != "shell" && typ != "http" {
		return fmt.Errorf("invalid task type: %q (must be shell or http)", t.Type)
	}
	t.Type = typ

	if len(strings.TrimSpace(t.Cron)) == 0 {
		return errors.New("cron expression is required")
	}

	if t.TimeoutSeconds <= 0 {
		t.TimeoutSeconds = 60
	} else if t.TimeoutSeconds > 3600 {
		return fmt.Errorf("timeout too large (%d > 3600)", t.TimeoutSeconds)
	}

	policy := strings.ToLower(strings.TrimSpace(t.NotifyPolicy))
	if policy == "" {
		policy = "on_failure"
	}
	if policy != "never" && policy != "on_failure" && policy != "always" {
		return fmt.Errorf("invalid notify policy: %q (must be never, on_failure, or always)", t.NotifyPolicy)
	}
	t.NotifyPolicy = policy

	if t.Type == "shell" {
		if len(strings.TrimSpace(t.ShellCommand)) == 0 {
			return errors.New("shell_command is required for shell task")
		}
	} else if t.Type == "http" {
		if len(strings.TrimSpace(t.HTTPURL)) == 0 {
			return errors.New("http_url is required for http task")
		}
		if t.HTTPMethod == "" {
			t.HTTPMethod = "GET"
		}
		t.HTTPMethod = strings.ToUpper(strings.TrimSpace(t.HTTPMethod))
	}

	return nil
}

// ListTasks returns a shallow copy of all tasks.
func (c *Config) ListTasks() []*Task {
	c.mu.RLock()
	defer c.mu.RUnlock()
	res := make([]*Task, len(c.Tasks))
	for i, t := range c.Tasks {
		dup := *t
		res[i] = &dup
	}
	return res
}

// GetTask returns a copy of the task matching id.
func (c *Config) GetTask(id string) (*Task, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, t := range c.Tasks {
		if t.ID == id {
			dup := *t
			return &dup, true
		}
	}
	return nil, false
}

// UpsertTask adds or updates a task by ID.
func (c *Config) UpsertTask(task *Task) error {
	if err := task.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for i, t := range c.Tasks {
		if t.ID == task.ID {
			task.CreatedAt = t.CreatedAt
			task.UpdatedAt = now
			// Preserve run state if not explicitly overwritten
			if task.LastRunAt == "" {
				task.LastRunAt = t.LastRunAt
				task.LastRunStatus = t.LastRunStatus
				task.LastRunDurationMs = t.LastRunDurationMs
				task.LastRunExitCode = t.LastRunExitCode
				task.LastRunError = t.LastRunError
			}
			c.Tasks[i] = task
			return nil
		}
	}

	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	c.Tasks = append(c.Tasks, task)
	return nil
}

// DeleteTask removes a task by ID.
func (c *Config) DeleteTask(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, t := range c.Tasks {
		if t.ID == id {
			c.Tasks = append(c.Tasks[:i], c.Tasks[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("task %q not found", id)
}

// UpdateTaskRunState updates the runtime outcome of a task.
func (c *Config) UpdateTaskRunState(id string, status string, durationMs int64, exitCode int, errStr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.Tasks {
		if t.ID == id {
			t.LastRunAt = time.Now().Format(time.RFC3339)
			t.LastRunStatus = status
			t.LastRunDurationMs = durationMs
			t.LastRunExitCode = exitCode
			t.LastRunError = errStr
			return
		}
	}
}
