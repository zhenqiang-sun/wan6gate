package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"wan6gate/internal/config"
	"wan6gate/internal/notify"
)

// handleNotify GET returns the current notify config (WebhookURL in plain text;
// the UI is LAN-only and authenticated). PUT preserves the existing webhook
// when the form leaves it empty so the user doesn't have to re-paste it.
func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, s.cfg.GetNotify())
	case http.MethodPut:
		var body config.NotifyConfig
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request body")
			return
		}
		existing := s.cfg.GetNotify()
		if body.WebhookURL == "" {
			body.WebhookURL = existing.WebhookURL
		}
		if body.Provider == "" {
			body.Provider = "feishu"
		}
		if body.Enabled && body.WebhookURL == "" {
			writeErr(w, http.StatusBadRequest, "webhook_url required when enabled")
			return
		}
		// Basic URL sanity for feishu (only one provider for now).
		if body.Provider == "feishu" && body.WebhookURL != "" {
			if !strings.HasPrefix(body.WebhookURL, "https://open.feishu.cn/") &&
				!strings.HasPrefix(body.WebhookURL, "https://open.larksuite.com/") {
				writeErr(w, http.StatusBadRequest, "feishu webhook_url must start with https://open.feishu.cn/ or https://open.larksuite.com/")
				return
			}
		}
		s.cfg.SetNotify(body)
		if err := s.cfg.Save(s.cfgPath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.log.Info("notify config updated", "enabled", body.Enabled, "provider", body.Provider)
		writeOK(w, s.cfg.GetNotify())
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleNotifyTest sends a synchronous test notification to the *currently saved*
// webhook (so the user can verify connectivity without saving new creds first).
// If the request body has webhook_url, that one is used instead (allows testing
// before save).
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if s.notif == nil {
		writeErr(w, http.StatusInternalServerError, "notifier not available")
		return
	}
	// Optional: accept a one-shot URL override in body for "test before save".
	var body struct {
		WebhookURL string `json:"webhook_url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// Snapshot current cfg.Notify; if URL override given, swap it in temporarily.
	if body.WebhookURL != "" {
		old := s.cfg.GetNotify()
		test := old
		test.WebhookURL = body.WebhookURL
		s.cfg.SetNotify(test)
		defer s.cfg.SetNotify(old) // restore regardless of result
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	err := s.notif.SendSync(ctx, notify.Event{
		Type:   notify.EventTest,
		Title:  "wan6gate test",
		Detail: "If you see this, the webhook is reachable.",
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, "sent")
}
