package agenttask

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateBackup(t *testing.T) {
	rcp := "age1" + strings.Repeat("q", 58)
	key := "AGE-SECRET-KEY-1" + strings.Repeat("Q", 58)
	sha := strings.Repeat("a", 64)
	ok := map[string]string{
		BackupCreate:  `{"id":"b-1","kind":"volume","target":"my_data.v1","recipient":"` + rcp + `"}`,
		BackupVerify:  `{"id":"b-1","sha256":"` + sha + `","backup_key":"` + key + `"}`,
		BackupRestore: `{"id":"b-1","kind":"wordpress","target":"blog","sha256":"` + sha + `","backup_key":"` + key + `"}`,
		BackupDelete:  `{"id":"b-1"}`,
		BackupList:    `{}`,
	}
	for typ, p := range ok {
		if err := Validate(typ, json.RawMessage(p)); err != nil {
			t.Errorf("%s: %v", typ, err)
		}
	}
	for typ, p := range map[string]string{
		BackupCreate:  `{"id":"../x","kind":"volume","target":"d","recipient":"` + rcp + `"}`,
		BackupVerify:  `{"id":"b","sha256":"xyz","backup_key":"` + key + `"}`,
		BackupRestore: `{"id":"b","kind":"volume","target":"/etc","sha256":"` + sha + `","backup_key":"` + key + `"}`,
		BackupDelete:  `{"id":"b","path":"/"}`,
	} {
		if Validate(typ, json.RawMessage(p)) == nil {
			t.Errorf("accepted %s %s", typ, p)
		}
	}
	if Validate(BackupCreate, json.RawMessage(`{"id":"b","kind":"volume","target":"d","recipient":"`+key+`"}`)) == nil {
		t.Error("a secret key was accepted as recipient")
	}
	if Class(BackupList) != ClassRead || Class(BackupCreate) != ClassOperate || !NeedsReauth(BackupRestore, nil) || NeedsReauth(BackupCreate, nil) {
		t.Error("classes")
	}
}
