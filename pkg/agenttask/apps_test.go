package agenttask

import (
	"encoding/json"
	"testing"
	"time"
)

func TestValidateApps(t *testing.T) {
	src := `"source":{"url":"https://github.com/o/r.git","ref":"release/v1.2"},"build":{"mode":"node","context":"apps/web"}`
	run := `"run":{"container_port":3000,"host_port":8080,"env":{"A":"1"}}`
	ok := []string{
		`{"app":"web","deployment":"0b1c-2d",` + src + `,` + run + `}`,
		`{"app":"web","deployment":"d2","image":"osriva-app/web:0b1c-2d",` + run + `}`,
		`{"app":"web","deployment":"d3","source":{"url":"http://10.0.0.5/r.git","ref":"main"},"build":{"mode":"dockerfile","dockerfile":"docker/Dockerfile.prod"},` + run + `}`,
	}
	for _, p := range ok {
		if err := Validate(AppDeploy, json.RawMessage(p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	bad := []string{
		`{"app":"Web","deployment":"d",` + src + `,` + run + `}`,
		`{"app":"web","deployment":"d",` + run + `}`,
		`{"app":"web","deployment":"d","image":"osriva-app/other:d1",` + run + `}`,
		`{"app":"web","deployment":"d","image":"nginx:latest",` + run + `}`,
		`{"app":"web","deployment":"d","image":"osriva-app/web:d1",` + src + `,` + run + `}`,
		`{"app":"web","deployment":"d","source":{"url":"https://u:p@h/r.git","ref":"main"},"build":{"mode":"static"},` + run + `}`,
		`{"app":"web","deployment":"d","source":{"url":"file:///etc","ref":"main"},"build":{"mode":"static"},` + run + `}`,
		`{"app":"web","deployment":"d","source":{"url":"git@github.com:o/r.git","ref":"main"},"build":{"mode":"static"},` + run + `}`,
		`{"app":"web","deployment":"d","source":{"url":"https://h/r.git","ref":"-u"},"build":{"mode":"static"},` + run + `}`,
		`{"app":"web","deployment":"d","source":{"url":"https://h/r.git","ref":"a/../b"},"build":{"mode":"static"},` + run + `}`,
		`{"app":"web","deployment":"d","source":{"url":"https://h/r.git","ref":"main"},"build":{"mode":"static","context":"../x"},` + run + `}`,
		`{"app":"web","deployment":"d","source":{"url":"https://h/r.git","ref":"main"},"build":{"mode":"static","context":"/etc"},` + run + `}`,
		`{"app":"web","deployment":"d","source":{"url":"https://h/r.git","ref":"main"},"build":{"mode":"static","dockerfile":"Dockerfile"},` + run + `}`,
		`{"app":"web","deployment":"d","source":{"url":"https://h/r.git","ref":"main"},"build":{"mode":"compose"},` + run + `}`,
		`{"app":"web","deployment":"d",` + src + `,"run":{"container_port":0,"host_port":8080}}`,
		`{"app":"web","deployment":"d",` + src + `,` + run + `,"privileged":true}`,
	}
	for _, p := range bad {
		if err := Validate(AppDeploy, json.RawMessage(p)); err == nil {
			t.Errorf("accepted: %s", p)
		}
	}
	if Validate(AppRemove, json.RawMessage(`{"app":"web"}`)) != nil || Validate(AppRemove, json.RawMessage(`{"app":"../x"}`)) == nil {
		t.Error("app.remove validation")
	}
	if Class(AppDeploy) != ClassOperate || !NeedsReauth(AppDeploy, nil) || !NeedsReauth(AppRemove, nil) {
		t.Error("deploy class / reauth")
	}
	if RunTimeout(AppDeploy) != 20*time.Minute || RunTimeout(ServicesList) != 5*time.Minute || !LongRunning(AppDeploy) {
		t.Error("timeouts")
	}
}
