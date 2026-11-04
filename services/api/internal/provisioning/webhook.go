package provisioning

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/jobs"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/monitor"
)

var webhookClient = &http.Client{
	Timeout: 10 * time.Second,
	// A redirect could send the signed payload somewhere else.
	CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not followed") },
}

// postWebhook sends a signed event; HTTP 4xx (except 408/429) is permanent.
func postWebhook(ctx context.Context, url, secret, event, deliveryID string, body []byte) error {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return jobs.Permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OSRIVA-Panel")
	req.Header.Set("X-Osriva-Event", event)
	req.Header.Set("X-Osriva-Delivery", deliveryID)
	req.Header.Set("X-Osriva-Timestamp", ts)
	req.Header.Set("X-Osriva-Signature", monitor.Sign(secret, ts, body))
	resp, err := webhookClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	err = fmt.Errorf("webhook answered HTTP %d", resp.StatusCode)
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
		return jobs.Permanent(err)
	}
	return err
}
