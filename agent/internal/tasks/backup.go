package tasks

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// Backups (docs/adr/0010-backups.md): tar → gzip → age (X25519), written to
// backupDir. Volumes are read from their local mount point (the agent runs as
// root); WordPress databases are dumped with mariadb-dump in a short-lived
// container on the instance network.

var (
	backupDir = "/var/lib/osriva-agent/backups"
	// volumePath resolves a local volume's directory; replaced in tests.
	volumePath = func(ctx context.Context, c *dockerClient, name string) (string, error) {
		var v struct{ Driver, Mountpoint string }
		if err := c.json(ctx, http.MethodGet, "/volumes/"+url.PathEscape(name), nil, nil, &v); err != nil {
			return "", err
		}
		if v.Driver != "local" {
			return "", fmt.Errorf("volume %s uses the %q driver; only local volumes can be backed up", name, v.Driver)
		}
		fi, err := os.Stat(v.Mountpoint)
		if err != nil || !fi.IsDir() {
			return "", fmt.Errorf("volume %s: mount point %s is not readable here (rootless Docker or a remote engine?)", name, v.Mountpoint)
		}
		return v.Mountpoint, nil
	}
	// runDBTool runs a MariaDB client command for a WordPress instance with dir mounted at /backup; replaced in tests.
	runDBTool = dbToolContainer
)

const backupExt = ".osb"

type backupManifest struct {
	Format  int    `json:"format"`
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Created string `json:"created"`
}

func backupPath(id string) string { return filepath.Join(backupDir, id+backupExt) }

// usersOf lists the containers that mount a volume, refusing OSRIVA's control plane.
func usersOf(ctx context.Context, c *dockerClient, volume string) ([]string, error) {
	f, _ := json.Marshal(map[string][]string{"volume": {volume}})
	var cts []struct {
		ID     string `json:"Id"`
		State  string
		Labels map[string]string
	}
	if err := c.json(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}, "filters": {string(f)}}, nil, &cts); err != nil {
		return nil, err
	}
	running := []string{}
	for _, ct := range cts {
		if protected(ct.Labels) {
			return nil, fmt.Errorf("volume %s belongs to the OSRIVA control plane; back it up with osriva-install.sh backup", volume)
		}
		if ct.State == "running" {
			running = append(running, ct.ID)
		}
	}
	return running, nil
}

// tarTree writes dir into tw under prefix, preserving modes, owners and links.
func tarTree(tw *tar.Writer, dir, prefix string) (int, error) {
	files := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case info.IsDir(), info.Mode().IsRegular():
		default:
			return nil // sockets, devices and pipes are not backed up
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = prefix + filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uname, hdr.Gname = "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		files++
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.CopyN(tw, f, info.Size())
			f.Close()
			return err
		}
		return nil
	})
	return files, err
}

func addFile(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func backupCreate(ctx context.Context, c *dockerClient, p agenttask.BackupCreateParams) (agenttask.BackupCreateResult, error) {
	start := time.Now()
	res := agenttask.BackupCreateResult{ID: p.ID, Kind: p.Kind, Target: p.Target}
	recipient, err := age.ParseX25519Recipient(p.Recipient)
	if err != nil {
		return res, fmt.Errorf("invalid recipient: %w", err)
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return res, err
	}
	if _, err := os.Stat(backupPath(p.ID)); err == nil {
		return res, fmt.Errorf("backup %s already exists", p.ID)
	}

	type source struct{ dir, prefix string }
	var sources []source
	var dbDump string
	switch p.Kind {
	case agenttask.BackupVolume:
		if _, err := usersOf(ctx, c, p.Target); err != nil {
			return res, err
		}
		dir, err := volumePath(ctx, c, p.Target)
		if err != nil {
			return res, err
		}
		sources = []source{{dir, "volume/"}}
	case agenttask.BackupWordPress:
		files, err := checkWordPress(ctx, c, p.Target)
		if err != nil {
			return res, err
		}
		tmp, err := os.MkdirTemp(backupDir, "dump-")
		if err != nil {
			return res, err
		}
		defer os.RemoveAll(tmp)
		// A consistent snapshot of the InnoDB tables without stopping the site.
		if err := runDBTool(ctx, c, p.Target, tmp,
			`mariadb-dump -h "$DB_HOST" -u wordpress --single-transaction --routines --triggers --default-character-set=utf8mb4 wordpress > /backup/db.sql`); err != nil {
			return res, fmt.Errorf("database dump: %w", err)
		}
		dbDump = filepath.Join(tmp, "db.sql")
		sources = []source{{files, "files/"}}
	}

	tmpFile := backupPath(p.ID) + ".tmp"
	f, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return res, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmpFile)
		}
	}()
	cipherHash, plainHash := sha256.New(), sha256.New()
	enc, err := age.Encrypt(io.MultiWriter(f, cipherHash), recipient)
	if err != nil {
		return res, err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(io.MultiWriter(gz, plainHash))
	manifest, _ := json.Marshal(backupManifest{Format: 1, Kind: p.Kind, Target: p.Target, Created: time.Now().UTC().Format(time.RFC3339)})
	if err := addFile(tw, "manifest.json", manifest); err != nil {
		return res, err
	}
	if dbDump != "" {
		data, err := os.ReadFile(dbDump)
		if err != nil {
			return res, err
		}
		if err := addFile(tw, "db.sql", data); err != nil {
			return res, err
		}
		res.Files++
	}
	for _, s := range sources {
		n, err := tarTree(tw, s.dir, s.prefix)
		if err != nil {
			return res, fmt.Errorf("reading %s: %w", s.dir, err)
		}
		res.Files += n
	}
	for _, cl := range []io.Closer{tw, gz, enc} {
		if err := cl.Close(); err != nil {
			return res, err
		}
	}
	if err := f.Sync(); err != nil {
		return res, err
	}
	fi, _ := f.Stat()
	if err := f.Close(); err != nil {
		return res, err
	}
	if err := os.Rename(tmpFile, backupPath(p.ID)); err != nil {
		return res, err
	}
	ok = true
	res.Size, res.SHA256, res.PlainSHA256 = fi.Size(), hex.EncodeToString(cipherHash.Sum(nil)), hex.EncodeToString(plainHash.Sum(nil))
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}

func checkWordPress(ctx context.Context, c *dockerClient, name string) (string, error) {
	for _, n := range []string{agenttask.WPContainer(name), agenttask.WPDBContainer(name)} {
		in, err := c.inspect(ctx, n)
		if err != nil {
			return "", fmt.Errorf("WordPress instance %s: %w", name, err)
		}
		if in.Config.Labels[agenttask.CMSLabel] != name {
			return "", fmt.Errorf("container %s was not created by OSRIVA", n)
		}
	}
	return volumePath(ctx, c, agenttask.WPFilesVolume(name))
}

// dbToolContainer runs script in a MariaDB client container on the instance
// network with dir mounted at /backup; the password comes from the instance.
func dbToolContainer(ctx context.Context, c *dockerClient, name, dir, script string) error {
	wp, err := c.inspect(ctx, agenttask.WPContainer(name))
	if err != nil {
		return err
	}
	pass := ""
	for _, e := range wp.Config.Env {
		if v, ok := strings.CutPrefix(e, "WORDPRESS_DB_PASSWORD="); ok {
			pass = v
		}
	}
	body := map[string]any{
		"Image":  agenttask.MariaDBImage,
		"Cmd":    []string{"sh", "-c", script},
		"Env":    []string{"MYSQL_PWD=" + pass, "DB_HOST=" + agenttask.WPDBContainer(name)},
		"Labels": cmsLabels(name),
		"HostConfig": map[string]any{
			"Binds":       []string{dir + ":/backup"},
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
		return err
	}
	var waited struct{ StatusCode int }
	if err := c.json(ctx, http.MethodPost, "/containers/"+id+"/wait", nil, nil, &waited); err != nil {
		return err
	}
	if waited.StatusCode != 0 {
		out, _ := c.rawLogs(ctx, id)
		return fmt.Errorf("exit code %d: %s", waited.StatusCode, strings.TrimSpace(out))
	}
	return nil
}

// openBackup checks the file checksum and returns a tar reader over the
// decrypted, decompressed archive plus the hash of the tar stream.
func openBackup(id, wantSHA, key string) (*tar.Reader, func() string, io.Closer, error) {
	identity, err := age.ParseX25519Identity(key)
	if err != nil {
		return nil, nil, nil, errors.New("invalid backup key")
	}
	f, err := os.Open(backupPath(id))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("backup %s is not on this server", id)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		f.Close()
		return nil, nil, nil, fmt.Errorf("the backup file is damaged: checksum %s does not match %s", got[:12], wantSHA[:12])
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	dec, err := age.Decrypt(f, identity)
	if err != nil {
		f.Close()
		return nil, nil, nil, fmt.Errorf("cannot decrypt the backup (wrong key?): %w", err)
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	plain := sha256.New()
	tee := io.TeeReader(gz, plain)
	// sum drains the rest of the stream (the tar trailer) so the hash covers all of it.
	sum := func() string {
		_, _ = io.Copy(io.Discard, tee)
		return hex.EncodeToString(plain.Sum(nil))
	}
	return tar.NewReader(tee), sum, f, nil
}

// backupVerify decrypts and reads the whole archive; damage is a result, not an error.
func backupVerify(p agenttask.BackupVerifyParams) (agenttask.BackupVerifyResult, error) {
	res := agenttask.BackupVerifyResult{ID: p.ID}
	tr, sum, closer, err := openBackup(p.ID, p.SHA256, p.BackupKey)
	if err != nil {
		res.Problem = err.Error()
		return res, nil
	}
	defer closer.Close()
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			res.Problem = "the archive is truncated or corrupt: " + err.Error()
			return res, nil
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			res.Problem = "the archive is truncated or corrupt: " + err.Error()
			return res, nil
		}
		if hdr.Name != "manifest.json" {
			res.Files++
		}
	}
	res.PlainSHA256, res.OK = sum(), true
	return res, nil
}

// clearDir removes everything inside dir (but not dir itself).
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// safeJoin maps an archive name below root and refuses traversal or any
// existing symbolic link on the way (so extraction can never write outside root).
func safeJoin(root, name string) (string, error) {
	clean := path.Clean("/" + name)
	if clean == "/" || strings.Contains(name, "\x00") {
		return "", fmt.Errorf("invalid archive entry %q", name)
	}
	rel := strings.TrimPrefix(clean, "/")
	if rel != strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/") {
		return "", fmt.Errorf("archive entry %q is not a clean relative path", name)
	}
	cur := root
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("archive entry %q goes through a symbolic link", name)
		}
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}

// extractEntry writes one archive entry (already stripped of its prefix) below root.
func extractEntry(root, name string, hdr *tar.Header, r io.Reader) error {
	dst, err := safeJoin(root, name)
	if err != nil {
		return err
	}
	mode := fs.FileMode(hdr.Mode).Perm() | fs.FileMode(hdr.Mode)&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return err
		}
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, r)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	case tar.TypeSymlink:
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.Symlink(hdr.Linkname, dst); err != nil {
			return err
		}
		_ = os.Lchown(dst, hdr.Uid, hdr.Gid)
		return nil
	default:
		return nil // other entry types are never written by OSRIVA
	}
	_ = os.Lchown(dst, hdr.Uid, hdr.Gid)
	_ = os.Chmod(dst, mode)
	_ = os.Chtimes(dst, hdr.ModTime, hdr.ModTime)
	return nil
}

func backupRestore(ctx context.Context, c *dockerClient, p agenttask.BackupRestoreParams) (agenttask.BackupRestoreResult, error) {
	res := agenttask.BackupRestoreResult{ID: p.ID, Stopped: []string{}}
	// 1. Read and check the whole archive before touching anything.
	v, err := backupVerify(agenttask.BackupVerifyParams{ID: p.ID, SHA256: p.SHA256, BackupKey: p.BackupKey})
	if err != nil {
		return res, err
	}
	if !v.OK {
		return res, fmt.Errorf("nothing was changed: %s", v.Problem)
	}

	// 2. Work out where everything goes and what has to stop.
	var dataDir string
	var stop []string
	switch p.Kind {
	case agenttask.BackupVolume:
		if stop, err = usersOf(ctx, c, p.Target); err != nil {
			return res, err
		}
		if dataDir, err = volumePath(ctx, c, p.Target); err != nil {
			return res, err
		}
	case agenttask.BackupWordPress:
		if dataDir, err = checkWordPress(ctx, c, p.Target); err != nil {
			return res, err
		}
		wp, _ := c.inspect(ctx, agenttask.WPContainer(p.Target))
		if wp.State.Status == "running" {
			stop = []string{wp.ID}
		}
	}
	tmp, err := os.MkdirTemp(backupDir, "restore-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)

	for _, id := range stop {
		if err := c.json(ctx, http.MethodPost, "/containers/"+id+"/stop", url.Values{"t": {"20"}}, nil, nil); err != nil {
			return res, fmt.Errorf("stopping %s: %w", id[:12], err)
		}
		res.Stopped = append(res.Stopped, id)
	}
	defer func() {
		for _, id := range res.Stopped {
			_ = c.json(context.WithoutCancel(ctx), http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
		}
	}()

	// 3. Replace the data.
	if err := clearDir(dataDir); err != nil {
		return res, err
	}
	tr, _, closer, err := openBackup(p.ID, p.SHA256, p.BackupKey)
	if err != nil {
		return res, err
	}
	defer closer.Close()
	prefix := map[string]string{agenttask.BackupVolume: "volume/", agenttask.BackupWordPress: "files/"}[p.Kind]
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, err
		}
		switch {
		case hdr.Name == "db.sql" && p.Kind == agenttask.BackupWordPress:
			f, err := os.OpenFile(filepath.Join(tmp, "db.sql"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return res, err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return res, err
			}
			res.Database = true
		case strings.HasPrefix(hdr.Name, prefix) && hdr.Name != prefix:
			if err := extractEntry(dataDir, strings.TrimPrefix(hdr.Name, prefix), hdr, tr); err != nil {
				return res, err
			}
			res.Files++
		}
	}
	if res.Database {
		_ = os.Chmod(tmp, 0o755) // the client container reads it as its own root user
		if err := runDBTool(ctx, c, p.Target, tmp, `mariadb -h "$DB_HOST" -u wordpress wordpress < /backup/db.sql`); err != nil {
			return res, fmt.Errorf("database restore: %w", err)
		}
	}
	return res, nil
}

func backupList() ([]agenttask.BackupFile, error) {
	out := []agenttask.BackupFile{}
	entries, err := os.ReadDir(backupDir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), backupExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, agenttask.BackupFile{ID: strings.TrimSuffix(e.Name(), backupExt), Size: info.Size(),
			Modified: info.ModTime().UTC().Format(time.RFC3339)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out, nil
}

func backupTaskRun(ctx context.Context, typ string, params json.RawMessage) (any, error) {
	switch typ {
	case agenttask.BackupList:
		return backupList()
	case agenttask.BackupDelete:
		var p agenttask.BackupRefParams
		_ = json.Unmarshal(params, &p)
		err := os.Remove(backupPath(p.ID))
		if errors.Is(err, fs.ErrNotExist) {
			err = nil // already gone
		}
		return map[string]any{"id": p.ID, "deleted": true}, err
	case agenttask.BackupVerify:
		var p agenttask.BackupVerifyParams
		_ = json.Unmarshal(params, &p)
		return backupVerify(p)
	}
	c, err := newDocker()
	if err != nil {
		return nil, err
	}
	if typ == agenttask.BackupCreate {
		var p agenttask.BackupCreateParams
		_ = json.Unmarshal(params, &p)
		return backupCreate(ctx, c, p)
	}
	var p agenttask.BackupRestoreParams
	_ = json.Unmarshal(params, &p)
	return backupRestore(ctx, c, p)
}
