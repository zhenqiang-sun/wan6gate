package tasks

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wan6gate/internal/config"
)

func TestExecuteShellSuccess(t *testing.T) {
	task := &config.Task{
		ID:             "task_shell_1",
		Name:           "Echo Test",
		Type:           "shell",
		ShellCommand:   "echo 'hello wan6gate'",
		TimeoutSeconds: 5,
	}
	rec := Execute(context.Background(), task, "manual")
	if rec.Status != "success" || rec.ExitCode != 0 {
		t.Fatalf("expected success, got status=%s exitCode=%d err=%s", rec.Status, rec.ExitCode, rec.Error)
	}
	if !strings.Contains(rec.Output, "hello wan6gate") {
		t.Fatalf("unexpected output: %s", rec.Output)
	}
}

func TestExecuteShellFailure(t *testing.T) {
	task := &config.Task{
		ID:             "task_shell_fail",
		Name:           "Exit 1 Test",
		Type:           "shell",
		ShellCommand:   "exit 2",
		TimeoutSeconds: 5,
	}
	rec := Execute(context.Background(), task, "manual")
	if rec.Status != "failure" || rec.ExitCode != 2 {
		t.Fatalf("expected failure with exit code 2, got status=%s exitCode=%d", rec.Status, rec.ExitCode)
	}
}

func TestExecuteShellTimeout(t *testing.T) {
	task := &config.Task{
		ID:             "task_shell_timeout",
		Name:           "Sleep Timeout Test",
		Type:           "shell",
		ShellCommand:   "sleep 3",
		TimeoutSeconds: 1,
	}
	start := time.Now()
	rec := Execute(context.Background(), task, "manual")
	duration := time.Since(start)
	if rec.Status != "failure" {
		t.Fatalf("expected failure on timeout, got %s", rec.Status)
	}
	if rec.Error != "execution timed out" {
		t.Fatalf("expected error %q, got %q", "execution timed out", rec.Error)
	}
	if rec.ExitCode != -1 {
		t.Fatalf("expected exit code -1 on timeout, got %d", rec.ExitCode)
	}
	if duration > 2500*time.Millisecond {
		t.Fatalf("execution took too long (%v), timeout not enforced", duration)
	}
}

func TestExecuteShellOutputTruncation(t *testing.T) {
	// Produce ~100KB of output
	task := &config.Task{
		ID:             "task_shell_large",
		Name:           "Large Output Test",
		Type:           "shell",
		ShellCommand:   "dd if=/dev/zero bs=1024 count=100 2>/dev/null | tr '\\0' 'A'",
		TimeoutSeconds: 5,
	}
	rec := Execute(context.Background(), task, "manual")
	if len(rec.Output) > MaxOutputBytes+100 {
		t.Fatalf("output length %d exceeded MaxOutputBytes (%d)", len(rec.Output), MaxOutputBytes)
	}
}

func TestExecuteHTTPSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"received": %q}`, string(body))
	}))
	defer ts.Close()

	task := &config.Task{
		ID:             "task_http_1",
		Name:           "HTTP Test",
		Type:           "http",
		HTTPMethod:     "POST",
		HTTPURL:        ts.URL,
		HTTPBody:       `{"action":"reboot"}`,
		TimeoutSeconds: 5,
	}
	rec := Execute(context.Background(), task, "manual")
	if rec.Status != "success" || rec.ExitCode != 200 {
		t.Fatalf("expected 200 OK, got status=%s code=%d err=%s", rec.Status, rec.ExitCode, rec.Error)
	}
	if !strings.Contains(rec.Output, "reboot") {
		t.Fatalf("expected body to contain 'reboot', got %s", rec.Output)
	}
}
