# Writing OSRIVA Panel plugins

OSRIVA Panel runs third-party code only out of process. A plugin is an executable,
signed by its publisher, that the panel starts once per call. The design is in
[ADR-0016](adr/0016-plugins.md).

The SDK (`pkg/pluginsdk`), the signing tool (`pkg/pluginsdk/cmd/osriva-plugin`) and the
example plugin are licensed **Apache-2.0**. Plugins may use any license.

Status: **one extension point**, notification channels (`notifier`). DNS providers,
backup storage and hosting backends will use the same protocol when they arrive.

## 1. The protocol (version 1)

For every call the panel:

1. starts the executable in the plugin's directory, with a minimal environment
   (`PATH`, `OSRIVA_PLUGIN_PROTOCOL=1`, and `SystemRoot` on Windows);
2. writes one JSON request to stdin and closes it;
3. reads one JSON response from stdout (at most 1 MiB) and waits for the process to exit
   (at most 20 seconds, then the process, or on Linux its whole process group, is
   killed).

Anything written to stderr (first 8 KiB) goes to the panel log.

```json
{"protocol": 1, "method": "notify", "params": {
  "event": {"event": "alert.firing", "id": "…", "rule": "cpu high", "server": "web-1",
            "metric": "cpu", "value": 93.5, "threshold": 90,
            "message": "[OSRIVA] FIRING: cpu high on web-1 (cpu 93.5% > 90%)", "at": "2026-11-04T10:00:00Z"},
  "settings": {"…": "whatever the operator entered"},
  "secret": "optional channel secret"
}}
```

Answer with success:

```json
{"ok": true, "result": {}}
```

Or with a failure. `permanent: true` means the panel must not retry, for example a bad
configuration or an HTTP 4xx answer. Other failures are retried with back-off.

```json
{"ok": false, "error": {"code": "…", "message": "…", "permanent": true}}
```

Methods:

| Method | Params | Result |
|--------|--------|--------|
| `describe` | none | `{name, version, kinds}` |
| `notify` | `{event, settings, secret}` | `{}` |

Secrets arrive only in the request on stdin. Never log them or write them to disk.

## 2. A plugin in Go

```go
package main

import (
	"context"

	"github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk"
)

func main() {
	pluginsdk.Serve(pluginsdk.Handlers{
		Describe: func() pluginsdk.Description {
			return pluginsdk.Description{Name: "my-notifier", Version: "1.0.0", Kinds: []string{pluginsdk.KindNotifier}}
		},
		Notify: func(ctx context.Context, n pluginsdk.Notification) error {
			// deliver n.Event.Message using n.Settings / n.Secret;
			// return pluginsdk.Permanent(err) for errors a retry cannot fix
			return nil
		},
	})
}
```

The complete example `pkg/pluginsdk/examples/chat-notifier` posts `{"text": …}` to an
incoming-webhook URL, the format Slack, Mattermost and Rocket.Chat accept. Only the
format is implemented; delivery to those services themselves has not been tested.

## 3. The manifest and signing

Every plugin directory contains `plugin.json`; its name must equal the directory name:

```json
{
  "name": "chat-notifier",
  "version": "0.1.0",
  "publisher": "Example Ltd.",
  "description": "Posts alerts to an incoming-webhook URL",
  "protocol": 1,
  "kinds": ["notifier"],
  "executables": {
    "linux/amd64":   {"path": "chat-notifier",     "sha256": "0000000000000000000000000000000000000000000000000000000000000000"},
    "windows/amd64": {"path": "chat-notifier.exe", "sha256": "0000000000000000000000000000000000000000000000000000000000000000"}
  }
}
```

Sign it with the SDK tool. `sign` computes the real hashes:

```sh
go build -o osriva-plugin ./pkg/pluginsdk/cmd/osriva-plugin
./osriva-plugin keygen --out publisher.key    # prints the public key; keep the file secret
GOOS=linux GOARCH=amd64 go build -o chat-notifier/chat-notifier ./pkg/pluginsdk/examples/chat-notifier
./osriva-plugin sign --key publisher.key chat-notifier
./osriva-plugin verify --trusted <public key> chat-notifier
```

The signature covers the whole manifest, including every executable's SHA-256. Changing
a binary or any field breaks it.

## 4. Installing a plugin (operators)

1. Copy the plugin directory into `OSRIVA_PLUGIN_DIR`, which only the operator may write.
2. Set `OSRIVA_PLUGIN_TRUSTED_KEYS` to the publisher's public key; several keys are
   separated by commas. Restart the API, or use **Settings → Plugins → Rescan
   directory**.
3. In **Settings → Plugins** the plugin shows as valid, with its key id. If it does not,
   the reason is shown: untrusted key, bad signature, binary mismatch or wrong platform.
4. Click **Approve**. This needs re-authentication and pins the binary's SHA-256.
5. Create a notification channel of type **Plugin** under **Alerts**.

If the binary changes later, even by one byte, the plugin stops running until it is
approved again.

## 5. Security model and limits

* **Contained:**
  * the panel's environment (database URL, `OSRIVA_SECRET_KEY`) never reaches a plugin;
  * every call is a fresh process;
  * calls time out and their output is bounded;
  * unsigned, untrusted, changed or unapproved plugins never run.

  These are tested in `services/api/internal/plugins`, on Linux in CI and on Windows 11
  locally.
* **Not contained:** there is no OS sandbox (seccomp, namespaces, AppArmor). A plugin can
  use the network and read whatever the API's user can read. Approve only plugins whose
  publisher you trust.
* **Not covered:** the `isolate` process-group kill applies on Linux and other Unix
  systems; on Windows only the plugin process itself is killed on timeout.
