package ui

import (
	"fmt"
	"net/http"
	"time"
)

// handleLogsSSE streams log lines via Server-Sent Events.
func (s *Server) handleLogsSSE(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(ch)

	// Initial backlog
	for _, line := range s.hub.Backlog(100) {
		fmt.Fprintf(w, "data: %s\n\n", sanitizeSSE(line))
	}
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", sanitizeSSE(line))
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// sanitizeSSE replaces newlines (which would break framing) with spaces.
func sanitizeSSE(s string) string {
	out := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		if c == '\n' || c == '\r' {
			out = append(out, ' ')
		} else {
			out = append(out, c)
		}
	}
	return string(out)
}
