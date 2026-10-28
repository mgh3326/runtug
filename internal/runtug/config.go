package runtug

import (
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
		if k != "local" && k != "hub" && k != "hosts" && k != "artifacts" {
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
