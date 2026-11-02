package agenttask

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidate(t *testing.T) {
	ok := []struct{ typ, params string }{
		{ServicesList, ``},
		{ServicesList, `{}`},
		{ProcessesList, `{}`},
		{ServiceAction, `{"name":"nginx.service","action":"restart"}`},
		{ServiceAction, `{"name":"getty@tty1.service","action":"stop"}`},
		{ServiceAction, `{"name":"W32Time","action":"start"}`},
	}
	for _, c := range ok {
		if err := Validate(c.typ, json.RawMessage(c.params)); err != nil {
			t.Errorf("%s %s: %v", c.typ, c.params, err)
		}
	}
	bad := []struct{ typ, params string }{
		{"shell.exec", `{"cmd":"id"}`},
		{"", `{}`},
		{ServicesList, `{"unexpected":1}`},
		{ServiceAction, `{"name":"nginx; rm -rf /","action":"restart"}`},
		{ServiceAction, `{"name":"../../etc/passwd","action":"start"}`},
		{ServiceAction, `{"name":"-x","action":"start"}`},
		{ServiceAction, `{"name":"a b","action":"start"}`},
		{ServiceAction, `{"name":"nginx","action":"mask"}`},
		{ServiceAction, `{"name":"nginx","action":"restart","extra":true}`},
		{ServiceAction, `not json`},
	}
	for _, c := range bad {
		if err := Validate(c.typ, json.RawMessage(c.params)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s %s: expected ErrInvalid, got %v", c.typ, c.params, err)
		}
	}
}

func TestClassification(t *testing.T) {
	if !ReadOnly(ServicesList) || ReadOnly(ServiceAction) {
		t.Fatal("ReadOnly")
	}
	if !Disruptive(ActionStop) || !Disruptive(ActionDisable) || Disruptive(ActionRestart) || Disruptive(ActionStart) {
		t.Fatal("Disruptive")
	}
}
