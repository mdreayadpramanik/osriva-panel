package agenttask

import (
	"encoding/json"
	"testing"
)

func TestValidDomain(t *testing.T) {
	for _, d := range []string{"example.com", "www.example.co.uk", "ci.localhost", "a-b.example.com", "xn--bcher-kva.example"} {
		if !ValidDomain(d) {
			t.Errorf("%s rejected", d)
		}
	}
	for _, d := range []string{"", "localhost", "Example.com", "*.example.com", "10.0.0.1", "example.com.", "-a.example.com",
		"a..com", "a.com{", "a.com respond", "a.com\n}", "exa_mple.com"} {
		if ValidDomain(d) {
			t.Errorf("%q accepted", d)
		}
	}
}

func TestValidateProxyTasks(t *testing.T) {
	ok := `{"http_port":80,"https_port":443,"acme_email":"ops@example.com","sites":[{"domain":"example.com","upstream_port":8080,"tls":"acme"}]}`
	if err := Validate(ProxyApply, json.RawMessage(ok)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"http_port":80,"https_port":80,"sites":[]}`,
		`{"http_port":80,"https_port":443,"acme_email":"a@b.com}\n{","sites":[]}`,
		`{"http_port":80,"https_port":443,"sites":[{"domain":"a.com","upstream_port":1,"tls":"acme"},{"domain":"a.com","upstream_port":2,"tls":"acme"}]}`,
		`{"http_port":80,"https_port":443,"sites":[{"domain":"a.com","upstream_port":80,"tls":"acme"}]}`,
		`{"http_port":80,"https_port":443,"sites":[],"extra":1}`,
	} {
		if Validate(ProxyApply, json.RawMessage(bad)) == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
	if Class(ProxyStatus) != ClassRead || Class(ProxyApply) != ClassOperate || !NeedsReauth(ProxyDisable, nil) || NeedsReauth(ProxyStatus, nil) {
		t.Error("classes")
	}
}
