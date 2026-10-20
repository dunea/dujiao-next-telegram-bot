package setup

import (
	"encoding/hex"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dunea/dujiao-next-telegram-bot/internal/config"
	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	"github.com/redis/go-redis/v9"
)

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code"`
	Line   int    `json:"line,omitempty"`
}
type Report struct {
	Schema           int     `json:"schema"`
	Operation        string  `json:"operation"`
	Status           string  `json:"status"`
	Source           string  `json:"source,omitempty"`
	Profile          string  `json:"profile,omitempty"`
	ConfigurationOK  bool    `json:"configuration_ok"`
	NetworkChecks    string  `json:"network_checks"`
	SessionNamespace string  `json:"session_namespace,omitempty"`
	Checks           []Check `json:"checks"`
}

func (r *Report) add(name, status, code string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Code: code})
	if status == "failed" {
		r.ConfigurationOK = false
	}
}
func effective(values map[string]string, profile string) map[string]string {
	copy := make(map[string]string, len(values)+2)
	for k, v := range values {
		copy[k] = strings.TrimSpace(v)
	}
	if profile == "compose-redis" {
		copy["BOT_REDIS_URL"] = "redis://redis:6379/0"
		copy["BOT_AUDIT_FILE"] = "/var/lib/dujiao-bot/audit.jsonl"
	}
	return copy
}
func httpAddress(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if u.Port() != "" {
		n, err := strconv.Atoi(u.Port())
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}
func placeholder(value string) bool {
	switch strings.ToLower(value) {
	case "your-channel-key", "your-channel-secret", "change-me", "changeme", "integration-key", "integration-secret", "demo-key", "demo-secret":
		return true
	}
	return false
}

// Diagnose is offline. A successful report does not imply merchant credentials,
// Telegram access, Redis contents, disk persistence or payment acceptance work.
func Diagnose(values map[string]string, profile, source string) Report {
	r := Report{Schema: 1, Operation: "check", Status: "checked", Source: source, Profile: profile, ConfigurationOK: true, NetworkChecks: "not_run", Checks: []Check{}}
	if profile != "runtime" && profile != "compose-redis" {
		r.Profile = "invalid"
		r.add("profile", "failed", "unsupported_profile")
		return r
	}
	v := effective(values, profile)
	for _, name := range []string{"DUJIAO_BASE_URL", "DUJIAO_CHANNEL_KEY", "DUJIAO_CHANNEL_SECRET"} {
		if strings.TrimSpace(v[name]) == "" {
			r.add(name, "failed", "required_value_missing")
		} else if placeholder(v[name]) {
			r.add(name, "failed", "public_placeholder_rejected")
		}
	}
	for _, name := range []string{"DUJIAO_BASE_URL", "TELEGRAM_API_URL"} {
		if v[name] == "" {
			continue
		}
		if !httpAddress(v[name]) {
			r.add(name, "failed", "invalid_http_address")
			continue
		}
		u, _ := url.Parse(v[name])
		if u.Hostname() == "shop.example.com" || strings.HasSuffix(u.Hostname(), ".invalid") {
			r.add(name, "failed", "example_address_rejected")
		}
		if u.Scheme == "http" {
			r.add(name, "warning", "plain_http_requires_trusted_network")
		}
		if name == "TELEGRAM_API_URL" {
			r.add(name, "warning", "custom_endpoint_not_checked")
		}
	}
	if v["TELEGRAM_BOT_TOKEN"] == "" {
		r.add("TELEGRAM_BOT_TOKEN", "warning", "upstream_token_not_checked")
	} else {
		id, token, ok := strings.Cut(v["TELEGRAM_BOT_TOKEN"], ":")
		n, err := strconv.ParseInt(id, 10, 64)
		if !ok || err != nil || n <= 0 || token == "" || strings.ContainsAny(token, " \t\r\n:") {
			r.add("TELEGRAM_BOT_TOKEN", "failed", "invalid_token_shape")
		}
	}
	if v["BOT_REDIS_URL"] == "" {
		r.add("sessions", "warning", "memory_has_no_restart_protection")
	} else {
		if _, err := redis.ParseURL(v["BOT_REDIS_URL"]); err != nil {
			r.add("BOT_REDIS_URL", "failed", "invalid_redis_address")
		}
		key, err := hex.DecodeString(v["BOT_SESSION_ENCRYPTION_KEY"])
		if err != nil || len(key) != 32 {
			r.add("BOT_SESSION_ENCRYPTION_KEY", "failed", "independent_64_hex_key_required")
		} else if strings.EqualFold(v["BOT_SESSION_ENCRYPTION_KEY"], v["DUJIAO_CHANNEL_SECRET"]) || v["BOT_SESSION_ENCRYPTION_KEY"] == strings.Repeat("0", 64) || strings.EqualFold(v["BOT_SESSION_ENCRYPTION_KEY"], strings.Repeat("0123456789abcdef", 4)) {
			r.add("BOT_SESSION_ENCRYPTION_KEY", "failed", "public_or_reused_key_rejected")
		}
		r.add("BOT_REDIS_URL", "warning", "transport_and_auth_not_checked")
		r.add("redis_data", "warning", "contents_and_durability_not_checked")
	}
	if profile == "compose-redis" && values["BOT_REDIS_URL"] != "" && values["BOT_REDIS_URL"] != v["BOT_REDIS_URL"] {
		r.add("BOT_REDIS_URL", "warning", "compose_overlay_overrides_file")
	}
	if v["BOT_ADMIN_USER_IDS"] == "" {
		r.add("BOT_ADMIN_USER_IDS", "warning", "admin_notifications_not_configured")
	} else if _, err := operations.ParseAdminIDs(v["BOT_ADMIN_USER_IDS"]); err != nil {
		r.add("BOT_ADMIN_USER_IDS", "failed", "private_numeric_ids_required")
	}
	if v["BOT_AUDIT_FILE"] == "" {
		r.add("BOT_AUDIT_FILE", "warning", "persistent_audit_not_configured")
	}
	r.add("deployment", "warning", "ports_audit_access_and_compose_project_not_verified")
	names := make([]string, 0, len(variableNames()))
	for name := range variableNames() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := v[name]
		if !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			r.add(name, "failed", "invalid_text_value")
		}
		if strings.HasPrefix(name, "BOT_RATE_") || name == "BOT_BROADCAST_MAX_RECIPIENTS" {
			if value == "" {
				continue
			}
			n, err := strconv.Atoi(value)
			upper := 100000
			if name == "BOT_BROADCAST_MAX_RECIPIENTS" {
				upper = 5000
			}
			if err != nil || n < 1 || n > upper {
				r.add(name, "failed", "out_of_range")
			}
		}
	}
	for _, name := range []string{"BOT_POLL_TIMEOUT", "BOT_SHUTDOWN_TIMEOUT", "BOT_BROADCAST_INTERVAL"} {
		if v[name] == "" {
			continue
		}
		d, err := time.ParseDuration(v[name])
		if err != nil || d <= 0 || name == "BOT_BROADCAST_INTERVAL" && (d < time.Second || d > 10*time.Second) {
			r.add(name, "failed", "invalid_duration")
		}
	}
	if level := strings.ToLower(v["BOT_LOG_LEVEL"]); level != "" && level != "debug" && level != "info" && level != "warn" && level != "error" {
		r.add("BOT_LOG_LEVEL", "failed", "invalid_log_level")
	}
	if listen := v["BOT_HTTP_LISTEN"]; listen != "" {
		_, port, err := net.SplitHostPort(listen)
		n, parseErr := strconv.Atoi(port)
		if err != nil || parseErr != nil || n < 1 || n > 65535 {
			r.add("BOT_HTTP_LISTEN", "failed", "invalid_listen_address")
		}
	}
	cfg, err := config.LoadFrom(func(k string) string { return v[k] })
	if err != nil {
		r.add("runtime.config", "failed", "invalid_runtime_settings")
	} else {
		r.add("runtime.config", "passed", "validated_without_startup")
		if cfg.RedisURL != "" {
			r.SessionNamespace = cfg.SessionPrefix()
		}
	}
	return r
}

// Compare refuses silent changes to safety-critical storage scope and keys.
// Even equal values do not prove a Redis data set, volume or snapshot is equal.
func Compare(r *Report, current, previous map[string]string) {
	a, b := effective(current, r.Profile), effective(previous, r.Profile)
	if old := Diagnose(previous, r.Profile, "comparison"); !old.ConfigurationOK {
		r.add("comparison", "failed", "previous_configuration_invalid")
		return
	}
	for _, name := range []string{"DUJIAO_BASE_URL", "DUJIAO_CHANNEL_KEY", "BOT_REDIS_URL", "BOT_SESSION_ENCRYPTION_KEY", "COMPOSE_PROJECT_NAME"} {
		left, right := strings.TrimSpace(a[name]), strings.TrimSpace(b[name])
		if name == "DUJIAO_BASE_URL" {
			left, right = strings.TrimRight(left, "/"), strings.TrimRight(right, "/")
		}
		equal := left == right
		if name == "BOT_SESSION_ENCRYPTION_KEY" {
			equal = strings.EqualFold(left, right)
		}
		if !equal {
			r.add("stable."+name, "failed", "changed_requires_manual_review")
		} else {
			r.add("stable."+name, "passed", "unchanged")
		}
	}
	r.add("comparison.data", "warning", "volumes_snapshots_and_pending_operations_not_checked")
}
