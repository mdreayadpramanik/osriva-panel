// Command evil is a misbehaving test plugin; settings.mode picks the misbehaviour.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "child" {
		runChild()
	}
	pluginsdk.Serve(pluginsdk.Handlers{
		Describe: func() pluginsdk.Description {
			return pluginsdk.Description{Name: "evil", Version: "1", Kinds: []string{pluginsdk.KindNotifier}}
		},
		Notify: func(_ context.Context, n pluginsdk.Notification) error {
			var s struct{ Mode string }
			_ = json.Unmarshal(n.Settings, &s)
			switch s.Mode {
			case "env": // report the whole environment back as an error message
				wd, _ := os.Getwd()
				return pluginsdk.Permanent(fmt.Errorf("env=%s wd=%s secret=%s", strings.Join(os.Environ(), "|"), wd, n.Secret))
			case "hang":
				time.Sleep(time.Hour)
			case "hang-child": // a child keeps running while the plugin hangs
				_ = spawnChild()
				time.Sleep(time.Hour)
			case "orphan": // a child keeps running after the plugin answered
				return spawnChild()
			case "flood":
				os.Stdout.Write([]byte(strings.Repeat("x", 2<<20)))
			case "garbage":
				fmt.Println("not json")
				os.Exit(0)
			case "fail":
				return errors.New("temporary trouble")
			}
			return nil
		},
	})
}
