package agenttask

import (
	"encoding/json"
	"testing"
)

func TestValidateCMS(t *testing.T) {
	ok := `{"name":"blog","host_port":18095,"url":"https://blog.example.com","title":"Ali's \"blog\" $(id)","admin_user":"admin","admin_email":"a@example.com","admin_password":"0123456789abcdef"}`
	if err := Validate(CMSWPInstall, json.RawMessage(ok)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"name":"Blog","host_port":1,"url":"https://b.com","title":"t","admin_user":"a","admin_email":"a@b.com","admin_password":"0123456789abcdef"}`,
		`{"name":"b","host_port":1,"url":"https://b.com/$(id)","title":"t","admin_user":"a","admin_email":"a@b.com","admin_password":"0123456789abcdef"}`,
		`{"name":"b","host_port":1,"url":"ftp://b.com","title":"t","admin_user":"a","admin_email":"a@b.com","admin_password":"0123456789abcdef"}`,
		`{"name":"b","host_port":1,"url":"https://b.com","title":"t\nx","admin_user":"a","admin_email":"a@b.com","admin_password":"0123456789abcdef"}`,
		`{"name":"b","host_port":1,"url":"https://b.com","title":"t","admin_user":"a b","admin_email":"a@b.com","admin_password":"0123456789abcdef"}`,
		`{"name":"b","host_port":1,"url":"https://b.com","title":"t","admin_user":"a","admin_email":"a@b.com","admin_password":"short"}`,
		`{"name":"b","host_port":1,"url":"https://b.com","title":"t","admin_user":"a","admin_email":"a@b.com","admin_password":"0123456789abcdef","image":"x"}`,
	} {
		if Validate(CMSWPInstall, json.RawMessage(bad)) == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
	if Validate(CMSWPUpdate, json.RawMessage(`{"name":"blog","target":"everything"}`)) == nil {
		t.Error("bad update target")
	}
	if Class(CMSWPInfo) != ClassRead || Class(CMSWPInstall) != ClassOperate || !NeedsReauth(CMSRemove, nil) || NeedsReauth(CMSWPVerify, nil) {
		t.Error("classes")
	}
}
