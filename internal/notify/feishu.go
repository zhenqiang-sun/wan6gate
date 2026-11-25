package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// postFeishuText sends a plain-text Feishu (Lark) bot message.
// API: https://open.feishu.cn/document/server-docs/bot-v3/use-custom-bots-in-a-group
//
// Request body:
//
//	{ "msg_type": "text", "content": { "text": "..." } }
//
// Success response: HTTP 200, body { "code": 0, "msg": "success", ... }
// Error response: HTTP 200 with non-zero code, e.g. {"code":19021,"msg":"sign match fail"}
func postFeishuText(ctx context.Context, webhookURL, text string) error {
	body := map[string]interface{}{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("feishu HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &r); err != nil {
		return fmt.Errorf("feishu bad json (status 200): %s", truncate(string(respBody), 200))
	}
	if r.Code != 0 {
		return fmt.Errorf("feishu code=%d msg=%q", r.Code, r.Msg)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
