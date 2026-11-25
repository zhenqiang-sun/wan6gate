package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"wan6gate/internal/config"
	"wan6gate/internal/notify"
)

type Manager struct {
	cfg     *config.Config
	cfgPath string
	log     *slog.Logger
	notif   *notify.Notifier
	history *HistoryStore

	runningMu sync.Mutex
	running   map[string]bool
}

func NewManager(cfg *config.Config, cfgPath string, log *slog.Logger, notif *notify.Notifier) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		cfg:     cfg,
		cfgPath: cfgPath,
		log:     log.With("module", "tasks"),
		notif:   notif,
		history: NewHistoryStore(50),
		running: make(map[string]bool),
	}
}

// Start launches the scheduler loop in the background. It exits when ctx is canceled.
func (m *Manager) Start(ctx context.Context) {
	go m.loop(ctx)
}

func (m *Manager) loop(ctx context.Context) {
	m.log.Info("task scheduler loop started")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var lastMinute int = -1

	for {
		select {
		case <-ctx.Done():
			m.log.Info("task scheduler loop stopped")
			return
		case now := <-ticker.C:
			currentMinute := now.Minute()
			// Execute once per minute at the transition of minute
			if currentMinute != lastMinute {
				lastMinute = currentMinute
				m.checkAndTrigger(now)
			}
		}
	}
}

func (m *Manager) checkAndTrigger(now time.Time) {
	tasks := m.cfg.ListTasks()
	for _, task := range tasks {
		if !task.Enabled {
			continue
		}
		sched, err := ParseCron(task.Cron)
		if err != nil {
			m.log.Warn("skipping task with invalid cron", "task", task.ID, "cron", task.Cron, "err", err)
			continue
		}
		if sched.Matches(now) {
			m.log.Info("triggering scheduled task", "task", task.ID, "name", task.Name)
			go func(t *config.Task) {
				_, err := m.RunTask(t.ID, "cron")
				if err != nil {
					m.log.Warn("scheduled task run error", "task", t.ID, "err", err)
				}
			}(task)
		}
	}
}

// RunTask executes a task immediately. triggerType is "cron" or "manual".
func (m *Manager) RunTask(id string, triggerType string) (*RunRecord, error) {
	task, ok := m.cfg.GetTask(id)
	if !ok {
		return nil, fmt.Errorf("task %q not found", id)
	}

	m.runningMu.Lock()
	if m.running[id] {
		m.runningMu.Unlock()
		return nil, fmt.Errorf("task %q is already running", id)
	}
	m.running[id] = true
	m.runningMu.Unlock()

	defer func() {
		m.runningMu.Lock()
		delete(m.running, id)
		m.runningMu.Unlock()
	}()

	m.log.Info("executing task", "id", task.ID, "name", task.Name, "trigger", triggerType)
	rec := Execute(context.Background(), task, triggerType)

	// Update in-memory & persistent config run state
	m.cfg.UpdateTaskRunState(task.ID, rec.Status, rec.DurationMs, rec.ExitCode, rec.Error)
	if m.cfgPath != "" {
		if err := m.cfg.Save(m.cfgPath); err != nil {
			m.log.Warn("failed to persist config after task run", "err", err)
		}
	}

	// Store in ring buffer
	m.history.Add(rec)

	// Send notification if applicable
	if m.notif != nil {
		m.notif.OnTaskRun(task.Name, triggerType, rec.Status, rec.DurationMs, rec.ExitCode, rec.Output, rec.Error, task.NotifyPolicy)
	}

	m.log.Info("task finished", "id", task.ID, "status", rec.Status, "duration_ms", rec.DurationMs, "exit_code", rec.ExitCode)
	return rec, nil
}

// History returns execution history records.
func (m *Manager) History(taskID string, limit int) []*RunRecord {
	return m.history.List(taskID, limit)
}
