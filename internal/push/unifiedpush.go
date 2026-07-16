package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// UnifiedPushSender is the degoogled push backend (roadmap §1, [unifiedpush]). Where FCM is
// token-addressed through Google's servers, UnifiedPush is endpoint-addressed: the subscription's
// "token" IS the distributor-issued endpoint URL, and delivering a push is a plain HTTPS POST of the
// message to that URL. The distributor (ntfy or another) forwards the bytes to the app.
//
// The notification content is carried as the SAME logical fields FCM sends — title, body, priority,
// collapse key, data — so a family gets an identical alert whichever backend their deployment chose.
// That parity is the point of S6 supporting both, and it is what the parity test pins.
type UnifiedPushSender struct {
	client *http.Client
}

// NewUnifiedPushSender builds a UnifiedPush sender. A nil client gets a default with a finite timeout,
// for the same reason the FCM sender does — a stuck distributor must not hang the delivery worker.
func NewUnifiedPushSender(client *http.Client) *UnifiedPushSender {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &UnifiedPushSender{client: client}
}

// unifiedPushMessage is the JSON body POSTed to the endpoint. It mirrors the fields FCM carries so the
// receiving app sees the same alert either way: a title and body to show, a "high" priority to wake on
// (Priority is also set as a header for distributors like ntfy that read it there), a collapse key for
// latest-state replacement, and the structured data map — which, as everywhere in this package, holds
// only the family's own labels, never a coordinate.
type unifiedPushMessage struct {
	Title       string            `json:"title"`
	Body        string            `json:"body"`
	Priority    string            `json:"priority"`
	CollapseKey string            `json:"collapse_key,omitempty"`
	Data        map[string]string `json:"data,omitempty"`
}

// Send POSTs the notification to the subscription's endpoint URL as a high-priority message. A non-2xx
// response is an error the dispatcher retries-then-logs; a transport error is likewise returned, never
// panicked — the same best-effort contract as the FCM sender.
func (s *UnifiedPushSender) Send(ctx context.Context, d Delivery) error {
	msg := unifiedPushMessage{
		Title:       d.Note.Title,
		Body:        d.Note.Body,
		Priority:    "high",
		CollapseKey: d.Note.CollapseKey,
		Data:        d.Note.Data,
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("unifiedpush: marshal message: %w", err)
	}

	// The endpoint URL is the subscription's token (see the type doc).
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Sub.Token, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("unifiedpush: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Redundant with the body field, but distributors such as ntfy read urgency from a header; setting
	// both is what makes "high priority" land regardless of which the distributor honours.
	req.Header.Set("Urgency", "high")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("unifiedpush: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("unifiedpush: send returned %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}
