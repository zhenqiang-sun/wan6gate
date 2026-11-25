// Package notify dispatches event notifications to outbound webhooks.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // bundle IANA tz database so LoadLocation works on minimal OpenWrt rootfs

	"wan6gate/internal/config"
)

// displayLoc is the time zone used when rendering timestamps in outbound
// notifications. Hardcoded to Asia/Shanghai because (a) the only deployment
// audience is in CST and (b) the router's own /etc/TZ is often unset on stock
// MiWiFi firmware, so time.Local would yield UTC. Override at link time via
// `-X wan6gate/internal/notify.DisplayTZName=Europe/Berlin` if you ever need.
var DisplayTZName = "Asia/Shanghai"

var displayLoc = func() *time.Location {
	if loc, err := time.LoadLocation(DisplayTZName); err == nil {
		return loc
	}
	return time.UTC
}()

// EventType enumerates the events we may notify on.
type EventType string

const (
	EventDaemonStarted EventType = "daemon_started"
	EventDDNSChange    EventType = "ddns_change"
	EventDDNSError     EventType = "ddns_error"
	EventSocks5Ban     EventType = "socks5_ban"
	EventTaskRun       EventType = "task_run"
	EventTest          EventType = "test"
)

// Event is a single notification record.
type Event struct {
	Type   EventType
	Title  string
	Detail string
	When   time.Time
}

// Notifier reads cfg.Notify on each Send to pick up live UI changes.
// Underlying clients (Feishu etc.) are constructed per-call to keep this stateless.
type Notifier struct {
	cfg    *config.Config
	log    *slog.Logger
	banAgg *banAggregator
}

func New(cfg *config.Config, log *slog.Logger) *Notifier {
	n := &Notifier{cfg: cfg, log: log.With("module", "notify")}
	n.banAgg = newBanAggregator(n, banAggregateWindow)
	return n
}

// Send fires an event. Idempotent / non-blocking the goroutine that calls it
// (we spawn an inner goroutine because webhook POST can take a few seconds).
// Returns nil immediately. Errors are logged.
func (n *Notifier) Send(ev Event) {
	if n == nil {
		return
	}
	cfg := n.cfg.GetNotify()
	if !cfg.Enabled || cfg.WebhookURL == "" {
		return
	}
	if !eventEnabled(cfg, ev.Type) {
		return
	}
	if ev.When.IsZero() {
		ev.When = time.Now()
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := postFeishuText(ctx, cfg.WebhookURL, formatText(ev)); err != nil {
			n.log.Warn("notify send failed", "type", ev.Type, "err", err)
			return
		}
		n.log.Info("notify sent", "type", ev.Type)
	}()
}

// SendSync is like Send but blocks (used by /api/notify/test endpoint to surface errors).
func (n *Notifier) SendSync(ctx context.Context, ev Event) error {
	cfg := n.cfg.GetNotify()
	if cfg.WebhookURL == "" {
		return errExt("webhook_url not set")
	}
	if ev.When.IsZero() {
		ev.When = time.Now()
	}
	return postFeishuText(ctx, cfg.WebhookURL, formatText(ev))
}

func eventEnabled(c config.NotifyConfig, t EventType) bool {
	switch t {
	case EventDaemonStarted:
		return c.NotifyOnDaemonStart
	case EventDDNSChange:
		return c.NotifyOnDDNSChange
	case EventDDNSError:
		return c.NotifyOnDDNSError
	case EventSocks5Ban:
		return c.NotifyOnSocks5Ban
	case EventTaskRun:
		return true // Governed per-task by task.NotifyPolicy
	case EventTest:
		return true
	default:
		return false
	}
}

func formatText(ev Event) string {
	emoji := map[EventType]string{
		EventDaemonStarted: "🚀",
		EventDDNSChange:    "🌐",
		EventDDNSError:     "⚠️",
		EventSocks5Ban:     "🚫",
		EventTaskRun:       "⏱️",
		EventTest:          "🧪",
	}[ev.Type]
	if emoji == "" {
		emoji = "ℹ️"
	}
	when := ev.When.In(displayLoc).Format("2006-01-02 15:04:05 -0700")
	if ev.Detail == "" {
		return emoji + " [" + ev.Title + "] @ " + when
	}
	return emoji + " [" + ev.Title + "] @ " + when + "\n" + ev.Detail
}

// OnTaskRun dispatches a notification for a scheduled task run according to its policy.
func (n *Notifier) OnTaskRun(taskName, triggerType, status string, durationMs int64, exitCode int, output, errStr, policy string) {
	if n == nil || policy == "never" {
		return
	}
	if policy == "on_failure" && status != "failure" {
		return
	}

	statusText := "成功"
	if status == "failure" {
		statusText = "失败"
	}

	triggerText := "手动执行"
	if triggerType == "cron" {
		triggerText = "定时调度"
	}

	title := fmt.Sprintf("计划任务%s: %s", statusText, taskName)
	detail := fmt.Sprintf("触发方式: %s\n执行状态: %s\n耗时: %dms\n退出码/状态码: %d",
		triggerText, status, durationMs, exitCode)
	if errStr != "" {
		detail += fmt.Sprintf("\n错误信息: %s", errStr)
	}
	if output != "" {
		outSummary := strings.TrimSpace(output)
		if len(outSummary) > 500 {
			outSummary = outSummary[:500] + "...[截断]"
		}
		detail += fmt.Sprintf("\n输出内容:\n%s", outSummary)
	}

	n.Send(Event{
		Type:   EventTaskRun,
		Title:  title,
		Detail: detail,
		When:   time.Now(),
	})
}

// errExt is a tiny error wrapper to avoid importing errors just for one call.
func errExt(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

// debounce stores recent event signatures to avoid notification spam.
type debounce struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func newDebounce(ttl time.Duration) *debounce {
	return &debounce{seen: make(map[string]time.Time), ttl: ttl}
}

func (d *debounce) shouldFire(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for k, t := range d.seen {
		if now.Sub(t) > d.ttl {
			delete(d.seen, k)
		}
	}
	if t, ok := d.seen[key]; ok && now.Sub(t) < d.ttl {
		return false
	}
	d.seen[key] = now
	return true
}

// Reuse a process-wide debouncer for DDNS errors to keep webhook quiet during outages.
var ddnsErrDebounce = newDebounce(15 * time.Minute)

// SendDDNSErrorOnce sends an EventDDNSError but only once per 15min per error message.
func (n *Notifier) SendDDNSErrorOnce(detail string) {
	if !ddnsErrDebounce.shouldFire(detail) {
		return
	}
	n.Send(Event{Type: EventDDNSError, Title: "DDNS sync failed", Detail: detail})
}

// banAggregateWindow is how long we batch SOCKS5 ban events before sending one
// summary message. Tuned so that brute-force scanners don't spam the webhook
// while the operator still sees activity within the same hour.
const banAggregateWindow = 15 * time.Minute

// banAggregator buffers ban IPs and flushes a single summary notification per
// window. The window starts on the first ban and resets after flush.
type banAggregator struct {
	mu      sync.Mutex
	pending map[string]int // ip → ban count within current window
	timer   *time.Timer
	window  time.Duration
	n       *Notifier
}

func newBanAggregator(n *Notifier, window time.Duration) *banAggregator {
	return &banAggregator{
		pending: make(map[string]int),
		window:  window,
		n:       n,
	}
}

// OnSocks5Ban records a newly-banned IP. Triggers an aggregated notification
// after banAggregateWindow if no timer is currently armed.
func (n *Notifier) OnSocks5Ban(ip string) {
	if n == nil || n.banAgg == nil {
		return
	}
	n.banAgg.add(ip)
}

func (a *banAggregator) add(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending[ip]++
	if a.timer == nil {
		a.timer = time.AfterFunc(a.window, a.flush)
	}
}

func (a *banAggregator) flush() {
	a.mu.Lock()
	ips := a.pending
	a.pending = make(map[string]int)
	a.timer = nil
	a.mu.Unlock()

	if len(ips) == 0 {
		return
	}
	a.n.Send(Event{
		Type:   EventSocks5Ban,
		Title:  "SOCKS5 brute-force ban",
		Detail: formatBanSummary(ips, a.window),
	})
}

func formatBanSummary(ips map[string]int, window time.Duration) string {
	// Sort IPs by descending ban count, then by IP for stability.
	type kv struct {
		ip string
		n  int
	}
	list := make([]kv, 0, len(ips))
	for ip, n := range ips {
		list = append(list, kv{ip, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].ip < list[j].ip
	})

	var b strings.Builder
	fmt.Fprintf(&b, "过去 %s 内新增 %d 个 IP 被封禁:\n", window, len(list))
	const maxShown = 20
	for i, kv := range list {
		if i >= maxShown {
			fmt.Fprintf(&b, "... 以及另 %d 个", len(list)-maxShown)
			break
		}
		if kv.n > 1 {
			fmt.Fprintf(&b, "  - %s  (×%d)\n", kv.ip, kv.n)
		} else {
			fmt.Fprintf(&b, "  - %s\n", kv.ip)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
