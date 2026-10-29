package runtug

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

type Launch struct {
	Catalog struct {
		Stale bool `json:"stale"`
	} `json:"catalog"`
	Profile string `json:"profile"`
	ModelID string `json:"model_id"`
	Effort  string `json:"effort"`
	Grade   string `json:"grade"`
	Gate    string `json:"gate"`
	Pool    string `json:"pool"`
	Family  string `json:"family"`
}

// QuotaRequest asks the quota CLI about one launch. Profile is the launch
// canon; GateProfile, when set, is the name its gate knows the profile by.
// AccountKey scopes only Gate to shared evidence for a remote host's account.
type QuotaRequest struct {
	Profile, Effort, Role, OperatorRequest, RequestedBy, Purpose, GateProfile string
	AccountKey                                                                string
}

// CatalogRow is one bench catalog rung: profile@effort with its operator-set
// grade, admission gate and quota pool. Retired rows are excluded by the
// selector.
type CatalogRow struct {
	Profile   string   `json:"profile"`
	Effort    string   `json:"effort"`
	Grade     string   `json:"grade"`
	ModelID   string   `json:"model_id"`
	Pool      string   `json:"pool"`
	Gate      string   `json:"gate"`
	Score     *float64 `json:"score"`
	RetiredAt *string  `json:"retired_at"`
}

// PoolView is one quota pool's status from the quota CLI's scopefuel.v1
// status document. Waste carries the subscription use-it-or-lose-it flag when
// the adapter reports verdicts; Blocking is the constraining used percentage
// (verdict.blocking_pct, else the highest account-scope bucket reading).
type PoolView struct {
	Status   string
	Stale    bool
	Class    string
	Waste    *bool
	Blocking *float64
}

// QuotaGate is deliberately independent of the scopefuel executable name.
// pacelever can implement this interface or the same CLI/JSON contract.
type QuotaGate interface {
	Launch(context.Context, QuotaRequest) (Launch, error)
	Gate(context.Context, QuotaRequest) (string, error)
	Catalog(context.Context) ([]CatalogRow, error)
	Pools(context.Context) (map[string]PoolView, error)
}
type CLIQuota struct {
	Runner Runner
	Binary string
}

func (q CLIQuota) Launch(ctx context.Context, r QuotaRequest) (Launch, error) {
	a := []string{"policy", "launch", r.Profile, "--json"}
	a = quotaArgs(a, r)
	result := q.Runner.Run(ctx, Command{Name: q.Binary, Args: a})
	if e := check(result, "quota catalog"); e != nil {
		return Launch{}, e
	}
	var l Launch
	if e := uniqueJSON([]byte(result.Out)); e != nil {
		return l, fail(70, "ambiguous quota catalog JSON: %v", e)
	}
	if e := json.Unmarshal([]byte(result.Out), &l); e != nil {
		return l, fail(70, "invalid quota catalog JSON: %v", e)
	}
	if l.Catalog.Stale {
		return l, fail(70, "quota catalog is stale")
	}
	return l, nil
}
func quotaArgs(a []string, r QuotaRequest) []string {
	for _, p := range [][2]string{{"--effort", r.Effort}, {"--role", r.Role}, {"--operator-request", r.OperatorRequest}, {"--requested-by", r.RequestedBy}, {"--purpose", r.Purpose}} {
		if p[1] != "" {
			a = append(a, p[0], p[1])
		}
	}
	return a
}
func (q CLIQuota) Gate(ctx context.Context, r QuotaRequest) (string, error) {
	name := r.Profile
	if r.GateProfile != "" {
		name = r.GateProfile
	}
	args := quotaArgs([]string{"gate", "-m", name}, r)
	if r.AccountKey != "" {
		args = append(args, "--account-key", r.AccountKey, "--json")
	}
	x := q.Runner.Run(ctx, Command{Name: q.Binary, Args: args})
	if x.Code == 5 {
		return "", fail(77, "quota role-denied (gate exit 5 → runtug exit 77): %s", x.Err)
	}
	if e := check(x, "quota gate"); e != nil {
		return "", e
	}
	if r.AccountKey != "" {
		// Older pacelever versions accept this flag but ignore it. Require an
		// account-bound receipt before trusting a successful remote judgement.
		var receipt struct {
			Schema     string `json:"schema"`
			AccountKey string `json:"account_key"`
			Pool       string `json:"provider_id"`
			OK         bool   `json:"ok"`
			ExitCode   *int   `json:"exit_code"`
		}
		pool, _, _ := strings.Cut(r.AccountKey, ":")
		if uniqueJSON([]byte(x.Out)) != nil || json.Unmarshal([]byte(x.Out), &receipt) != nil || receipt.Schema != "scopefuel.gate.v1" || receipt.AccountKey != r.AccountKey || receipt.Pool != pool || !receipt.OK || receipt.ExitCode == nil || *receipt.ExitCode != 0 {
			return "", fail(70, "quota gate did not confirm the target account")
		}
	}
	return x.Out, nil
}

// quotaJudgment binds a gate attempt to its host and, for shared evidence, the
// exact account passed to pacelever. Unknown evidence never invokes a local gate.
type quotaJudgment struct {
	Host       string `json:"host"`
	AccountKey string `json:"account_key,omitempty"`
	Source     string `json:"source"`
	Outcome    string `json:"outcome"`
	ExitCode   int    `json:"exit_code"`
}

func (a *App) judgeHost(ctx context.Context, r QuotaRequest, pool, host string, local bool, key string, acc SharedAccount) (quotaJudgment, error) {
	j := quotaJudgment{Host: host, Source: "local", Outcome: "unknown", ExitCode: 4}
	if !local {
		j.Source, j.AccountKey = "shared", key
		if key == "" || acc.Key != key || !acc.Observed || !acc.Fresh || acc.Degraded {
			return j, fail(4, "target account quota unavailable")
		}
		r.AccountKey = key
	}
	if a.Quota == nil {
		return j, fail(4, "quota gate unavailable")
	}
	gate, err := a.Quota.Gate(ctx, r)
	if err == nil {
		if got := gatePool(gate); pool != "" && got != pool && (got != "" || !local) {
			err = fail(70, "quota gate pool mismatch")
		}
	}
	j.Outcome, j.ExitCode = "admitted", 0
	if err != nil {
		j.Outcome, j.ExitCode = "refused", exitCode(err)
	}
	return j, err
}

// Catalog lists the bench catalog rungs. Newer quota CLIs serve a JSON
// envelope under --json; older ones only print the text table, so the text
// form is parsed as a fallback. A stale catalog is refused like the launch
// resolution: grades and models must come from fresh evidence.
func (q CLIQuota) Catalog(ctx context.Context) ([]CatalogRow, error) {
	r := q.Runner.Run(ctx, Command{Name: q.Binary, Args: []string{"bench", "catalog", "list", "--json"}})
	if r.Code != 0 {
		r = q.Runner.Run(ctx, Command{Name: q.Binary, Args: []string{"bench", "catalog", "list"}})
	}
	if e := check(r, "bench catalog"); e != nil {
		return nil, e
	}
	out := strings.TrimSpace(r.Out)
	if strings.HasPrefix(out, "{") {
		return parseCatalogJSON(out)
	}
	return parseCatalogText(out)
}
func parseCatalogJSON(out string) ([]CatalogRow, error) {
	var doc struct {
		Catalog []CatalogRow `json:"catalog"`
		Source  struct {
			Stale bool `json:"stale"`
		} `json:"source"`
	}
	if e := uniqueJSON([]byte(out)); e != nil {
		return nil, fail(70, "ambiguous bench catalog JSON: %v", e)
	}
	if e := json.Unmarshal([]byte(out), &doc); e != nil || doc.Catalog == nil {
		return nil, fail(70, "invalid bench catalog JSON")
	}
	if doc.Source.Stale {
		return nil, fail(70, "bench catalog is stale")
	}
	return doc.Catalog, nil
}

// parseCatalogText reads the table form: GRADE LABEL k=v... where LABEL is
// profile or profile@effort (a trailing @ means the default rung) and the
// "retired" word marks a withdrawn rung. Lines that do not open with a grade
// are headers or prose and skipped; a graded row missing its key fields is a
// malformed contract and refused rather than guessed.
var catalogAge = regexp.MustCompile(`\(age ([0-9.]+)([smhd])\)`)

func parseCatalogText(out string) ([]CatalogRow, error) {
	var rows []CatalogRow
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "catalog=") {
			if strings.Contains(line, "stale") {
				return nil, fail(70, "bench catalog is stale")
			}
			if m := catalogAge.FindStringSubmatch(line); m != nil {
				n, e := strconv.ParseFloat(m[1], 64)
				if e == nil && (m[2] == "d" || m[2] == "h" && n > 24) {
					return nil, fail(70, "bench catalog is stale")
				}
			}
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if _, ok := gradeOrder[f[0]]; !ok {
			continue
		}
		row := CatalogRow{Grade: f[0]}
		row.Profile, row.Effort, _ = strings.Cut(f[1], "@")
		for _, w := range f[2:] {
			k, v, ok := strings.Cut(w, "=")
			if !ok {
				if w == "retired" {
					row.RetiredAt = &w
				}
				continue
			}
			switch k {
			case "model":
				row.ModelID = v
			case "pool":
				row.Pool = v
			case "gate":
				row.Gate = v
			case "score":
				if v != "-" {
					if n, e := strconv.ParseFloat(v, 64); e == nil {
						row.Score = &n
					}
				}
			}
		}
		if row.ModelID == "" || row.Pool == "" || row.Gate == "" {
			return nil, fail(70, "bench catalog row incomplete: %s", line)
		}
		rows = append(rows, row)
	}
	if rows == nil {
		return nil, fail(70, "empty bench catalog")
	}
	return rows, nil
}

// Pools reads the quota CLI's scopefuel.v1 status document and reduces it to
// the per-pool signals the selection order needs. An absent verdict means the
// adapter does not report waste flags or a blocking percentage; the selector
// degrades those inputs rather than inventing them.
func (q CLIQuota) Pools(ctx context.Context) (map[string]PoolView, error) {
	r := q.Runner.Run(ctx, Command{Name: q.Binary, Args: []string{"--json"}})
	if e := check(r, "quota status"); e != nil {
		return nil, e
	}
	var doc struct {
		Schema    string `json:"schema"`
		Providers []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			Stale     bool   `json:"stale"`
			PoolClass string `json:"pool_class"`
			Verdict   *struct {
				Waste    *bool    `json:"waste"`
				Blocking *float64 `json:"blocking_pct"`
			} `json:"verdict"`
			Buckets []struct {
				UsedPct *float64 `json:"used_pct"`
				Scope   struct {
					Kind string `json:"kind"`
				} `json:"scope"`
			} `json:"buckets"`
		} `json:"providers"`
	}
	if e := json.Unmarshal([]byte(r.Out), &doc); e != nil || doc.Schema == "" {
		return nil, fail(70, "invalid quota status JSON")
	}
	// The status document can repeat a provider id (a stale cached row plus a
	// fresh measurement); usable rows win, and among them the strictest
	// readings accumulate.
	groups := map[string][]int{}
	for i, p := range doc.Providers {
		groups[p.ID] = append(groups[p.ID], i)
	}
	pools := map[string]PoolView{}
	for id, idx := range groups {
		var src []int
		for _, i := range idx {
			p := doc.Providers[i]
			if p.Status == "ok" && !p.Stale {
				src = append(src, i)
			}
		}
		if len(src) == 0 {
			src = idx
		}
		v := PoolView{}
		for n, i := range src {
			p := doc.Providers[i]
			if n == 0 {
				v.Status, v.Class = p.Status, p.PoolClass
			}
			v.Stale = v.Stale || p.Stale
			if p.Verdict != nil {
				if p.Verdict.Waste != nil && *p.Verdict.Waste {
					v.Waste = p.Verdict.Waste
				} else if v.Waste == nil {
					v.Waste = p.Verdict.Waste
				}
				if p.Verdict.Blocking != nil && (v.Blocking == nil || *p.Verdict.Blocking > *v.Blocking) {
					v.Blocking = p.Verdict.Blocking
				}
			}
			for _, b := range p.Buckets {
				if b.Scope.Kind == "account" && b.UsedPct != nil && (v.Blocking == nil || *b.UsedPct > *v.Blocking) {
					v.Blocking = b.UsedPct
				}
			}
		}
		pools[id] = v
	}
	return pools, nil
}
