package tasks

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// WordPress (docs/adr/0009-cms-wordpress.md): official WordPress and MariaDB
// images on a private network per instance, managed with WP-CLI in
// short-lived containers. Database passwords are generated here and never
// leave the server.

var (
	// wpReady waits until WordPress answers HTTP on the host port; replaced in tests.
	wpReady = func(ctx context.Context, port int) error {
		deadline := time.Now().Add(3 * time.Minute)
		for {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/wp-login.php", port), nil)
			resp, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode < 500 {
					return nil
				}
			}
			if time.Now().After(deadline) {
				return errors.New("WordPress did not answer on its port in time")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	dbHealthyTimeout = 3 * time.Minute
	pollInterval     = 2 * time.Second
)

// wpConfigExtra lets WordPress see HTTPS behind the OSRIVA proxy.
const wpConfigExtra = `if (isset($_SERVER['HTTP_X_FORWARDED_PROTO']) && $_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https') { $_SERVER['HTTPS'] = 'on'; }`

func randomSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cmsLabels(name string) map[string]string {
	return map[string]string{agenttask.CMSLabel: name, "dev.osriva.created-by": "osriva-agent"}
}

// objectState reports whether a container, network or volume exists and was created for this instance.
func (c *dockerClient) objectState(ctx context.Context, kind, name, instance string) (exists, ours bool, id string, err error) {
	var obj struct {
		ID, Id string
		Labels map[string]string
		Config struct{ Labels map[string]string }
	}
	path := map[string]string{"container": "/containers/" + url.PathEscape(name) + "/json", "network": "/networks/" + url.PathEscape(name),
		"volume": "/volumes/" + url.PathEscape(name)}[kind]
	err = c.json(ctx, http.MethodGet, path, nil, nil, &obj)
	var de *dockerError
	if errors.As(err, &de) && de.Status == http.StatusNotFound {
		return false, false, "", nil
	}
	if err != nil {
		return false, false, "", err
	}
	labels := obj.Labels
	if kind == "container" {
		labels = obj.Config.Labels
	}
	id = obj.Id
	if id == "" {
		id = obj.ID
	}
	return true, labels[agenttask.CMSLabel] == instance, id, nil
}

func (c *dockerClient) ensureImage(ctx context.Context, log *deployLog, image string) error {
	var img struct{ Id string }
	if c.json(ctx, http.MethodGet, "/images/"+image+"/json", nil, nil, &img) == nil {
		return nil
	}
	log.step("pulling %s", image)
	_, err := dockerPull(ctx, c, image)
	return err
}

func wpInstall(ctx context.Context, c *dockerClient, p agenttask.WPInstallParams) (agenttask.WPCommandResult, error) {
	res := agenttask.WPCommandResult{Name: p.Name}
	log := &deployLog{}
	fail := func(err error) (agenttask.WPCommandResult, error) {
		log.step("FAILED: %v", err)
		res.Output = log.String()
		return res, &ResultError{Result: res, Err: err}
	}
	// Never reuse or overwrite anything that already exists under these names.
	for _, o := range [][2]string{{"container", agenttask.WPContainer(p.Name)}, {"container", agenttask.WPDBContainer(p.Name)},
		{"network", agenttask.WPNetwork(p.Name)}, {"volume", agenttask.WPFilesVolume(p.Name)}, {"volume", agenttask.WPDBVolume(p.Name)}} {
		exists, _, _, err := c.objectState(ctx, o[0], o[1], p.Name)
		if err != nil {
			return fail(err)
		}
		if exists {
			return fail(fmt.Errorf("a %s named %s already exists; remove it or choose another name", o[0], o[1]))
		}
	}
	for _, img := range []string{agenttask.MariaDBImage, agenttask.WordPressImage, agenttask.WPCLIImage} {
		if err := c.ensureImage(ctx, log, img); err != nil {
			return fail(err)
		}
	}
	labels := cmsLabels(p.Name)
	network := agenttask.WPNetwork(p.Name)
	log.step("creating network and volumes")
	if err := c.json(ctx, http.MethodPost, "/networks/create", nil, map[string]any{"Name": network, "Driver": "bridge", "Labels": labels}, nil); err != nil {
		return fail(err)
	}
	for _, v := range []string{agenttask.WPFilesVolume(p.Name), agenttask.WPDBVolume(p.Name)} {
		if err := c.json(ctx, http.MethodPost, "/volumes/create", nil, map[string]any{"Name": v, "Labels": labels}, nil); err != nil {
			return fail(err)
		}
	}

	dbPass := randomSecret()
	hardening := func(hc map[string]any) map[string]any {
		hc["NetworkMode"], hc["Privileged"], hc["SecurityOpt"] = network, false, []string{"no-new-privileges"}
		hc["RestartPolicy"] = map[string]string{"Name": "unless-stopped"}
		return hc
	}
	log.step("starting MariaDB")
	db := map[string]any{
		"Image": agenttask.MariaDBImage,
		"Env": []string{"MARIADB_DATABASE=wordpress", "MARIADB_USER=wordpress", "MARIADB_PASSWORD=" + dbPass,
			"MARIADB_RANDOM_ROOT_PASSWORD=1"},
		"Labels": labels,
		"Healthcheck": map[string]any{"Test": []string{"CMD", "healthcheck.sh", "--connect", "--innodb_initialized"},
			"Interval": int64(3 * time.Second), "Timeout": int64(5 * time.Second), "Retries": 60},
		"HostConfig": hardening(map[string]any{
			"Mounts": []map[string]string{{"Type": "volume", "Source": agenttask.WPDBVolume(p.Name), "Target": "/var/lib/mysql"}},
		}),
	}
	dbID, err := c.createStart(ctx, agenttask.WPDBContainer(p.Name), db)
	if err != nil {
		return fail(err)
	}
	if err := c.waitHealthy(ctx, dbID); err != nil {
		return fail(err)
	}

	log.step("starting WordPress on 127.0.0.1:%d", p.HostPort)
	wp := map[string]any{
		"Image": agenttask.WordPressImage,
		"Env": []string{"WORDPRESS_DB_HOST=" + agenttask.WPDBContainer(p.Name), "WORDPRESS_DB_USER=wordpress",
			"WORDPRESS_DB_PASSWORD=" + dbPass, "WORDPRESS_DB_NAME=wordpress", "WORDPRESS_CONFIG_EXTRA=" + wpConfigExtra},
		"ExposedPorts": map[string]struct{}{"80/tcp": {}},
		"Labels":       labels,
		"HostConfig": hardening(map[string]any{
			"PortBindings": map[string][]map[string]string{"80/tcp": {{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(p.HostPort)}}},
			"Mounts":       []map[string]string{{"Type": "volume", "Source": agenttask.WPFilesVolume(p.Name), "Target": "/var/www/html"}},
		}),
	}
	if _, err := c.createStart(ctx, agenttask.WPContainer(p.Name), wp); err != nil {
		return fail(err)
	}
	if err := wpReady(ctx, p.HostPort); err != nil {
		return fail(err)
	}

	log.step("installing WordPress with WP-CLI")
	// Values reach WP-CLI through environment variables; the shell never parses them.
	code, out, err := wpCLI(ctx, c, p.Name, []string{"WP_URL=" + p.URL, "WP_TITLE=" + p.Title, "WP_USER=" + p.AdminUser,
		"WP_EMAIL=" + p.AdminEmail, "WP_PASS=" + p.AdminPassword},
		`wp core install --url="$WP_URL" --title="$WP_TITLE" --admin_user="$WP_USER" --admin_email="$WP_EMAIL" --admin_password="$WP_PASS" --skip-email`)
	log.write(out)
	if err != nil {
		return fail(err)
	}
	if code != 0 {
		return fail(fmt.Errorf("wp core install exited with code %d", code))
	}
	log.step("done: %s", p.URL)
	res.OK, res.Output = true, log.String()
	return res, nil
}

func (c *dockerClient) createStart(ctx context.Context, name string, body map[string]any) (string, error) {
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	var created struct{ Id string }
	if err := c.json(ctx, http.MethodPost, "/containers/create", q, body, &created); err != nil {
		return "", err
	}
	if err := c.json(ctx, http.MethodPost, "/containers/"+created.Id+"/start", nil, nil, nil); err != nil {
		return created.Id, err
	}
	return created.Id, nil
}

func (c *dockerClient) waitHealthy(ctx context.Context, id string) error {
	deadline := time.Now().Add(dbHealthyTimeout)
	for {
		in, err := c.inspect(ctx, id)
		if err != nil {
			return err
		}
		if in.State.Health != nil && in.State.Health.Status == "healthy" {
			return nil
		}
		if in.State.Status == "exited" || in.State.Status == "dead" {
			return fmt.Errorf("the database container stopped (exit code %d)", in.State.ExitCode)
		}
		if time.Now().After(deadline) {
			return errors.New("the database did not become healthy in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// wpCLI runs a fixed shell script in a short-lived WP-CLI container that
// shares the instance's files and network. It returns the exit code and the
// combined output.
func wpCLI(ctx context.Context, c *dockerClient, name string, extraEnv []string, script string) (int, string, error) {
	wp, err := c.inspect(ctx, agenttask.WPContainer(name))
	if err != nil {
		return 0, "", fmt.Errorf("WordPress instance %s not found: %w", name, err)
	}
	if wp.Config.Labels[agenttask.CMSLabel] != name {
		return 0, "", fmt.Errorf("container %s was not created by OSRIVA", agenttask.WPContainer(name))
	}
	env := []string{"HOME=/tmp", "WP_CLI_CACHE_DIR=/tmp/wp-cli-cache"}
	for _, e := range wp.Config.Env {
		if strings.HasPrefix(e, "WORDPRESS_") {
			env = append(env, e) // wp-config.php reads the database settings from the environment
		}
	}
	body := map[string]any{
		"Image":  agenttask.WPCLIImage,
		"User":   "33:33", // www-data in the Debian-based WordPress image owns the files
		"Cmd":    []string{"sh", "-c", script},
		"Env":    append(env, extraEnv...),
		"Labels": cmsLabels(name),
		"HostConfig": map[string]any{
			"VolumesFrom": []string{wp.ID},
			"NetworkMode": agenttask.WPNetwork(name),
			"Privileged":  false,
			"SecurityOpt": []string{"no-new-privileges"},
		},
	}
	id, err := c.createStart(ctx, "", body)
	if id != "" {
		defer func() {
			_ = c.json(context.WithoutCancel(ctx), http.MethodDelete, "/containers/"+id, url.Values{"force": {"1"}}, nil, nil)
		}()
	}
	if err != nil {
		return 0, "", err
	}
	var waited struct{ StatusCode int }
	if err := c.json(ctx, http.MethodPost, "/containers/"+id+"/wait", nil, nil, &waited); err != nil {
		return 0, "", err
	}
	out, err := c.rawLogs(ctx, id)
	return waited.StatusCode, out, err
}

func wpInfo(ctx context.Context, c *dockerClient, name string) (agenttask.WPInfoResult, error) {
	res := agenttask.WPInfoResult{Name: name, Plugins: []agenttask.WPComponent{}, Themes: []agenttask.WPComponent{}}
	for _, n := range []string{agenttask.WPContainer(name), agenttask.WPDBContainer(name)} {
		in, err := c.inspect(ctx, n)
		var de *dockerError
		if errors.As(err, &de) && de.Status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return res, err
		}
		if in.Config.Labels[agenttask.CMSLabel] != name {
			return res, fmt.Errorf("container %s was not created by OSRIVA", n)
		}
		if n == agenttask.WPContainer(name) {
			res.State, res.Running = in.State.Status, in.State.Status == "running"
		} else {
			res.DBState = in.State.Status
		}
	}
	if res.State == "" {
		return res, fmt.Errorf("WordPress instance %s not found", name)
	}
	if !res.Running || res.DBState != "running" {
		return res, nil
	}
	// Plugins and themes are not loaded: listing must not run their code.
	code, out, err := wpCLI(ctx, c, name, nil, `set -e
wp core is-installed --skip-plugins --skip-themes
echo '<<osriva>>'; wp core version --skip-plugins --skip-themes 2>/dev/null
echo '<<osriva>>'; wp core check-update --format=json --skip-plugins --skip-themes 2>/dev/null || true
echo '<<osriva>>'; wp plugin list --format=json --fields=name,status,version,update_version --skip-plugins --skip-themes 2>/dev/null
echo '<<osriva>>'; wp theme list --format=json --fields=name,status,version,update_version --skip-plugins --skip-themes 2>/dev/null
echo '<<osriva>>'; wp option get siteurl --skip-plugins --skip-themes 2>/dev/null`)
	if err != nil {
		return res, err
	}
	if code != 0 {
		return res, nil // not installed (yet)
	}
	res.Installed = true
	parts := strings.Split(out, "<<osriva>>\n")
	if len(parts) == 6 {
		res.CoreVersion = strings.TrimSpace(parts[1])
		var upd []struct{ Version string }
		if json.Unmarshal([]byte(strings.TrimSpace(parts[2])), &upd) == nil && len(upd) > 0 {
			res.CoreUpdate = upd[0].Version
		}
		_ = json.Unmarshal([]byte(strings.TrimSpace(parts[3])), &res.Plugins)
		_ = json.Unmarshal([]byte(strings.TrimSpace(parts[4])), &res.Themes)
		res.SiteURL = strings.TrimSpace(parts[5])
	}
	return res, nil
}

func wpRun(ctx context.Context, c *dockerClient, name, script string, failIsError bool) (agenttask.WPCommandResult, error) {
	res := agenttask.WPCommandResult{Name: name}
	code, out, err := wpCLI(ctx, c, name, nil, script)
	res.Output = out
	if err != nil {
		return res, err
	}
	res.OK = code == 0
	if !res.OK && failIsError {
		return res, &ResultError{Result: res, Err: fmt.Errorf("WP-CLI exited with code %d", code)}
	}
	return res, nil
}

func cmsRemove(ctx context.Context, c *dockerClient, p agenttask.CMSRemoveParams) (agenttask.CMSRemoveResult, error) {
	res := agenttask.CMSRemoveResult{Name: p.Name, Removed: []string{}}
	objs := [][2]string{{"container", agenttask.WPContainer(p.Name)}, {"container", agenttask.WPDBContainer(p.Name)}, {"network", agenttask.WPNetwork(p.Name)}}
	if p.DeleteData {
		objs = append(objs, [2]string{"volume", agenttask.WPFilesVolume(p.Name)}, [2]string{"volume", agenttask.WPDBVolume(p.Name)})
	}
	// Check everything first: nothing is removed if any object is not ours.
	for _, o := range objs {
		exists, ours, _, err := c.objectState(ctx, o[0], o[1], p.Name)
		if err != nil {
			return res, err
		}
		if exists && !ours {
			return res, fmt.Errorf("%s %s was not created by OSRIVA; nothing was removed", o[0], o[1])
		}
	}
	for _, o := range objs {
		exists, _, id, _ := c.objectState(ctx, o[0], o[1], p.Name)
		if !exists {
			continue
		}
		var err error
		switch o[0] {
		case "container":
			err = c.json(ctx, http.MethodDelete, "/containers/"+id, url.Values{"force": {"1"}}, nil, nil)
		case "network":
			err = c.json(ctx, http.MethodDelete, "/networks/"+url.PathEscape(o[1]), nil, nil, nil)
		case "volume":
			err = c.json(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(o[1]), nil, nil, nil)
		}
		if err != nil {
			return res, fmt.Errorf("removing %s %s: %w", o[0], o[1], err)
		}
		res.Removed = append(res.Removed, o[1])
	}
	res.DataDeleted = p.DeleteData
	return res, nil
}

func cmsTaskRun(ctx context.Context, typ string, params json.RawMessage) (any, error) {
	c, err := newDocker()
	if err != nil {
		return nil, err
	}
	switch typ {
	case agenttask.CMSWPInstall:
		var p agenttask.WPInstallParams
		_ = json.Unmarshal(params, &p)
		return wpInstall(ctx, c, p)
	case agenttask.CMSRemove:
		var p agenttask.CMSRemoveParams
		_ = json.Unmarshal(params, &p)
		return cmsRemove(ctx, c, p)
	case agenttask.CMSWPUpdate:
		var p agenttask.WPUpdateParams
		_ = json.Unmarshal(params, &p)
		script := map[string]string{
			"core":    "wp core update && wp core update-db",
			"plugins": "wp plugin update --all",
			"themes":  "wp theme update --all",
		}[p.Target]
		return wpRun(ctx, c, p.Name, script, true)
	}
	var p agenttask.CMSNameParams
	_ = json.Unmarshal(params, &p)
	if typ == agenttask.CMSWPVerify {
		// Compares every file with the checksums published by wordpress.org.
		return wpRun(ctx, c, p.Name, "wp core verify-checksums --skip-plugins --skip-themes && wp plugin verify-checksums --all --skip-plugins --skip-themes", false)
	}
	return wpInfo(ctx, c, p.Name)
}

// rawLogs returns a finished container's stdout and stderr in order, untruncated
// per line (WP-CLI prints JSON on one line), at most 1 MiB.
func (c *dockerClient) rawLogs(ctx context.Context, id string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/logs", url.Values{"stdout": {"1"}, "stderr": {"1"}}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, 1<<20)
	var out strings.Builder
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(body, hdr[:]); err != nil {
			break
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		if _, err := io.CopyN(&out, body, n); err != nil {
			break
		}
	}
	return out.String(), nil
}
