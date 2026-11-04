// SPDX-License-Identifier: Apache-2.0

// Command chat-notifier is an example OSRIVA Panel notifier plugin. It posts
// {"text": "<message>"} to an incoming-webhook URL, the format accepted by
// Slack, Mattermost and Rocket.Chat incoming webhooks.
//
// Channel settings: {"url": "https://hooks.example.com/..."}. The URL often
// contains a token, so it may instead be given as the channel secret.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk"
)

const version = "0.1.0"

type settings struct {
	URL string `json:"url"`
}

func notify(ctx context.Context, n pluginsdk.Notification) error {
	var s settings
	if len(n.Settings) > 0 {
		if err := json.Unmarshal(n.Settings, &s); err != nil {
			return pluginsdk.Permanent(errors.New("settings must be {\"url\": …}"))
		}
	}
	if n.Secret != "" {
		s.URL = n.Secret
	}
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return pluginsdk.Permanent(errors.New("an http(s) webhook URL is required (settings.url or the channel secret)"))
	}
	body, _ := json.Marshal(map[string]string{"text": n.Event.Message})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return pluginsdk.Permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("redirects are not followed")
	}}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	err = fmt.Errorf("the webhook answered HTTP %d", resp.StatusCode)
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429 {
		return pluginsdk.Permanent(err)
	}
	return err
}

func main() {
	pluginsdk.Serve(pluginsdk.Handlers{
		Describe: func() pluginsdk.Description {
			return pluginsdk.Description{Name: "chat-notifier", Version: version, Kinds: []string{pluginsdk.KindNotifier}}
		},
		Notify: notify,
	})
}
