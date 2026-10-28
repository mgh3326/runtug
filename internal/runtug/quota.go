package runtug

import (
	"context"
	"encoding/json"
)

type Launch struct {
	Profile string `json:"profile"`
	ModelID string `json:"model_id"`
	Effort  string `json:"effort"`
	Grade   string `json:"grade"`
	Gate    string `json:"gate"`
	Pool    string `json:"pool"`
	Family  string `json:"family"`
}
type QuotaRequest struct{ Profile, Effort, Role, OperatorRequest, RequestedBy, Purpose string }

// QuotaGate is deliberately independent of the scopefuel executable name.
// pacelever can implement this interface or the same CLI/JSON contract.
type QuotaGate interface {
	Launch(context.Context, QuotaRequest) (Launch, error)
	Gate(context.Context, QuotaRequest) (string, error)
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
	if e := json.Unmarshal([]byte(result.Out), &l); e != nil {
		return l, fail(70, "invalid quota catalog JSON: %v", e)
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
	x := q.Runner.Run(ctx, Command{Name: q.Binary, Args: quotaArgs([]string{"gate", "-m", r.Profile}, r)})
	if x.Code == 5 {
		return "", fail(77, "quota role-denied (gate exit 5 → runtug exit 77): %s", x.Err)
	}
	if e := check(x, "quota gate"); e != nil {
		return "", e
	}
	return x.Out, nil
}
