package runtug

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

//go:embed artifact-policy.json
var defaultArtifactPolicy []byte

type Content struct {
	Path       string `json:"path"`
	Bytes      int64  `json:"bytes"`
	SHA        string `json:"sha256"`
	State      string `json:"state,omitempty"`
	UploadedAt string `json:"uploaded_at,omitempty"`
	Mirror     bool   `json:"mirror_required,omitempty"`
}
type Archive struct {
	Source      string    `json:"source,omitempty"`
	DrivePath   string    `json:"drive_path"`
	Contents    []Content `json:"contents"`
	State       string    `json:"state"`
	ConfirmedAt string    `json:"drive_confirmed_at,omitempty"`
	Manifest    string    `json:"manifest"`
	ManifestSHA string    `json:"manifest_sha256"`
}
type ArtifactRecord struct {
	Schema  string    `json:"schema"`
	Name    string    `json:"name"`
	Host    string    `json:"host"`
	Job     string    `json:"job_id,omitempty"`
	Task    string    `json:"task_id,omitempty"`
	Archive Archive   `json:"archive"`
	Deleted []Content `json:"local_deleted,omitempty"`
}

func hashFile(path string) (int64, string, error) {
	f, e := os.Open(path)
	if e != nil {
		return 0, "", e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), e
}
func glob(pattern, path string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String()).MatchString(path)
}
func excluded(path string, patterns []string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		lower := strings.ToLower(part)
		if strings.HasPrefix(lower, ".env") || strings.Contains(lower, "credential") || strings.Contains(lower, "secret") || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") || lower == ".git" {
			return true
		}
	}
	for _, pat := range patterns {
		if glob(pat, path) || glob(pat, filepath.Base(path)) {
			return true
		}
	}
	return false
}
func inventory(root string, exclude []string) ([]Content, error) {
	var rows []Content
	e := filepath.WalkDir(root, func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if path == root {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if excluded(rel, exclude) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || strings.ContainsAny(rel, "\t\r\n") {
			return fail(2, "nonregular or unrepresentable artifact path")
		}
		n, h, e := hashFile(path)
		if e != nil {
			return e
		}
		rows = append(rows, Content{Path: filepath.ToSlash(rel), Bytes: n, SHA: h})
		return nil
	})
	return rows, e
}
func (a *App) artifacts(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail(2, "artifacts requires move or sweep")
	}
	switch args[0] {
	case "move":
		return a.artifactMove(ctx, args[1:])
	case "sweep":
		return a.artifactSweep(ctx, args[1:])
	default:
		return fail(2, "unknown artifacts command")
	}
}
func (a *App) artifactMove(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail(2, "move requires a source tree")
	}
	src := expand(args[0])
	f := flags("artifacts move")
	dest := f.String("dest", "", "")
	sourceHost := f.String("source-host", "", "")
	apply := f.Bool("apply", false, "")
	if e := f.Parse(args[1:]); e != nil {
		return fail(2, "%v", e)
	}
	if f.NArg() > 0 {
		return fail(2, "unexpected move arguments")
	}
	root := expand(str(a.Config.Artifacts, "drive_folder"))
	target := expand(*dest)
	if !filepath.IsAbs(root) || !filepath.IsAbs(target) || !within(root, target) || *dest == "" {
		return fail(2, "--dest must be under artifacts.drive_folder")
	}
	if e := noSymlinks(target); e != nil {
		return e
	}
	if src == target || within(src, target) || within(target, src) {
		return fail(2, "source and destination trees overlap")
	}
	exclude := stringsValue(a.Config.Artifacts, "exclude")
	if len(exclude) == 0 {
		return fail(70, "artifacts.exclude is required")
	}
	if *sourceHost != "" {
		return a.remoteArtifactMove(ctx, src, target, *sourceHost, *apply)
	}
	if !filepath.IsAbs(src) {
		var e error
		src, e = filepath.Abs(src)
		if e != nil {
			return e
		}
	}
	if e := noSymlinks(src); e != nil {
		return e
	}
	rows, e := inventory(src, exclude)
	if e != nil {
		return e
	}
	if len(rows) == 0 {
		return fail(2, "empty artifact inventory")
	}
	rel, e := filepath.Rel(root, target)
	if e != nil {
		return e
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 || !validID(parts[0]) {
		return fail(2, "dest must be drive_folder/host/name")
	}
	fmt.Fprintf(a.Out, "artifact-move source=%s dest=%s files=%d apply=%t\n", src, target, len(rows), *apply)
	if !*apply {
		for _, row := range rows {
			fmt.Fprintf(a.Out, "copy path=%s bytes=%d sha256=%s\n", row.Path, row.Bytes, row.SHA)
		}
		return nil
	}
	record := ArtifactRecord{Schema: "fleet.artifact.v2", Host: parts[0], Name: strings.Join(parts[1:], "/"), Archive: Archive{Source: src, DrivePath: target, State: "running"}}
	key := "artifacts/" + parts[0] + "/" + record.Name
	batchFiles := integer(a.Config.Artifacts, "batch_files", 2000)
	batchBytes := integer(a.Config.Artifacts, "batch_bytes", 3<<30)
	if batchFiles <= 0 || batchBytes <= 0 {
		return fail(70, "invalid artifact batch limits")
	}
	for i := 0; i < len(rows); {
		end := i
		size := int64(0)
		for end < len(rows) && int64(end-i) < batchFiles && (size < batchBytes || end == i) {
			size += rows[end].Bytes
			end++
		}
		var paths []string
		for _, row := range rows[i:end] {
			source := filepath.Join(src, filepath.FromSlash(row.Path))
			destination := filepath.Join(target, filepath.FromSlash(row.Path))
			if e = noSymlinks(destination); e != nil {
				return e
			}
			if e = os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
				return e
			}
			if e = copyVerified(source, destination, row); e != nil {
				return e
			}
			paths = append(paths, destination)
		}
		if e = a.confirmUpload(ctx, paths); e != nil {
			return e
		}
		for k := i; k < end; k++ {
			rows[k].State = "uploaded"
			rows[k].UploadedAt = a.Now().UTC().Format(time.RFC3339)
		}
		record.Archive.Contents = append(record.Archive.Contents, rows[i:end]...)
		if e = a.putRecord(ctx, key, record); e != nil {
			return e
		}
		// Evict Drive copies only after the server has retained confirmation.
		helper := expand(str(a.Config.Artifacts, "fpbulk"))
		r := a.Runner.Run(ctx, Command{Name: helper, Args: []string{"evict"}, Input: strings.Join(paths, "\n") + "\n"})
		if e = check(r, "Drive eviction"); e != nil {
			return e
		}
		i = end
	}
	var manifest strings.Builder
	manifest.WriteString("path\tsize\tsha256\n")
	for _, row := range rows {
		fmt.Fprintf(&manifest, "%s\t%d\t%s\n", row.Path, row.Bytes, row.SHA)
	}
	manifestPath := target + ".manifest.tsv"
	if e = noSymlinks(manifestPath); e != nil {
		return e
	}
	if e = os.WriteFile(manifestPath, []byte(manifest.String()), 0600); e != nil {
		return e
	}
	_, sha, e := hashFile(manifestPath)
	if e != nil {
		return e
	}
	record.Archive.Manifest = filepath.Base(manifestPath)
	record.Archive.ManifestSHA = sha
	record.Archive.State = "drive_confirmed"
	record.Archive.ConfirmedAt = a.Now().UTC().Format(time.RFC3339)
	if e = a.putRecord(ctx, key, record); e != nil {
		return e
	}
	fmt.Fprintf(a.Out, "OK artifact=%s files=%d originals=retained\n", key, len(rows))
	return nil
}
func copyVerified(src, dest string, row Content) error {
	n, hash, e := hashFile(src)
	if e != nil {
		return e
	}
	if n != row.Bytes || hash != row.SHA {
		return fail(75, "source changed during copy")
	}
	if _, e = os.Lstat(dest); e == nil {
		n, h, e := hashFile(dest)
		if e != nil || n != row.Bytes || h != row.SHA {
			return fail(75, "destination conflict")
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, err := io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	n, h, e := hashFile(dest)
	if e != nil {
		return e
	}
	if n != row.Bytes || h != row.SHA {
		return fail(75, "copied file hash mismatch")
	}
	return nil
}
func (a *App) confirmUpload(ctx context.Context, paths []string) error {
	helper := expand(str(a.Config.Artifacts, "fpbulk"))
	logs := expand(str(a.Config.Artifacts, "drivefs_log_dir"))
	if helper == "" || logs == "" {
		return fail(70, "upload confirmation needs fpbulk and drivefs_log_dir")
	}
	gap := time.Duration(integer(a.Config.Artifacts, "confirm_gap_seconds", 120)) * time.Second
	if gap < 120*time.Second {
		return fail(70, "confirmation gap must be at least 120 seconds")
	}
	timeout := time.Duration(integer(a.Config.Artifacts, "confirm_timeout_seconds", 21600)) * time.Second
	poll := time.Duration(integer(a.Config.Artifacts, "poll_seconds", 30)) * time.Second
	if poll <= 0 || timeout < gap {
		return fail(70, "invalid confirmation timing")
	}
	stale := time.Duration(integer(a.Config.Artifacts, "stale_minutes", 10)) * time.Minute
	deadline := a.Now().Add(timeout)
	var first time.Time
	for {
		app := str(a.Config.Artifacts, "app_check")
		if app == "" {
			app = "pgrep -x 'Google Drive'"
		}
		r := a.Runner.Run(ctx, Command{Name: "sh", Args: []string{"-c", app}})
		fresh := false
		entries, _ := os.ReadDir(logs)
		for _, ent := range entries {
			if info, e := ent.Info(); e == nil && !ent.IsDir() && a.Now().Sub(info.ModTime()) <= stale {
				fresh = true
			}
		}
		status := a.Runner.Run(ctx, Command{Name: helper, Args: []string{"status"}, Input: strings.Join(paths, "\n") + "\n"})
		uploaded := map[string]bool{}
		for _, line := range strings.Split(status.Out, "\n") {
			flag, path, ok := strings.Cut(line, "\t")
			if ok {
				uploaded[path] = len(flag) == 2 && flag[0] == '1' && (flag[1] == '0' || flag[1] == '1')
			}
		}
		all := r.Code == 0 && fresh && status.Code == 0
		for _, path := range paths {
			all = all && uploaded[path]
		}
		if all {
			if !first.IsZero() && a.Now().Sub(first) >= gap {
				return nil
			}
			if first.IsZero() {
				first = a.Now()
			}
		} else {
			first = time.Time{}
		}
		if !a.Now().Before(deadline) {
			return fail(3, "upload confirmation timed out; copies retained")
		}
		if e := a.Sleep(ctx, poll); e != nil {
			return e
		}
	}
}
func (a *App) putRecord(ctx context.Context, key string, record any) error {
	b, e := encode(record)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp("", "runtug-artifact-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return a.must(ctx, "handoffkeep", "doc", "put", "--key", key, "--kind", "artifact", "--file", f.Name())
}
func (a *App) remoteArtifactMove(ctx context.Context, src, dest, host string, apply bool) error {
	h, ok := a.Config.Hosts[host]
	if !ok || str(h, "ssh") == "" || !filepath.IsAbs(src) {
		return fail(70, "remote source needs configured SSH host and absolute source")
	}
	if !apply {
		fmt.Fprintf(a.Out, "dry-run pull source_host=%s source=%s dest=%s (remote read only)\n", host, src, dest)
		return nil
	}
	// Transfer only, using rsync's exclude filters; no remote command or delete.
	scratch, e := os.MkdirTemp(expand(str(a.Config.Artifacts, "scratch_dir")), "runtug-pull-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(scratch)
	args := []string{"-a", "--safe-links", "--protect-args"}
	for _, pat := range append(stringsValue(a.Config.Artifacts, "exclude"), ".env*", "*.pem", "*.key", "*secret*", "*credential*", ".git") {
		args = append(args, "--exclude", pat)
	}
	args = append(args, "--", str(h, "ssh")+":"+src+"/", scratch+"/")
	if e = a.must(ctx, "rsync", args...); e != nil {
		return e
	}
	return a.artifactMove(ctx, []string{scratch, "--dest", dest, "--apply"})
}

type ArtifactPolicy struct {
	Schema string                     `json:"schema"`
	Roots  map[string]json.RawMessage `json:"roots"`
	Rows   []ArtifactRule             `json:"rows"`
}
type ArtifactRule struct {
	Class, Root, Glob, Host, Job string
	KeepNewest                   int  `json:"keep_newest"`
	Mirror                       bool `json:"mirror_required"`
}

func (a *App) artifactSweep(ctx context.Context, args []string) error {
	f := flags("sweep")
	job := f.String("job", "", "")
	ageArg := f.String("age", "7d", "")
	policyPath := f.String("policy", "", "")
	apply := f.Bool("apply", false, "")
	if e := f.Parse(args); e != nil {
		return fail(2, "%v", e)
	}
	if f.NArg() > 0 {
		return fail(2, "unexpected sweep arguments")
	}
	age, e := duration(*ageArg)
	if e != nil || age < 0 {
		return fail(2, "invalid sweep age")
	}
	b := defaultArtifactPolicy
	if *policyPath != "" {
		b, e = os.ReadFile(*policyPath)
		if e != nil {
			return e
		}
	}
	var pol ArtifactPolicy
	if e = decode(b, &pol); e != nil || pol.Schema != "fleet.artifact-policy.v1" {
		return fail(70, "invalid artifact policy")
	}
	hatch := str(a.Config.Artifacts, "local_delete_at")
	if hatch == "" {
		hatch = "drive_confirmed"
	}
	if hatch != "drive_confirmed" && hatch != "uploaded" {
		return fail(70, "invalid local_delete_at")
	}
	agents, e := a.agents(ctx)
	if e != nil {
		return e
	}
	jobs, e := os.ReadDir(a.Jobs)
	if e != nil {
		return e
	}
	for _, entry := range jobs {
		if !entry.IsDir() || *job != "" && entry.Name() != *job {
			continue
		}
		j, e := a.job(entry.Name())
		if e != nil {
			return e
		}
		if !j.Terminal || j.Keep || a.Now().Sub(j.At) < age {
			continue
		}
		alive := false
		for _, ag := range agents {
			if ag.Pane == j.Pane {
				alive = true
			}
		}
		if alive {
			continue
		}
		r := a.call(ctx, "handoffkeep", "tasks", "show", j.Task)
		if r.Code != 0 {
			continue
		}
		var task struct{ State, Project string }
		if json.Unmarshal([]byte(r.Out), &task) != nil || !slices.Contains([]string{"merged", "dropped"}, task.State) || task.Project == "" {
			continue
		}
		prefix := "artifacts/" + task.Project + "/" + j.Task + "/" + j.ID
		r = a.call(ctx, "handoffkeep", "doc", "list", "--prefix", prefix)
		if r.Code != 0 {
			continue
		}
		var docs []struct{ Key string }
		if json.Unmarshal([]byte(r.Out), &docs) != nil {
			return fail(70, "invalid artifact doc listing")
		}
		for _, doc := range docs {
			if doc.Key != prefix && !strings.HasPrefix(doc.Key, prefix+"/") {
				continue
			}
			r = a.call(ctx, "handoffkeep", "doc", "get", doc.Key)
			if r.Code != 0 {
				continue
			}
			var d struct{ Body string }
			if json.Unmarshal([]byte(r.Out), &d) != nil {
				continue
			}
			var rec ArtifactRecord
			if json.Unmarshal([]byte(d.Body), &rec) != nil || rec.Schema != "fleet.artifact.v2" || rec.Host != a.machine() || rec.Job != "" && rec.Job != j.ID || rec.Task != "" && rec.Task != j.Task {
				continue
			}
			if e = a.sweepRecord(ctx, j, doc.Key, rec, pol, hatch, *apply); e != nil {
				return e
			}
		}
	}
	return nil
}
func (a *App) sweepRecord(ctx context.Context, j Job, key string, rec ArtifactRecord, pol ArtifactPolicy, hatch string, apply bool) error {
	src := rec.Archive.Source
	if !filepath.IsAbs(src) || !within(filepath.Join(a.Jobs, j.ID), src) {
		return nil
	}
	if e := noSymlinks(src); e != nil {
		return e
	}
	for _, row := range rec.Archive.Contents {
		if filepath.IsAbs(row.Path) || !within(src, filepath.Join(src, row.Path)) {
			continue
		}
		path := filepath.Join(src, row.Path)
		class := ""
		mirror := row.Mirror
		for _, rule := range pol.Rows {
			var root struct{ Path, Kind string }
			raw, ok := pol.Roots[rule.Root]
			if !ok {
				return fail(70, "unknown artifact policy root")
			}
			if json.Unmarshal(raw, &root) != nil {
				var rel string
				if json.Unmarshal(raw, &rel) != nil {
					return fail(70, "invalid artifact policy root")
				}
				root.Path = rel
				root.Kind = "per-job"
			}
			workRoot := filepath.Dir(filepath.Dir(a.Jobs))
			base := filepath.Join(workRoot, root.Path)
			if !within(workRoot, base) {
				return fail(70, "artifact root escapes work root")
			}
			if root.Kind == "per-job" {
				base = filepath.Join(base, j.ID)
			}
			rel, e := filepath.Rel(base, path)
			if e != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			if rule.Job != "" && rule.Job != j.ID || rule.Host != "" && rule.Host != a.machine() {
				continue
			}
			if glob(rule.Glob, filepath.ToSlash(rel)) {
				class = rule.Class
				mirror = mirror || rule.Mirror
				break
			}
		}
		if class != "evidence" && class != "final-build" {
			continue
		}
		if rec.Archive.State != "drive_confirmed" && rec.Archive.ConfirmedAt == "" {
			if hatch != "uploaded" || class == "evidence" || mirror || row.UploadedAt == "" {
				continue
			}
		}
		if excluded(row.Path, stringsValue(a.Config.Artifacts, "exclude")) {
			continue
		}
		if e := noSymlinks(path); e != nil {
			continue
		}
		n, h, e := hashFile(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if n != row.Bytes || h != row.SHA {
			continue
		}
		fmt.Fprintf(a.Out, "artifact-sweep job=%s path=%s class=%s apply=%t\n", j.ID, path, class, apply)
		if !apply {
			continue
		}
		latest, e := a.job(j.ID)
		if e != nil {
			return e
		}
		if !latest.Terminal || latest.Keep {
			return fail(75, "job changed during artifact sweep")
		}
		n, h, e = hashFile(path)
		if e != nil || n != row.Bytes || h != row.SHA {
			return fail(75, "artifact changed before deletion")
		}
		rec.Deleted = append(rec.Deleted, row)
		if e = a.putRecord(ctx, key, rec); e != nil {
			return e
		}
		if e = os.Remove(path); e != nil {
			return e
		}
	}
	return nil
}
