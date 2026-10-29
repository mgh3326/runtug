package runtug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// HostView is one machine's connectivity and load as reported by the hub's
// host list. LoadRatio is load5/ncpu; a nil ratio means the hub could not
// measure it. A wakeable machine absent from the list is treated as asleep;
// other unlisted machines are treated as awake and proven by the spawn leg.
type HostView struct {
	Name      string
	Connected bool
	LoadRatio *float64
	Reason    string
}

// PresenceRecord is a pacelever host-presence row: which host measured which
// shared-quota account key and when.
type PresenceRecord struct {
	HostID     string    `json:"host_id"`
	AccountKey string    `json:"account_key"`
	LastSeen   time.Time `json:"last_seen"`
}

// SharedAccount is the shared quota cache state of one account key. A degraded
// read counts as unknown, never as an absent account.
type SharedAccount struct {
	Key        string
	Observed   bool
	Fresh      bool
	Degraded   bool
	MeasuredAt time.Time
}

// PlacementSource supplies the external reads a placement decision needs:
// the hub's host list, pacelever's host-presence records and the shared quota
// cache. A read error degrades the corresponding inputs to "unknown".
type PlacementSource interface {
	Hosts(ctx context.Context, cwd string) ([]HostView, error)
	Presence(ctx context.Context) ([]PresenceRecord, error)
	SharedAccount(ctx context.Context, key string) (SharedAccount, error)
}

// WakeRequest describes one wake attempt on an asleep host.
type WakeRequest struct {
	HoldMinutes int
	WaitSeconds int
	Reason      string
}

// Waker wakes an asleep host. The in-flight `panewire node wake` command is
// the production implementation; tests substitute a fake.
type Waker interface {
	Wake(ctx context.Context, host string, req WakeRequest) error
}

const (
	loginPresent = "present"
	loginUnknown = "unknown"
)

var (
	tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	accountFP  = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func parseTags(raw string) ([]string, error) {
	var out []string
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !tagPattern.MatchString(t) {
			return nil, fail(2, "invalid tag %q", t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

func exitCode(e error) int {
	var x *ExitError
	if errors.As(e, &x) {
		return x.Code
	}
	return 0
}

func number(m map[string]any, k string) (float64, bool) {
	switch v := m[k].(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}

type placeCandidate struct {
	name     string
	cfg      map[string]any
	local    bool
	tags     []string
	order    int
	eligible bool
	missing  []string
	awake    bool
	wakeable bool
	excluded string
	login    string
	load     *float64
	over     bool
	outcome  string
}

type placePlan struct {
	tags       []string
	profile    string
	pool       string
	repo       string
	waited     time.Duration
	threshold  float64
	at         time.Time
	candidates []placeCandidate
	ordered    []*placeCandidate
	sleepers   []*placeCandidate
	wake       map[string]any
	degraded   []string
}

type candResult struct {
	ok          bool
	stop        error
	unreachable bool
	rc          int
	note        string
	res         Result
}

func (a *App) loadThreshold() float64 {
	switch n := a.Config.Placement["load_threshold"].(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return 1.0
}

func (a *App) placementTTL(key string, def int64) time.Duration {
	if n := integer(a.Config.Quota, key, 0); n > 0 {
		return time.Duration(n) * time.Second
	}
	return time.Duration(def) * time.Second
}

func (a *App) localLoad(ctx context.Context) *float64 {
	if a.LoadRatio == nil {
		return nil
	}
	r, e := a.LoadRatio(ctx)
	if e != nil || r < 0 {
		return nil
	}
	return &r
}

func (a *App) hostViews(ctx context.Context, cwd string) (map[string]HostView, []string) {
	if a.Source == nil {
		return nil, []string{"hosts_unavailable"}
	}
	views, e := a.Source.Hosts(ctx, cwd)
	if e != nil {
		return nil, []string{"hosts_degraded"}
	}
	m := map[string]HostView{}
	for _, v := range views {
		m[v.Name] = v
	}
	return m, nil
}

// neededAccounts resolves which shared account keys a quota pool needs:
// explicitly configured fingerprints, or the keys presence records carry for
// the pool.
func neededAccounts(quota map[string]any, pool string, presence []PresenceRecord) []string {
	var out []string
	if pool == "" {
		return out
	}
	if accounts, ok := quota["accounts"].(map[string]any); ok {
		if fp := str(accounts, pool); accountFP.MatchString(fp) {
			out = append(out, pool+":"+fp)
		}
	}
	for _, rec := range presence {
		if strings.HasPrefix(rec.AccountKey, pool+":") && !slices.Contains(out, rec.AccountKey) {
			out = append(out, rec.AccountKey)
		}
	}
	return out
}

// candidateLogin classes a host's evidence for holding the needed account.
// "present" requires a fresh presence record corroborated by a readable
// shared-cache observation; everything else is "unknown" — a degraded or
// missing reading is never treated as absent.
func (a *App) candidateLogin(presence []PresenceRecord, accounts map[string]SharedAccount, keys []string, hostID string, now time.Time, ttl time.Duration) string {
	if len(keys) == 0 {
		return ""
	}
	for _, rec := range presence {
		if rec.HostID != hostID || !slices.Contains(keys, rec.AccountKey) {
			continue
		}
		age := now.Sub(rec.LastSeen)
		if age < -time.Minute || age > ttl {
			continue
		}
		if acc := accounts[rec.AccountKey]; acc.Observed && !acc.Degraded {
			return loginPresent
		}
	}
	return loginUnknown
}

func (a *App) plan(ctx context.Context, s Spawn, pool string, waited time.Duration) (*placePlan, error) {
	required, e := parseTags(s.Tags)
	if e != nil {
		return nil, e
	}
	threshold := a.loadThreshold()
	hub, degraded := a.hostViews(ctx, s.CWD)
	var presence []PresenceRecord
	presencesDegraded := pool != "" && a.Source == nil
	if pool != "" && a.Source != nil {
		if records, e := a.Source.Presence(ctx); e != nil {
			presencesDegraded = true
		} else {
			presence = records
		}
	}
	keys := neededAccounts(a.Config.Quota, pool, presence)
	accounts := map[string]SharedAccount{}
	if a.Source != nil {
		for _, k := range keys {
			acc, e := a.Source.SharedAccount(ctx, k)
			if e != nil {
				acc = SharedAccount{Key: k, Degraded: true}
			}
			accounts[k] = acc
		}
	}
	if presencesDegraded {
		degraded = append(degraded, "presence_degraded")
	}
	if len(keys) > 0 {
		known := false
		for _, acc := range accounts {
			if acc.Observed && !acc.Degraded {
				known = true
			}
		}
		if !known {
			degraded = append(degraded, "quota_cache_degraded")
		}
	}
	now := a.Now()
	presTTL := a.placementTTL("presence_ttl_s", 86400)
	enum := []placeCandidate{}
	machine := a.machine()
	local := placeCandidate{name: machine, cfg: a.Config.Local, local: true, awake: true, tags: stringsValue(a.Config.Local, "tags")}
	if !boolean(a.Config.Local, "spawn", true) {
		local.excluded = "spawn_disabled"
	}
	local.load = a.localLoad(ctx)
	if local.load != nil {
		maxRatio, ok := number(a.Config.Local, "max_load_ratio")
		if ok && *local.load >= maxRatio {
			local.over = true
		}
	}
	if n := integer(a.Config.Local, "max_active", 0); n > 0 {
		if agents, e := a.agents(ctx); e == nil && int64(len(agents)) >= n {
			local.over = true
		}
	}
	local.login = a.candidateLogin(presence, accounts, keys, firstNonempty(str(a.Config.Local, "presence_id"), machine), now, presTTL)
	if presencesDegraded && local.login == "" {
		local.login = loginUnknown
	}
	enum = append(enum, local)
	names := slices.Clone(a.Config.HostOrder)
	var extra []string
	for name := range a.Config.Hosts {
		if !slices.Contains(names, name) {
			extra = append(extra, name)
		}
	}
	slices.Sort(extra)
	names = append(names, extra...)
	for i, name := range names {
		h := a.Config.Hosts[name]
		c := placeCandidate{name: name, cfg: h, order: i + 1, tags: stringsValue(h, "tags"), wakeable: str(h, "wake") == "panewire"}
		if hv, reported := hub[name]; reported {
			switch {
			case strings.Contains(hv.Reason, "not_accepting"):
				c.excluded = "not_accepting"
			case !hv.Connected:
			default:
				c.awake = true
			}
			c.load = hv.LoadRatio
		} else {
			c.awake = !c.wakeable
		}
		c.login = a.candidateLogin(presence, accounts, keys, firstNonempty(str(h, "presence_id"), name), now, presTTL)
		if presencesDegraded && c.login == "" {
			c.login = loginUnknown
		}
		enum = append(enum, c)
	}
	p := &placePlan{tags: required, profile: s.Profile, pool: pool, waited: waited, threshold: threshold, at: now, degraded: degraded}
	for i := range enum {
		c := &enum[i]
		for _, t := range required {
			if !slices.Contains(c.tags, t) {
				c.missing = append(c.missing, t)
			}
		}
		c.eligible = len(c.missing) == 0
		if !c.over && c.load != nil && *c.load >= threshold {
			c.over = true
		}
		switch {
		case c.excluded != "":
			c.outcome = c.excluded
		case !c.eligible:
			c.outcome = "missing_tags:" + strings.Join(c.missing, ",")
		case c.awake:
			p.ordered = append(p.ordered, c)
		case c.wakeable:
			p.sleepers = append(p.sleepers, c)
		default:
			c.outcome = "disconnected"
		}
	}
	p.candidates = enum
	slices.SortStableFunc(p.ordered, func(x, y *placeCandidate) int {
		if bx, by := placeBucket(*x, threshold), placeBucket(*y, threshold); bx != by {
			return bx - by
		}
		if x.login != y.login {
			if x.login == loginPresent || x.login == "" {
				return -1
			}
			return 1
		}
		lx, ly := loadOrInf(x.load), loadOrInf(y.load)
		if lx != ly {
			if lx < ly {
				return -1
			}
			return 1
		}
		return x.order - y.order
	})
	return p, nil
}

func loadOrInf(l *float64) float64 {
	if l == nil {
		return 1e9
	}
	return *l
}

// placeBucket orders awake candidates: hosts with positive login evidence and
// a measured load under the threshold first, then unknown-login hosts under
// the threshold, then candidates with no usable load reading, and only when
// nothing else qualifies any host at or above the load threshold.
func placeBucket(c placeCandidate, threshold float64) int {
	if c.over {
		return 4
	}
	under := c.load != nil && *c.load < threshold
	if c.login != loginUnknown {
		if under {
			return 0
		}
		return 2
	}
	if under {
		return 1
	}
	return 3
}

// record renders the placement decision written to the job record: every
// evaluated input (tags, pool, repo, wait), the per-candidate classification
// and the chosen host.
func (p *placePlan) record(chosen string) map[string]any {
	candidates := []any{}
	for _, c := range p.candidates {
		row := map[string]any{"host": c.name, "eligible": c.eligible, "awake": c.awake, "login": c.login, "over_threshold": c.over}
		if c.wakeable {
			row["wakeable"] = true
		}
		if c.local {
			row["local"] = true
		}
		if c.load != nil {
			row["load_ratio"] = *c.load
		}
		if len(c.missing) > 0 {
			row["missing_tags"] = c.missing
		}
		if c.outcome != "" {
			row["outcome"] = c.outcome
		}
		if c.name == chosen {
			row["chosen"] = true
		}
		candidates = append(candidates, row)
	}
	rec := map[string]any{"source": "policy", "tags": p.tags, "profile": p.profile, "waited_s": int64(p.waited.Seconds()), "load_threshold": p.threshold, "candidates": candidates, "chose": chosen, "at": p.at.Format(time.RFC3339Nano)}
	if p.pool != "" {
		rec["pool"] = p.pool
	}
	if p.repo != "" {
		rec["repo"] = p.repo
	}
	if len(p.degraded) > 0 {
		rec["degraded"] = p.degraded
	}
	if p.wake != nil {
		rec["wake"] = p.wake
	}
	return rec
}

func (p *placePlan) sleeper(name string) *placeCandidate {
	for _, c := range p.sleepers {
		if c.name == name {
			return c
		}
	}
	return nil
}

// wakeTargets applies the policy's wake rules once the awake round is over:
// a required capability no awake host has, every awake candidate over the
// load threshold after the job has waited long enough, the needed account
// measured only on an asleep host, or an operator request. Wakeable hosts
// that proved unreachable during the round join the target list whenever a
// policy reason fires; on their own they are not a wake reason.
func (a *App) wakeTargets(s Spawn, p *placePlan, unreachable []string) ([]string, []string) {
	var reasons, targets []string
	add := func(name string) {
		if !slices.Contains(targets, name) {
			targets = append(targets, name)
		}
	}
	if len(p.ordered) == 0 && len(p.sleepers) > 0 {
		reasons = append(reasons, "capability_absent_awake")
		for _, c := range p.sleepers {
			add(c.name)
		}
	}
	if len(p.ordered) > 0 && p.waited >= a.wakeAfter() {
		allOver := true
		for _, c := range p.ordered {
			if !c.over {
				allOver = false
			}
		}
		if allOver {
			reasons = append(reasons, "all_awake_over_threshold")
			for _, c := range p.sleepers {
				add(c.name)
			}
		}
	}
	if s.OperatorRequest != "" || s.Purpose == "predeploy" {
		reason := "operator_requested"
		if s.OperatorRequest == "" {
			reason = "predeploy_slow_suite"
		}
		for _, c := range p.sleepers {
			add(c.name)
		}
		if len(targets) > 0 {
			reasons = append(reasons, reason)
		}
	}
	// The needed account is logged in only on an asleep host when fresh
	// presence exists there and no eligible awake host shows it. Degraded
	// presence reads make this unprovable, so they never wake.
	presentOnAwake := false
	for _, c := range p.ordered {
		if c.login == loginPresent {
			presentOnAwake = true
		}
	}
	presentSleepers := []string{}
	for _, c := range p.sleepers {
		if c.login == loginPresent {
			presentSleepers = append(presentSleepers, c.name)
		}
	}
	if !presentOnAwake && len(presentSleepers) > 0 {
		reasons = append(reasons, "account_only_on")
		targets = append(presentSleepers, targets...)
	}
	if len(reasons) > 0 {
		for _, name := range unreachable {
			add(name)
		}
	}
	return targets, reasons
}

func (a *App) wakeAfter() time.Duration {
	if n := integer(a.Config.Placement, "wake_after_s", 0); n > 0 {
		return time.Duration(n) * time.Second
	}
	return 10 * time.Minute
}

// waitForNode polls the hub host list and an SSH probe until the woken node
// reports connected, then lets the caller place the job.
func (a *App) waitForNode(ctx context.Context, host string, h map[string]any, waitS int) error {
	if waitS <= 0 {
		waitS = 90
	}
	deadline := a.Now().Add(time.Duration(waitS) * time.Second)
	target := str(h, "ssh")
	if target == "" {
		target = host
	}
	for {
		if a.Source != nil {
			if views, e := a.Source.Hosts(ctx, ""); e == nil {
				for _, v := range views {
					if v.Name == host && v.Connected {
						return nil
					}
				}
			}
		}
		if r := a.call(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "--", target, "true"); r.Code == 0 {
			return nil
		}
		if !a.Now().Before(deadline) {
			return fail(81, "node %s did not connect after wake", host)
		}
		if e := a.Sleep(ctx, 5*time.Second); e != nil {
			return e
		}
	}
}

type repoResolution struct {
	name string
	url  string
	rel  string
}

// localRepo resolves the job checkout: the repo name from the top-level
// basename, the origin URL used to clone it on a target, and the cwd's path
// inside the checkout.
func (a *App) localRepo(ctx context.Context, cwd string) *repoResolution {
	r := a.call(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	top := strings.TrimSpace(r.Out)
	if r.Code != 0 || top == "" {
		return nil
	}
	name := filepath.Base(top)
	if !validID(name) {
		return nil
	}
	rel := "."
	if absTop, e1 := filepath.Abs(top); e1 == nil {
		if abs, e2 := filepath.Abs(cwd); e2 == nil {
			if v, e3 := filepath.Rel(absTop, abs); e3 == nil && v != ".." && !strings.HasPrefix(v, ".."+string(os.PathSeparator)) {
				rel = v
			}
		}
	}
	url := ""
	if g := a.call(ctx, "git", "-C", cwd, "remote", "get-url", "origin"); g.Code == 0 {
		url = strings.TrimSpace(g.Out)
		if strings.ContainsAny(url, "\r\n") || strings.HasPrefix(url, "-") {
			url = ""
		}
	}
	return &repoResolution{name: name, url: url, rel: rel}
}

// remoteDir maps the job cwd onto the target. An explicit cwd_map entry stays
// the per-path override; otherwise the repo clone convention applies:
// $HOME/work/<repo> on the target, cloned when missing.
func remoteDir(h map[string]any, cwd string, repo *repoResolution) (string, *repoResolution) {
	if mapped := mappedCWD(h["cwd_map"], cwd); mapped != "" {
		return mapped, nil
	}
	if repo == nil {
		return "", nil
	}
	dir := "$HOME/work/" + repo.name
	if repo.rel != "." && repo.rel != "" {
		dir += "/" + repo.rel
	}
	return dir, repo
}

func (a *App) ensureRepo(ctx context.Context, h map[string]any, host string, repo *repoResolution) error {
	target := str(h, "ssh")
	if target == "" {
		target = host
	}
	dir := `"$HOME/work/` + repo.name + `"`
	r := a.call(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "--", target, "test -d "+dir)
	if r.Code == 0 {
		return nil
	}
	if r.Code == 255 {
		return fail(255, "repo check unreachable")
	}
	if repo.url == "" {
		return fail(70, "repo %s missing on %s and no origin URL to clone", repo.name, host)
	}
	r = a.call(ctx, "ssh", "-o", "BatchMode=yes", "--", target, "git clone -- "+shellQuote(repo.url)+" "+dir)
	if r.Code == 255 {
		return fail(255, "repo clone unreachable")
	}
	if e := check(r, "repo clone"); e != nil {
		return e
	}
	return nil
}

func (a *App) spawnRemote(ctx context.Context, s Spawn, brief string, c *placeCandidate, repo *repoResolution, forced bool) candResult {
	h := c.cfg
	if s.ExecutionLane == "mock" {
		return candResult{stop: fail(79, "remote mock spawns refused")}
	}
	if s.DryRun {
		payload, e := a.prepareBriefForHost(s, c.name, brief)
		if e != nil {
			return candResult{stop: e}
		}
		fmt.Fprintf(a.Out, "dry-run host=%s profile=%s role=%s job=%s\n%s\n", c.name, s.Profile, s.Role, s.Job, payload)
		return candResult{ok: true}
	}
	if str(h, "via") == "hub" {
		return candResult{stop: fail(70, "hub spawn transport still targets wrk; use an SSH target running runtug")}
	}
	cwd, clone := remoteDir(h, s.CWD, repo)
	if cwd == "" {
		return candResult{note: "repo_unmappable"}
	}
	if clone != nil {
		if e := a.ensureRepo(ctx, h, c.name, clone); e != nil {
			if exitCode(e) == 255 {
				return candResult{unreachable: true, note: "repo:" + e.Error()}
			}
			return candResult{rc: exitCode(e), note: "repo:" + e.Error()}
		}
	}
	target := str(h, "ssh")
	if target == "" {
		target = c.name
	}
	if strings.HasPrefix(target, "-") || strings.ContainsAny(target, "\r\n") {
		return candResult{stop: fail(70, "invalid SSH target")}
	}
	remote := s
	remote.ParentJob = ""
	remote.CWD = cwd
	remote.Host = "local"
	remote.Workspace = str(h, "workspace")
	if remote.Workspace == "" {
		remote.Workspace = s.Workspace
	}
	remote.Prompt = ""
	remote.Tags = ""
	remote.Delegated = true
	if s.Report != "" {
		if !within(a.Jobs, s.Report) {
			return candResult{stop: fail(2, "remote report outside jobs root")}
		}
		rel, e := filepath.Rel(a.Jobs, s.Report)
		if e != nil {
			return candResult{stop: e}
		}
		remote.Report = ""
		remote.ReportRelative = rel
	}
	envelope := struct {
		Spawn Spawn
		Brief string
	}{remote, brief}
	b, e := encode(envelope)
	if e != nil {
		return candResult{stop: e}
	}
	session := str(h, "herdr_session")
	if session == "" {
		session = "worker"
	}
	r := a.Runner.Run(ctx, Command{Name: "ssh", Args: []string{"-o", "BatchMode=yes", "--", target, "env HERDR_SESSION=" + shellQuote(session) + " runtug _spawn-stdin"}, Input: string(b)})
	fmt.Fprint(a.Err, r.Err)
	if strings.Contains(r.Out, "OK pane=") {
		fmt.Fprint(a.Out, strings.TrimRight(r.Out, "\n")+" host="+c.name+"\n")
		return candResult{ok: true, res: r}
	}
	if r.Code == 0 {
		return candResult{stop: fail(75, "remote spawn returned no receipt")}
	}
	if r.Code == 255 {
		return candResult{unreachable: true, rc: r.Code, note: "unreachable", res: r}
	}
	if forced || slices.Contains([]int{2, 70, 74, 75, 76, 77, 79, 82}, r.Code) {
		return candResult{stop: check(r, "remote spawn"), res: r}
	}
	return candResult{rc: r.Code, note: fmt.Sprintf("refused rc=%d", r.Code), res: r}
}

func (a *App) place(ctx context.Context, s Spawn, brief string) (bool, map[string]any, error) {
	if s.Host != "auto" {
		return a.placeForced(ctx, s, brief)
	}
	return a.placeAuto(ctx, s, brief)
}

func (a *App) placeAuto(ctx context.Context, s Spawn, brief string) (bool, map[string]any, error) {
	task, _, e := a.taskPrecheck(ctx, s)
	if e != nil {
		return true, nil, e
	}
	waited := taskWaited(task, a.Now())
	pool := a.quotaPool(ctx, s)
	p, e := a.plan(ctx, s, pool, waited)
	if e != nil {
		return true, nil, e
	}
	var repo *repoResolution
	repoDone := false
	resolveRepo := func() *repoResolution {
		if !repoDone {
			repoDone = true
			repo = a.localRepo(ctx, s.CWD)
			if repo != nil {
				p.repo = repo.name
			}
		}
		return repo
	}
	spawn := func(c *placeCandidate) candResult {
		if s.DryRun {
			return a.spawnRemote(ctx, s, brief, c, resolveRepo(), false)
		}
		if mappedCWD(c.cfg["cwd_map"], s.CWD) == "" {
			resolveRepo()
		}
		return a.spawnRemote(ctx, s, brief, c, repo, false)
	}
	emit := func(chosen string) map[string]any {
		rec := p.record(chosen)
		if !s.DryRun {
			if e := a.event(ctx, s.Job, "job.placement", rec); e != nil {
				a.warn("placement decision record failed: %v", e)
			}
		}
		return rec
	}
	var unreachable []string
	for _, c := range p.ordered {
		if c.local {
			return false, p.record(c.name), nil
		}
		res := spawn(c)
		c.outcome = res.note
		if res.ok {
			return true, emit(c.name), nil
		}
		if res.stop != nil {
			return true, nil, res.stop
		}
		if res.unreachable && c.wakeable {
			unreachable = append(unreachable, c.name)
		}
	}
	targets, reasons := a.wakeTargets(s, p, unreachable)
	for _, w := range targets {
		c := p.sleeper(w)
		if c == nil {
			c = &placeCandidate{name: w, cfg: a.Config.Hosts[w], wakeable: true}
		}
		p.wake = map[string]any{"host": w, "reasons": reasons, "ok": false}
		h := c.cfg
		req := WakeRequest{HoldMinutes: int(integer(h, "wake_hold", 10)), WaitSeconds: int(integer(h, "wake_wait", 90)), Reason: "runtug placement: " + strings.Join(reasons, ",")}
		var wakeErr error
		if a.Wake != nil {
			wakeErr = a.Wake.Wake(ctx, w, req)
		} else {
			wakeErr = fail(81, "no waker configured")
		}
		if wakeErr == nil {
			wakeErr = a.waitForNode(ctx, w, h, req.WaitSeconds)
		}
		if wakeErr != nil {
			p.wake["error"] = wakeErr.Error()
			continue
		}
		p.wake["ok"] = true
		res := spawn(c)
		c.outcome = res.note
		if res.ok {
			return true, emit(w), nil
		}
		if res.stop != nil {
			return true, nil, res.stop
		}
		if res.res.Code != 0 {
			return true, nil, check(res.res, "remote spawn")
		}
		return true, nil, fail(81, "woken host %s refused the spawn", w)
	}
	if len(targets) > 0 {
		return true, nil, fail(81, "wake target did not accept spawn")
	}
	if !boolean(a.Config.Local, "spawn", true) {
		return true, nil, fail(80, "local spawns are disabled on this host; no remote candidate completed")
	}
	return true, nil, fail(81, "no placement candidate accepted spawn; no local fallback")
}

func (a *App) placeForced(ctx context.Context, s Spawn, brief string) (bool, map[string]any, error) {
	tags, _ := parseTags(s.Tags)
	rec := map[string]any{"source": "forced", "tags": tags, "profile": s.Profile, "host": s.Host}
	if s.Host == "local" || s.Host == a.machine() {
		return false, rec, nil
	}
	h, ok := a.Config.Hosts[s.Host]
	if !ok {
		return true, nil, fail(70, "placement selected unconfigured host %s", s.Host)
	}
	emit := func() {
		if s.DryRun {
			return
		}
		if e := a.event(ctx, s.Job, "job.placement", rec); e != nil {
			a.warn("placement decision record failed: %v", e)
		}
	}
	c := placeCandidate{name: s.Host, cfg: h, wakeable: str(h, "wake") == "panewire", awake: true, eligible: true}
	repo := a.localRepo(ctx, s.CWD)
	res := a.spawnRemote(ctx, s, brief, &c, repo, true)
	if res.ok {
		emit()
		return true, rec, nil
	}
	if res.stop != nil {
		return true, nil, res.stop
	}
	if res.note == "repo_unmappable" {
		return true, nil, fail(70, "host %s cannot map the job cwd: no cwd_map override and no resolvable repo", s.Host)
	}
	if res.unreachable && c.wakeable {
		req := WakeRequest{HoldMinutes: int(integer(h, "wake_hold", 10)), WaitSeconds: int(integer(h, "wake_wait", 90)), Reason: "runtug: explicit host unreachable"}
		if a.Wake == nil {
			return true, nil, fail(81, "host wake unavailable")
		}
		if e := a.Wake.Wake(ctx, s.Host, req); e != nil {
			return true, nil, fail(81, "host wake failed: %v", e)
		}
		if e := a.waitForNode(ctx, s.Host, h, req.WaitSeconds); e != nil {
			return true, nil, e
		}
		res = a.spawnRemote(ctx, s, brief, &c, repo, true)
		if res.ok {
			emit()
			return true, rec, nil
		}
		if res.stop != nil {
			return true, nil, res.stop
		}
		if res.res.Code != 0 {
			return true, nil, check(res.res, "remote spawn")
		}
		return true, nil, fail(81, "host %s answered its wake but the spawn still failed: %s", s.Host, res.note)
	}
	if res.res.Code != 0 {
		return true, nil, check(res.res, "remote spawn")
	}
	return true, nil, fail(81, "host %s refused the spawn: %s", s.Host, res.note)
}

// quotaPool resolves which shared-quota pool the job's profile draws on, so
// placement can prefer hosts that measured the account. A catalog failure is
// not a placement failure: the spawn-time gate on the chosen host is the
// final admission check.
func (a *App) quotaPool(ctx context.Context, s Spawn) string {
	if a.Quota == nil {
		return ""
	}
	seat, ok := a.Policy.Profiles[s.Profile]
	if !ok {
		return ""
	}
	effort := s.Effort
	if effort == "" {
		effort = seat.Effort
	}
	l, e := a.Quota.Launch(ctx, QuotaRequest{Profile: seat.Canon, Effort: effort, Role: s.Role, OperatorRequest: s.OperatorRequest, RequestedBy: s.RequestedBy, Purpose: s.Purpose})
	if e != nil {
		return ""
	}
	return l.Pool
}

func taskWaited(t Task, now time.Time) time.Duration {
	if t.CreatedAt == "" {
		return 0
	}
	ts, e := time.Parse(time.RFC3339Nano, t.CreatedAt)
	if e != nil {
		return 0
	}
	if d := now.Sub(ts); d > 0 {
		return d
	}
	return 0
}

func parseHubHosts(raw string) ([]HostView, error) {
	var p struct {
		Candidates []json.RawMessage `json:"candidates"`
	}
	if e := json.Unmarshal([]byte(raw), &p); e != nil {
		return nil, fail(70, "invalid placement response")
	}
	var out []HostView
	for _, v := range p.Candidates {
		var c struct {
			Machine, Host, Target, Reason string
			Connected                     *bool
			LoadRatio                     *float64
		}
		var name string
		if json.Unmarshal(v, &name) != nil {
			if json.Unmarshal(v, &c) != nil {
				return nil, fail(70, "invalid placement candidate")
			}
			name = c.Machine
			if name == "" {
				name = c.Host
			}
			if name == "" {
				name = c.Target
			}
		}
		if !validID(name) {
			continue
		}
		hv := HostView{Name: name, Connected: true, LoadRatio: c.LoadRatio, Reason: c.Reason}
		if c.Connected != nil {
			hv.Connected = *c.Connected
		}
		out = append(out, hv)
	}
	return out, nil
}

func mappedCWD(raw any, cwd string) string {
	mapping, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	best, mapped := "", ""
	for from, v := range mapping {
		to, ok := v.(string)
		if !ok {
			continue
		}
		if cwd == from || strings.HasPrefix(cwd, from+"/") || strings.HasPrefix(cwd, from+".") {
			if len(from) > len(best) {
				best = from
				mapped = to + strings.TrimPrefix(cwd, from)
			}
		}
	}
	return mapped
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (a *App) spawnStdin(ctx context.Context) error {
	b, e := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if e != nil {
		return e
	}
	var env struct {
		Spawn Spawn
		Brief string
	}
	if e = decode(b, &env); e != nil {
		return e
	}
	if env.Spawn.Host != "local" {
		return fail(2, "delegation must target local")
	}
	f, e := os.CreateTemp("", "runtug-brief-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.WriteString(env.Brief); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	env.Spawn.Prompt = f.Name()
	env.Spawn.Delegated = true
	return a.Spawn(ctx, env.Spawn)
}
func (a *App) prepareBriefForHost(s Spawn, host, body string) (string, error) {
	copy := *a
	copy.Config = a.Config
	copy.Config.Local = map[string]any{}
	for k, v := range a.Config.Hosts[host] {
		copy.Config.Local[k] = v
	}
	copy.Config.Local["machine_id"] = host
	return copy.prepareBrief(s, body)
}
