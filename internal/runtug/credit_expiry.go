package runtug

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CreditBalance is a prepaid credit pool's latest hand-entered balance, read
// from pacelever's manual observation store (`pacelever manual list`), the
// same record its credit gate admits on. Nothing here is measured: the
// operator records the balance and its expiry by hand.
type CreditBalance struct {
	// LeftPct is the balance left, as a percentage of the credit total.
	LeftPct float64
	// BalanceUSD is the dollar balance when the entry records one.
	BalanceUSD *float64
	// HoursLeft is the time from the reading to the credit's expiry.
	HoursLeft float64
	// PacePctH is the observed spend, percent of the total per hour, from
	// the two newest balances; nil when no comparable pair exists.
	PacePctH *float64
}

// expiring reports whether the balance outlives its expiry at the observed
// pace, and its waste urgency: the balance left per hour to expiry, in the
// same unit as a subscription's unused share per hour to reset. An unknown
// pace counts as no observed spend, so the whole balance is at risk.
func (c *CreditBalance) expiring() (bool, float64) {
	if c == nil || c.HoursLeft <= 0 || c.LeftPct <= 0 {
		return false, 0
	}
	if c.PacePctH != nil && *c.PacePctH*c.HoursLeft >= c.LeftPct {
		return false, 0
	}
	return true, c.LeftPct / c.HoursLeft
}

// note renders the plan text for an expiring credit pool.
func (c *CreditBalance) note() string {
	s := fmt.Sprintf("credit_expiring days_left=%.1f balance_pct=%.1f", c.HoursLeft/24, c.LeftPct)
	if c.BalanceUSD != nil {
		s += fmt.Sprintf(" balance_usd=%.2f", *c.BalanceUSD)
	}
	return s
}

func (c *CreditBalance) record() map[string]any {
	r := map[string]any{"days_left": c.HoursLeft / 24, "balance_pct": c.LeftPct}
	if c.BalanceUSD != nil {
		r["balance_usd"] = *c.BalanceUSD
	}
	if c.PacePctH != nil {
		r["pace_pct_per_hour"] = *c.PacePctH
	}
	return r
}

type manualEntry struct {
	ID            string   `json:"manual_observation_id"`
	Event         string   `json:"event"`
	Pool          string   `json:"pool"`
	Window        string   `json:"window"`
	UsedPct       *float64 `json:"used_pct"`
	MeasuredAt    string   `json:"measured_at"`
	ExpiresAt     string   `json:"expires_at"`
	SupersedesRef *string  `json:"supersedes_ref"`
	Credit        *struct {
		RemainingUSD float64 `json:"remaining_usd"`
		TotalUSD     float64 `json:"total_usd"`
		ExpiresOn    string  `json:"expires_on"`
	} `json:"credit"`
}

// creditBalances reads the latest credit-window entry of every pool in
// pacelever's manual store, paced at now.
func (q CLIQuota) creditBalances(ctx context.Context, now time.Time) (map[string]*CreditBalance, error) {
	r := q.Runner.Run(ctx, Command{Name: q.Binary, Args: []string{"manual", "list"}})
	if e := check(r, "manual store"); e != nil {
		return nil, e
	}
	var store struct {
		Schema  string            `json:"schema"`
		History []manualEntry     `json:"history"`
		Latest  map[string]string `json:"latest"`
	}
	if e := json.Unmarshal([]byte(r.Out), &store); e != nil || store.Schema == "" {
		return nil, fail(exitConfig, "invalid manual store JSON")
	}
	byID := map[string]manualEntry{}
	for _, m := range store.History {
		byID[m.ID] = m
	}
	out := map[string]*CreditBalance{}
	for _, m := range store.History {
		if m.Window != "credit" || m.Event != "set" || store.Latest[m.Pool+":credit"] != m.ID || !pct(m.UsedPct) {
			continue
		}
		expires, e := creditExpiry(m)
		if e != nil {
			continue
		}
		c := &CreditBalance{LeftPct: 100 - *m.UsedPct, HoursLeft: expires.Sub(now).Hours()}
		if m.Credit != nil {
			usd := m.Credit.RemainingUSD
			c.BalanceUSD = &usd
		}
		if m.SupersedesRef != nil {
			c.PacePctH = creditPace(m, byID[*m.SupersedesRef], now)
		}
		out[m.Pool] = c
	}
	return out, nil
}

// creditExpiry is the credit's own expiry when the entry records dollars,
// else the entry's expiry, which pacelever sets to the credit's expiry.
func creditExpiry(m manualEntry) (time.Time, error) {
	s := m.ExpiresAt
	if m.Credit != nil && m.Credit.ExpiresOn != "" {
		s = m.Credit.ExpiresOn
	}
	if t, e := time.Parse("2006-01-02", s); e == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// creditPace derives the spend per hour from the newest pair of balances with
// pacelever's own limits (credit.go recentCreditBurn): the newer reading is at
// most a week old, the pair is at least an hour apart, and a top-up (the
// balance rising, or a changed total) leaves the pace unknown.
func creditPace(newer, older manualEntry, now time.Time) *float64 {
	if older.Window != "credit" || older.Pool != newer.Pool || !pct(older.UsedPct) {
		return nil
	}
	if newer.Credit != nil && older.Credit != nil && newer.Credit.TotalUSD != older.Credit.TotalUSD {
		return nil
	}
	at, e1 := time.Parse(time.RFC3339Nano, newer.MeasuredAt)
	prev, e2 := time.Parse(time.RFC3339Nano, older.MeasuredAt)
	if e1 != nil || e2 != nil || now.Sub(at) > 7*24*time.Hour || now.Before(at) {
		return nil
	}
	hours := at.Sub(prev).Hours()
	if hours < 1 || *newer.UsedPct < *older.UsedPct {
		return nil
	}
	pace := (*newer.UsedPct - *older.UsedPct) / hours
	return &pace
}

func pct(v *float64) bool { return v != nil && *v >= 0 && *v <= 100 }
