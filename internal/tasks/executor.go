package tasks

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"wan6gate/internal/config"
)

const MaxOutputBytes = 64 * 1024 // 64KB max captured output

type RunRecord struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	TaskName    string    `json:"task_name"`
	TriggerType string    `json:"trigger_type"` // "cron" | "manual"
	Status      string    `json:"status"`       // "success" | "failure"
	ExitCode    int       `json:"exit_code"`
	DurationMs  int64     `json:"duration_ms"`
	Output      string    `json:"output"`
	Error       string    `json:"error,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
}

func Execute(ctx context.Context, task *config.Task, triggerType string) *RunRecord {
	randID := make([]byte, 6)
	_, _ = rand.Read(randID)
	rec := &RunRecord{
		ID:          fmt.Sprintf("run_%x", randID),
		TaskID:      task.ID,
		TaskName:    task.Name,
		TriggerType: triggerType,
		StartedAt:   time.Now(),
	}

	timeoutSec := task.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	if task.Type == "http" {
		executeHTTP(execCtx, task, rec)
	} else {
		executeShell(execCtx, task, rec)
	}

	rec.FinishedAt = time.Now()
	rec.DurationMs = rec.FinishedAt.Sub(rec.StartedAt).Milliseconds()
	return rec
}

func executeShell(ctx context.Context, task *config.Task, rec *RunRecord) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", task.ShellCommand)

	// Run the shell in its own process group so a timeout can kill the whole
	// process tree (shell + its children) instead of only the shell itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// On timeout, kill the entire process group: killing only /bin/sh leaves
	// orphaned children (e.g. "sleep 3") holding the stdout/stderr pipes,
	// which makes cmd.Run() block until those children exit on their own.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			// Process group is already gone; the cancellation is satisfied.
			return nil
		}
		return err
	}
	// Safety net: even if a grandchild escaped the process group and keeps a
	// pipe open, don't block forever waiting for the I/O to drain.
	cmd.WaitDelay = 500 * time.Millisecond

	var buf limitedBuffer
	buf.limit = MaxOutputBytes
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	rec.Output = buf.String()

	if err != nil {
		rec.Status = "failure"
		// Check the context first: on timeout the process is SIGKILLed, which
		// surfaces as *exec.ExitError with exit code -1 and would otherwise be
		// misreported as a plain "exit code -1" failure.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			rec.ExitCode = -1
			rec.Error = "execution timed out"
		} else {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				rec.ExitCode = exitErr.ExitCode()
				rec.Error = fmt.Sprintf("exit code %d", rec.ExitCode)
			} else {
				rec.ExitCode = -1
				rec.Error = err.Error()
			}
		}
	} else {
		rec.Status = "success"
		rec.ExitCode = 0
	}
}

func executeHTTP(ctx context.Context, task *config.Task, rec *RunRecord) {
	method := task.HTTPMethod
	if method == "" {
		method = "GET"
	}

	var bodyReader io.Reader
	if task.HTTPBody != "" {
		bodyReader = strings.NewReader(task.HTTPBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, task.HTTPURL, bodyReader)
	if err != nil {
		rec.Status = "failure"
		rec.ExitCode = 0
		rec.Error = fmt.Sprintf("invalid request: %v", err)
		return
	}

	// Parse custom headers (lines of "Key: Value")
	if task.HTTPHeaders != "" {
		lines := strings.Split(task.HTTPHeaders, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				v := strings.TrimSpace(parts[1])
				req.Header.Set(k, v)
			}
		}
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		rec.Status = "failure"
		rec.ExitCode = 0
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			rec.Error = "request timed out"
		} else {
			rec.Error = err.Error()
		}
		return
	}
	defer resp.Body.Close()

	rec.ExitCode = resp.StatusCode
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, MaxOutputBytes))
	rec.Output = string(bodyBytes)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		rec.Status = "success"
	} else {
		rec.Status = "failure"
		rec.Error = fmt.Sprintf("HTTP %d %s", resp.StatusCode, resp.Status)
	}
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (n int, err error) {
	if b.buf.Len() >= b.limit {
		return len(p), nil // drop silently beyond limit
	}
	remaining := b.limit - b.buf.Len()
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.buf.WriteString("\n...[output truncated]")
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}
