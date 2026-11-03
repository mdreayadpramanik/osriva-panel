package agenttask

import "testing"

func TestBulk(t *testing.T) {
	for _, typ := range BulkTypes() {
		if !Types[typ] {
			t.Errorf("bulk type %q is not an agent task", typ)
		}
		if FileTask(typ) {
			t.Errorf("file task %q must not run in a batch", typ)
		}
	}
	for _, typ := range []string{FilesRead, FilesWrite, DockerContainerRun, DockerContainerAction, AppDeploy, BackupRestore,
		CMSRemove, ProxyApply, HostnameSet, "shell.exec"} {
		if Bulk(typ) {
			t.Errorf("%q must not run in a batch", typ)
		}
	}
	if !Bulk(ServiceAction) || !Bulk(SSHKeysAdd) || !Bulk(DockerImagePull) {
		t.Error("expected service actions, SSH keys and image pulls in batches")
	}
}
