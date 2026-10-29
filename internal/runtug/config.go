package runtug

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config accepts the existing wrk hosts.toml surface. Capability booleans remain
// data so new harnesses do not require a configuration parser change.
type Config struct {
	HostOrder []string                  `toml:"-"`
	Local     map[string]any            `toml:"local"`
	Hub       map[string]any            `toml:"hub"`
	Hosts     map[string]map[string]any `toml:"hosts"`
	Artifacts map[string]any            `toml:"artifacts"`
	Placement map[string]any            `toml:"placement"`
	Quota     map[string]any            `toml:"quota"`
}

func expand(p string) string {
	if p == "~" {
		h, _ := os.UserHomeDir()
		return h
	}
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}
func configPath() string {
	if v := os.Getenv("WRK_HOSTS_CONFIG"); v != "" {
		return v
	}
	p := os.Getenv("XDG_CONFIG_HOME")
	if p == "" {
		p = expand("~/.config")
	}
	return filepath.Join(p, "wrk", "hosts.toml")
}
func LoadConfig(path string) (Config, error) {
	c := Config{}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return c, nil
	}
	if e != nil {
		return c, fail(70, "hosts config: %v", e)
	}
	if strings.Contains(string(b), "\ufeff") {
		return c, fail(70, "hosts config contains a BOM")
	}
	var raw map[string]any
	if e = toml.Unmarshal(b, &raw); e != nil {
		return c, fail(70, "hosts config: %v", e)
	}
	for k := range raw {
		if k != "local" && k != "hub" && k != "hosts" && k != "artifacts" && k != "placement" && k != "quota" {
			return c, fail(70, "unknown hosts table %q", k)
		}
	}
	if e = toml.Unmarshal(b, &c); e != nil {
		return c, fail(70, "hosts config: %v", e)
	}
	for _, m := range regexp.MustCompile(`(?m)^\s*\[hosts\.([A-Za-z0-9._-]+)\]\s*(?:#.*)?$`).FindAllStringSubmatch(string(b), -1) {
		c.HostOrder = append(c.HostOrder, m[1])
	}
	for _, k := range []string{"spawn", "pi_anthropic", "cmd_dsflash41", "heavy_load_wait"} {
		if v, ok := c.Local[k]; ok {
			if _, ok = v.(bool); !ok {
				return c, fail(70, "local.%s must be a boolean", k)
			}
		}
	}
	for _, k := range []string{"heavy_max", "max_active", "shell_ready_timeout_ms", "fleet_usage_alert_gb"} {
		if v, ok := c.Local[k]; ok {
			if n, ok := v.(int64); !ok || n < 0 {
				return c, fail(70, "local.%s must be a nonnegative integer", k)
			}
		}
	}
	if v, ok := c.Hub["quota_gate"]; ok {
		if v != "local" || str(c.Hub, "hub_token_env") != "" {
			return c, fail(70, "hub.quota_gate accepts only local and contradicts hub_token_env")
		}
	}
	if str(c.Hub, "hub_token_env") != "" && str(c.Hub, "hub_url") == "" {
		return c, fail(70, "hub_token_env requires hub_url")
	}
	for name, h := range c.Hosts {
		if !validID(name) {
			return c, fail(70, "invalid host name")
		}
		if v := str(h, "ssh"); v != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._-]*$`).MatchString(v) {
			return c, fail(70, "SSH target must be an SSH config alias")
		}
		if v := str(h, "via"); v != "" && v != "ssh" && v != "hub" {
			return c, fail(70, "hosts.%s.via must be ssh or hub", name)
		}
		if e := validTags(h["tags"], "hosts."+name+".tags"); e != nil {
			return c, e
		}
		if v := str(h, "presence_id"); v != "" && !validID(v) {
			return c, fail(70, "hosts.%s.presence_id must be a machine ID", name)
		}
	}
	if e := validTags(c.Local["tags"], "local.tags"); e != nil {
		return c, e
	}
	if v := str(c.Local, "presence_id"); v != "" && !validID(v) {
		return c, fail(70, "local.presence_id must be a machine ID")
	}
	if v, ok := c.Placement["load_threshold"]; ok {
		if n, ok := v.(float64); !ok || n < 0 {
			if m, isInt := v.(int64); !isInt || m < 0 {
				return c, fail(70, "placement.load_threshold must be a nonnegative number")
			}
		}
	}
	for _, k := range []string{"wake_after_s", "presence_ttl_s", "fresh_ttl_s"} {
		for _, m := range []map[string]any{c.Placement, c.Quota} {
			if v, ok := m[k]; ok {
				if n, ok := v.(int64); !ok || n < 0 {
					return c, fail(70, "%s must be a nonnegative integer", k)
				}
			}
		}
	}
	if v := str(c.Quota, "url"); v != "" {
		u, e := url.Parse(v)
		if e != nil || u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
			return c, fail(70, "quota.url requires HTTPS")
		}
	}
	if v := str(c.Quota, "credential_env"); v != "" && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(v) {
		return c, fail(70, "quota.credential_env must name an environment variable")
	}
	if accounts, ok := c.Quota["accounts"].(map[string]any); ok {
		for pool, fp := range accounts {
			if !validID(pool) {
				return c, fail(70, "invalid quota account pool name")
			}
			if s, ok := fp.(string); !ok || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(s) {
				return c, fail(70, "quota.accounts.%s must be a 16-hex account fingerprint", pool)
			}
		}
	}
	if v, ok := c.Local["max_load_ratio"]; ok {
		switch n := v.(type) {
		case int64:
			if n < 0 {
				return c, typeError("local.max_load_ratio")
			}
		case float64:
			if n < 0 {
				return c, typeError("local.max_load_ratio")
			}
		default:
			return c, fail(70, "local.max_load_ratio must be a nonnegative number")
		}
	}
	if v, ok := c.Local["shell_ready_timeout_ms"]; ok {
		n := v.(int64)
		if n < 30000 || n > 300000 {
			return c, fail(70, "shell_ready_timeout_ms must be 30000..300000")
		}
	}
	return c, nil
}
func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func boolean(m map[string]any, k string, def bool) bool {
	v, ok := m[k].(bool)
	if !ok {
		return def
	}
	return v
}
func integer(m map[string]any, k string, def int64) int64 {
	v, ok := m[k].(int64)
	if !ok {
		return def
	}
	return v
}
func stringsValue(m map[string]any, k string) []string {
	var out []string
	switch v := m[k].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		out = strings.Split(v, ",")
	}
	return out
}
func (c Config) hubArgs(role string) []string {
	var a []string
	if v := str(c.Hub, "hub_url"); v != "" {
		a = append(a, "--hub-url", v)
	}
	token := str(c.Hub, "lanes_token_env")
	if token == "" && role == "builder" {
		token = str(c.Hub, "operator_token_env")
	}
	if token == "" {
		token = str(c.Hub, "hub_token_env")
	}
	if token != "" {
		a = append(a, "--hub-token-env", token)
	}
	if v := str(c.Hub, "hub_cf_env"); v != "" {
		a = append(a, "--hub-cf-env", v)
	}
	return a
}

var tagRule = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// isTailnetHost reports a literal Tailscale CGNAT address (100.64.0.0/10).
// Plain HTTP to it is allowed only with the explicit [quota]
// allow_plaintext_tailnet opt-in, mirroring pacelever's
// allow_plaintext_quota_share: the tailnet already encrypts the hop.
func isTailnetHost(h string) bool {
	ip := net.ParseIP(h)
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return ip != nil && ip.To4() != nil && cgnat.Contains(ip)
}

func validTags(v any, key string) error {
	if v == nil {
		return nil
	}
	for _, t := range stringsValue(map[string]any{"v": v}, "v") {
		if !tagRule.MatchString(t) {
			return fail(70, "%s contains an invalid tag %q", key, t)
		}
	}
	return nil
}
func typeError(key string) error          { return fail(70, "%s has an invalid type", key) }
func unmarshalTOML(b []byte, v any) error { return toml.Unmarshal(b, v) }
func (a *App) controlHubArgs(operator bool) []string {
	token := str(a.Config.Hub, "hub_token_env")
	if operator {
		token = firstNonempty(str(a.Config.Hub, "operator_token_env"), token)
	}
	args := []string{"--hub-url", str(a.Config.Hub, "hub_url"), "--hub-token-env", token}
	if v := str(a.Config.Hub, "hub_cf_env"); v != "" {
		args = append(args, "--hub-cf-env", v)
	}
	return args
}
