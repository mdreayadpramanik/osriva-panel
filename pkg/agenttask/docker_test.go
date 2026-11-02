package agenttask

import (
	"encoding/json"
	"testing"
)

func TestValidateDocker(t *testing.T) {
	ok := []struct{ typ, params string }{
		{DockerInfo, `{}`},
		{DockerContainers, `{}`},
		{DockerContainerAction, `{"id":"web-1","action":"restart"}`},
		{DockerContainerLogs, `{"id":"3f2a9c","lines":100}`},
		{DockerImagePull, `{"image":"ghcr.io/org/app:1.2@sha256:abc"}`},
		{DockerContainerRun, `{"image":"nginx:1.27","name":"web","env":{"A":"1"},"ports":[{"host_port":8080,"container_port":80}],"volumes":[{"volume":"web-data","target":"/usr/share/nginx/html"}],"restart":"unless-stopped"}`},
	}
	for _, c := range ok {
		if err := Validate(c.typ, json.RawMessage(c.params)); err != nil {
			t.Errorf("%s %s: %v", c.typ, c.params, err)
		}
	}
	bad := []struct{ typ, params string }{
		{DockerContainerAction, `{"id":"web","action":"kill"}`},
		{DockerContainerAction, `{"id":"../x","action":"stop"}`},
		{DockerImagePull, `{"image":"a/../b"}`},
		{DockerImagePull, `{"image":"x; rm -rf /"}`},
		// Privilege escalation options do not exist in the schema.
		{DockerContainerRun, `{"image":"alpine","privileged":true}`},
		{DockerContainerRun, `{"image":"alpine","network_mode":"host"}`},
		{DockerContainerRun, `{"image":"alpine","cap_add":["SYS_ADMIN"]}`},
		// Host paths are bind mounts and are refused.
		{DockerContainerRun, `{"image":"alpine","volumes":[{"volume":"/","target":"/host"}]}`},
		{DockerContainerRun, `{"image":"alpine","volumes":[{"volume":"/var/run/docker.sock","target":"/s"}]}`},
		{DockerContainerRun, `{"image":"alpine","volumes":[{"volume":"data","target":"relative"}]}`},
		{DockerContainerRun, `{"image":"alpine","volumes":[{"volume":"data","target":"/"}]}`},
		{DockerContainerRun, `{"image":"alpine","ports":[{"host_port":0,"container_port":80}]}`},
		{DockerContainerRun, `{"image":"alpine","ports":[{"host_port":80,"container_port":80},{"host_port":80,"container_port":81}]}`},
		{DockerContainerRun, `{"image":"alpine","env":{"BAD-KEY":"x"}}`},
		{DockerContainerRun, `{"image":"alpine","restart":"sometimes"}`},
		{DockerInfo, `{"x":1}`},
	}
	for _, c := range bad {
		if err := Validate(c.typ, json.RawMessage(c.params)); err == nil {
			t.Errorf("%s %s: accepted", c.typ, c.params)
		}
	}
}

func TestDockerClasses(t *testing.T) {
	if Class(DockerContainers) != ClassRead || Class(DockerContainerLogs) != ClassRead {
		t.Fatal("listing must be read class")
	}
	if Class(DockerContainerRun) != ClassOperate || Class(DockerImagePull) != ClassOperate {
		t.Fatal("changes must be operate class")
	}
	if !ReadOnly(DockerInfo) || ReadOnly(DockerContainerAction) {
		t.Fatal("ReadOnly wrong")
	}
	reauth := map[string]bool{
		`{"id":"a","action":"start"}`:  false,
		`{"id":"a","action":"stop"}`:   true,
		`{"id":"a","action":"remove"}`: true,
	}
	for p, want := range reauth {
		if NeedsReauth(DockerContainerAction, json.RawMessage(p)) != want {
			t.Errorf("reauth %s", p)
		}
	}
	if !NeedsReauth(DockerContainerRun, json.RawMessage(`{}`)) || NeedsReauth(DockerImagePull, json.RawMessage(`{}`)) {
		t.Error("reauth for run/pull")
	}
}
