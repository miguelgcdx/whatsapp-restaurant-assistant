package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/foundation"
)

type fakeSender struct {
	endpoint string
	client   *http.Client
}

// NewFakeSender is a local test protocol, not a live provider integration.
func NewFakeSender(endpoint string) (Sender, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil {
		return nil, errors.New("invalid fake provider URL")
	}
	ip := net.ParseIP(u.Hostname())
	if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || ip == nil || !ip.IsLoopback() || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("fake provider must be an explicit numeric-loopback HTTP URL without credentials or fragment")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			target := net.ParseIP(host)
			if err != nil || target == nil || !target.IsLoopback() {
				return nil, errors.New("non-loopback dial denied")
			}
			return dialer.DialContext(ctx, network, address)
		},
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 65536,
		// Avoid leaving idle transport goroutines after a worker's shutdown. The
		// client/transport is still reused, without retaining unused connections.
		DisableKeepAlives: true,
	}
	return &fakeSender{endpoint: endpoint, client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (s *fakeSender) Send(ctx context.Context, d foundation.Delivery) foundation.SendResult {
	unknown := foundation.SendResult{State: foundation.SendUnknown}
	payload, err := json.Marshal(struct {
		EventID  string `json:"event_id"`
		EffectID string `json:"effect_id"`
		Text     string `json:"text"`
	}{d.EventID, d.ID, d.Text})
	if err != nil {
		return unknown
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return unknown
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return unknown
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(body) > 65536 {
		return unknown
	}
	// These explicit local fake-provider statuses guarantee rejection before
	// acceptance. All other non-200 statuses remain ambiguous.
	if resp.StatusCode == 400 || resp.StatusCode == 422 {
		return foundation.SendResult{State: foundation.SendFailed}
	}
	if resp.StatusCode != 200 || !utf8.Valid(body) {
		return unknown
	}
	var accepted struct {
		MessageID string `json:"message_id"`
	}
	if err = json.Unmarshal(body, &accepted); err != nil || strings.TrimSpace(accepted.MessageID) == "" || utf8.RuneCountInString(accepted.MessageID) > 128 {
		return unknown
	}
	return foundation.SendResult{State: foundation.SendAccepted, ProviderID: accepted.MessageID}
}
