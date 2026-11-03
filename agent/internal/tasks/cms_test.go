package tasks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

func setupCMS(t *testing.T) *fakeEngine {
	f, _ := setupDeploy(t)
	for _, img := range []string{agenttask.WordPressImage, agenttask.MariaDBImage, agenttask.WPCLIImage} {
		f.images[img] = map[string]string{}
	}
	f.networks, f.volumes = map[string]map[string]string{}, map[string]map[string]string{}
	oldReady, oldPoll := wpReady, pollInterval
	wpReady = func(context.Context, int) error { return nil }
	pollInterval = 10 * time.Millisecond
	t.Cleanup(func() { wpReady, pollInterval = oldReady, oldPoll })
	return f
}

func cmsRun(t *testing.T, typ string, p any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	return Execute(context.Background(), typ, raw)
}

var wpParams = agenttask.WPInstallParams{Name: "blog", HostPort: 18095, URL: "https://blog.example.com", Title: `Ali's "blog" $(id)`,
	AdminUser: "admin", AdminEmail: "admin@example.com", AdminPassword: "correct-horse-battery-staple"}

func TestWordPressInstall(t *testing.T) {
	f := setupCMS(t)
	var installEnv []any
	f.cliOut = func(script string) (string, int) {
		if strings.Contains(script, "wp core install") {
			for _, c := range f.cts {
				if c.Image == agenttask.WPCLIImage {
					installEnv = c.Body["Env"].([]any)
				}
			}
			return "Success: WordPress installed successfully.\n", 0
		}
		return "", 0
	}
	out, err := cmsRun(t, agenttask.CMSWPInstall, wpParams)
	r := out.(agenttask.WPCommandResult)
	if err != nil || !r.OK || !strings.Contains(r.Output, "installed successfully") {
		t.Fatalf("install: %+v %v", r, err)
	}
	if strings.Contains(r.Output, wpParams.AdminPassword) {
		t.Fatal("admin password in the output")
	}
	db := f.byRef(agenttask.WPDBContainer("blog"))
	wp := f.byRef(agenttask.WPContainer("blog"))
	if db == nil || wp == nil || f.networks["osriva-wp-blog"] == nil || f.volumes["osriva-wp-blog-db"] == nil || f.volumes["osriva-wp-blog-files"] == nil {
		t.Fatalf("objects: cts=%v nets=%v vols=%v", f.cts, f.networks, f.volumes)
	}
	if hc := db.Body["HostConfig"].(map[string]any); hc["PortBindings"] != nil || hc["NetworkMode"] != "osriva-wp-blog" {
		t.Fatalf("database must not publish ports: %v", hc)
	}
	hc := wp.Body["HostConfig"].(map[string]any)
	if hc["PortBindings"].(map[string]any)["80/tcp"].([]any)[0].(map[string]any)["HostIp"] != "127.0.0.1" || hc["Privileged"] != false {
		t.Fatalf("WordPress host config: %v", hc)
	}
	// the database password is generated here and shared by both containers only
	var dbPass string
	for _, e := range db.Body["Env"].([]any) {
		if s := e.(string); strings.HasPrefix(s, "MARIADB_PASSWORD=") {
			dbPass = strings.TrimPrefix(s, "MARIADB_PASSWORD=")
		}
	}
	if len(dbPass) < 32 || !strings.Contains(strings.Join(anyStrings(wp.Body["Env"]), " "), "WORDPRESS_DB_PASSWORD="+dbPass) {
		t.Fatalf("db password: %q", dbPass)
	}
	// user values reach WP-CLI as environment variables, never inside the script
	if !strings.Contains(strings.Join(anyStrings(installEnv), "\n"), "WP_TITLE="+wpParams.Title) {
		t.Fatalf("install env: %v", installEnv)
	}
	for _, c := range f.cts {
		if c.Image == agenttask.WPCLIImage {
			t.Fatal("WP-CLI containers must be removed")
		}
	}

	if _, err := cmsRun(t, agenttask.CMSWPInstall, wpParams); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second install with the same name: %v", err)
	}
}

func anyStrings(v any) []string {
	out := []string{}
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestWordPressInfoVerifyUpdateRemove(t *testing.T) {
	f := setupCMS(t)
	f.cliOut = func(string) (string, int) { return "", 0 }
	if _, err := cmsRun(t, agenttask.CMSWPInstall, wpParams); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 6000) // longer than a log line limit: must not be truncated
	f.cliOut = func(script string) (string, int) {
		switch {
		case strings.Contains(script, "wp core is-installed"):
			return "<<osriva>>\n6.6.2\n<<osriva>>\n[{\"version\":\"6.7.1\",\"update_type\":\"minor\"}]\n<<osriva>>\n" +
				`[{"name":"akismet","status":"inactive","version":"5.3","update_version":"5.3.3"},{"name":"` + long + `","status":"active","version":"1","update_version":""}]` +
				"\n<<osriva>>\n[{\"name\":\"twentytwentyfour\",\"status\":\"active\",\"version\":\"1.2\",\"update_version\":\"\"}]\n<<osriva>>\nhttps://blog.example.com\n", 0
		case strings.Contains(script, "verify-checksums"):
			return "Warning: File doesn't verify against checksum: wp-includes/version.php\nError: WordPress installation doesn't verify against checksums.\n", 1
		case strings.Contains(script, "plugin update"):
			return "Error: network down\n", 1
		}
		return "", 0
	}
	out, err := cmsRun(t, agenttask.CMSWPInfo, agenttask.CMSNameParams{Name: "blog"})
	info := out.(agenttask.WPInfoResult)
	if err != nil || !info.Installed || info.CoreVersion != "6.6.2" || info.CoreUpdate != "6.7.1" || len(info.Plugins) != 2 ||
		info.Plugins[0].UpdateAvailable != "5.3.3" || info.Plugins[1].Name != long || info.SiteURL != "https://blog.example.com" || !info.Running {
		t.Fatalf("info: %+v %v", info, err)
	}
	out, err = cmsRun(t, agenttask.CMSWPVerify, agenttask.CMSNameParams{Name: "blog"})
	if v := out.(agenttask.WPCommandResult); err != nil || v.OK || !strings.Contains(v.Output, "doesn't verify") {
		t.Fatalf("a failed verification is a result, not an error: %+v %v", v, err)
	}
	if _, err := cmsRun(t, agenttask.CMSWPUpdate, agenttask.WPUpdateParams{Name: "blog", Target: "plugins"}); err == nil {
		t.Fatal("failed update must fail the task")
	}

	// keep data: containers and network go, volumes stay
	out, err = cmsRun(t, agenttask.CMSRemove, agenttask.CMSRemoveParams{Name: "blog"})
	if r := out.(agenttask.CMSRemoveResult); err != nil || len(r.Removed) != 3 || r.DataDeleted || len(f.volumes) != 2 || len(f.cts) != 0 {
		t.Fatalf("remove keeping data: %+v %v vols=%v", out, err, f.volumes)
	}
	out, err = cmsRun(t, agenttask.CMSRemove, agenttask.CMSRemoveParams{Name: "blog", DeleteData: true})
	if r := out.(agenttask.CMSRemoveResult); err != nil || !r.DataDeleted || len(f.volumes) != 0 {
		t.Fatalf("remove with data: %+v %v", out, err)
	}
}

func TestCMSRemoveRefusesForeignObjects(t *testing.T) {
	f := setupCMS(t)
	f.volumes["osriva-wp-shop-db"] = map[string]string{} // created by someone else
	if _, err := cmsRun(t, agenttask.CMSRemove, agenttask.CMSRemoveParams{Name: "shop", DeleteData: true}); err == nil || !strings.Contains(err.Error(), "nothing was removed") {
		t.Fatalf("foreign volume: %v", err)
	}
	if f.volumes["osriva-wp-shop-db"] == nil {
		t.Fatal("foreign volume deleted")
	}
	if _, err := cmsRun(t, agenttask.CMSWPInstall, agenttask.WPInstallParams{Name: "shop", HostPort: 18096, URL: "http://127.0.0.1:18096",
		Title: "Shop", AdminUser: "a", AdminEmail: "a@example.com", AdminPassword: "0123456789abcdef"}); err == nil {
		t.Fatal("install over an existing volume")
	}
}
