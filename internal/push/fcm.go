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

// TokenSource yields the OAuth2 access token the FCM HTTP v1 API authenticates with. It is an
// interface so the wire-shape tests can inject a static token against a mock FCM, while a real
// deployment mints a real one from a service account (ServiceAccountTokenSource, serviceaccount.go).
// The FCM sender does not know or care which it holds.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticTokenSource is a TokenSource that returns a fixed string. It is what the sender tests use, and
// it is also a legitimate production choice for a deployment that mints the access token out of band
// and injects it — the sender's contract is "give me a bearer token", not "hold a service account".
type StaticTokenSource string

// Token returns the fixed token.
func (s StaticTokenSource) Token(context.Context) (string, error) { return string(s), nil }

// fcmDefaultBaseURL is the FCM HTTP v1 host. It is a field on the sender (not a constant baked into
// the request) so the tests can point it at an httptest server standing in for FCM.
const fcmDefaultBaseURL = "https://fcm.googleapis.com"

// fcmMessagingScope is the OAuth2 scope an FCM v1 access token must carry. It lives here beside the
// sender that requires it so the service-account token source and the sender agree on one value.
const fcmMessagingScope = "https://www.googleapis.com/auth/firebase.messaging"

// FCMSender delivers a Notification as a high-priority FCM HTTP v1 message (roadmap §5.3, [fcm
// priority] [fcm set-message-type]). It is the default push backend.
type FCMSender struct {
	projectID string
	tokens    TokenSource
	client    *http.Client
	baseURL   string
}

// NewFCMSender builds an FCM sender for one Firebase project. projectID is the Firebase project id the
// v1 endpoint is scoped to; tokens supplies the bearer credential. A nil client gets a default with a
// finite timeout — an FCM call must not hang the delivery worker indefinitely.
func NewFCMSender(projectID string, tokens TokenSource, client *http.Client) *FCMSender {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &FCMSender{
		projectID: projectID,
		tokens:    tokens,
		client:    client,
		baseURL:   fcmDefaultBaseURL,
	}
}

// fcmMessage is the FCM HTTP v1 request body: a single top-level "message" object. The shape follows
// the v1 send-request reference — token-addressed, with notification/data at the message level and an
// AndroidConfig carrying the delivery controls that matter for a wake-the-app alert.
type fcmMessage struct {
	Message fcmMessageBody `json:"message"`
}

type fcmMessageBody struct {
	Token        string            `json:"token"`
	Notification fcmNotification   `json:"notification"`
	Data         map[string]string `json:"data,omitempty"`
	Android      fcmAndroidConfig  `json:"android"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// fcmAndroidConfig carries the two delivery controls S6 turns on:
//
//   - Priority "high" — only a high-priority message wakes a closed app on an idle (Doze) device,
//     which is the entire point of a geofence alert ([fcm priority]).
//   - CollapseKey — FCM delivers only the latest undelivered message per key, giving the "latest
//     state" collapsing S6 designs for ([fcm understand-delivery]).
type fcmAndroidConfig struct {
	Priority    string `json:"priority"`
	CollapseKey string `json:"collapse_key,omitempty"`
}

// Send POSTs the notification to FCM's v1 send endpoint as a high-priority, collapsible message. A
// non-2xx response is an error (carrying FCM's body, truncated) so the dispatcher can retry-then-log;
// a transport error is likewise returned, never panicked.
func (s *FCMSender) Send(ctx context.Context, d Delivery) error {
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("fcm: obtain access token: %w", err)
	}

	msg := fcmMessage{Message: fcmMessageBody{
		Token:        d.Sub.Token,
		Notification: fcmNotification{Title: d.Note.Title, Body: d.Note.Body},
		Data:         d.Note.Data,
		Android: fcmAndroidConfig{
			Priority:    "high",
			CollapseKey: d.Note.CollapseKey,
		},
	}}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("fcm: marshal message: %w", err)
	}

	url := fmt.Sprintf("%s/v1/projects/%s/messages:send", s.baseURL, s.projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("fcm: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("fcm: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read a bounded slice of the error body: FCM names the fault (UNREGISTERED, invalid token,
		// quota) in it, which is what makes a delivery-failure log actionable — but an unbounded read of
		// an error response is its own footgun.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("fcm: send returned %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}
