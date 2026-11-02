package service

import (
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	u := SystemdUnit("/usr/local/bin/osriva-agent", "/etc/osriva agent")
	for _, want := range []string{
		`ExecStart="/usr/local/bin/osriva-agent" run --config-dir "/etc/osriva agent"`,
		"Restart=on-failure", "RestartPreventExitStatus=3", "NoNewPrivileges=yes", "WantedBy=multi-user.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
	// systemd specifiers and variables must be escaped, not expanded.
	if u := SystemdUnit(`/opt/50%$x"y`, "/c"); !strings.Contains(u, `"/opt/50%%$$x\"y"`) {
		t.Errorf("escaping: %s", u)
	}
}

func TestShellScriptsQuoteSafely(t *testing.T) {
	evil := "/tmp/x'; rm -rf / #"
	for name, s := range map[string]string{"openrc": OpenRCScript(evil, "/c"), "freebsd": FreeBSDRCScript(evil, "/c")} {
		if strings.Contains(s, "x'; rm") {
			t.Errorf("%s: unescaped quote:\n%s", name, s)
		}
		if !strings.Contains(s, `'/tmp/x'\''; rm -rf / #'`) {
			t.Errorf("%s: expected shell-quoted path:\n%s", name, s)
		}
	}
	if !strings.HasPrefix(OpenRCScript("/b", "/c"), "#!/sbin/openrc-run\n") {
		t.Error("openrc shebang")
	}
	if !strings.Contains(FreeBSDRCScript("/b", "/c"), `rcvar="osriva_agent_enable"`) {
		t.Error("freebsd rcvar")
	}
}

func TestWindowsArgs(t *testing.T) {
	if got := strings.Join(WindowsArgs(`C:\ProgramData\OSRIVA\agent`), " "); got != `run --config-dir C:\ProgramData\OSRIVA\agent` {
		t.Fatal(got)
	}
}
