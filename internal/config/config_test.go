package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestExplicitEnvironmentMatchesRuntimeWithoutChangingProcess(t *testing.T) {
	values := map[string]string{"DUJIAO_BASE_URL": "https://store.example.net/", "DUJIAO_CHANNEL_KEY": "channel-key", "DUJIAO_CHANNEL_SECRET": "channel-secret", "BOT_REDIS_URL": "redis://127.0.0.1:6379/0", "BOT_SESSION_ENCRYPTION_KEY": "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", "BOT_DEFAULT_LOCALE": "en-US", "BOT_LOG_LEVEL": "WARN", "BOT_ADMIN_USER_IDS": "42,43", "BOT_RATE_WRITE_PER_MINUTE": "9"}
	for k, v := range values {
		t.Setenv(k, v)
	}
	process, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := LoadFrom(func(k string) string { return os.Getenv(k) })
	if err != nil || !reflect.DeepEqual(process, explicit) {
		t.Fatal("explicit loader changed runtime behavior")
	}
	legacyHash := sha256.Sum256([]byte(explicit.DujiaoBaseURL + "\x00" + explicit.DujiaoChannelKey))
	if explicit.SessionPrefix() != fmt.Sprintf("dujiao-bot:v1:{%x}", legacyHash[:16]) {
		t.Fatal("Redis namespace changed")
	}
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			cfg, err := LoadFrom(func(k string) string {
				if k == "DUJIAO_CHANNEL_KEY" {
					return "independent-key"
				}
				return os.Getenv(k)
			})
			if err != nil || cfg.DujiaoChannelKey != "independent-key" {
				t.Error("explicit configuration failed")
			}
		})
	}
	wg.Wait()
	if os.Getenv("DUJIAO_CHANNEL_KEY") != "channel-key" {
		t.Fatal("explicit loader mutated the process")
	}
}

func TestBroadcastConfigValidation(t *testing.T) {
	t.Setenv("DUJIAO_BASE_URL", "https://shop.example.com")
	t.Setenv("DUJIAO_CHANNEL_KEY", "key")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "secret")
	t.Setenv("BOT_BROADCAST_INTERVAL", "")
	t.Setenv("BOT_BROADCAST_MAX_RECIPIENTS", "")
	if cfg, err := Load(); err != nil || cfg.BroadcastInterval != time.Second || cfg.BroadcastMaxRecipients != 1000 {
		t.Fatal("broadcast defaults", cfg, err)
	}
	for _, value := range []string{"0s", "999ms", "11s", "NaN"} {
		t.Setenv("BOT_BROADCAST_INTERVAL", value)
		if _, err := Load(); err == nil {
			t.Fatal("unsafe interval", value)
		}
	}
	t.Setenv("BOT_BROADCAST_INTERVAL", "2s")
	for _, value := range []string{"0", "5001", "NaN"} {
		t.Setenv("BOT_BROADCAST_MAX_RECIPIENTS", value)
		if _, err := Load(); err == nil {
			t.Fatal("unsafe audience limit", value)
		}
	}
	t.Setenv("BOT_BROADCAST_MAX_RECIPIENTS", "50")
	if cfg, err := Load(); err != nil || cfg.BroadcastInterval != 2*time.Second || cfg.BroadcastMaxRecipients != 50 {
		t.Fatal("valid broadcast settings", err)
	}
}

func TestLoadRequiresDujiaoCredentials(t *testing.T) {
	t.Setenv("DUJIAO_BASE_URL", "")
	t.Setenv("DUJIAO_CHANNEL_KEY", "")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestOperationsConfigValidation(t *testing.T) {
	t.Setenv("DUJIAO_BASE_URL", "https://shop.example.com")
	t.Setenv("DUJIAO_CHANNEL_KEY", "key")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "secret")
	t.Setenv("BOT_ADMIN_USER_IDS", "42,43,42")
	t.Setenv("BOT_AUDIT_FILE", "/var/lib/dujiao-bot/audit.jsonl")
	t.Setenv("BOT_RATE_USER_PER_MINUTE", "20")
	cfg, err := Load()
	if err != nil || len(cfg.AdminUserIDs) != 2 || cfg.RatePolicy.User.Limit != 20 || cfg.RatePolicy.Write.Limit != 10 || cfg.AuditFile == "" {
		t.Fatal("bad operations defaults/config", err)
	}
	for _, raw := range []string{"0", "-1", "100001", "NaN", "1.5"} {
		t.Setenv("BOT_RATE_USER_PER_MINUTE", raw)
		if _, err := Load(); err == nil {
			t.Fatal("invalid limit accepted", raw)
		}
	}
	t.Setenv("BOT_RATE_USER_PER_MINUTE", "60")
	t.Setenv("BOT_ADMIN_USER_IDS", "-100123")
	if _, err := Load(); err == nil {
		t.Fatal("group/public admin destination accepted")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DUJIAO_BASE_URL", "https://shop.example.com/")
	t.Setenv("DUJIAO_CHANNEL_KEY", "key")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "secret")
	t.Setenv("BOT_DEFAULT_LOCALE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DujiaoBaseURL != "https://shop.example.com" {
		t.Fatalf("unexpected base URL: %s", cfg.DujiaoBaseURL)
	}
	if cfg.DefaultLocale != "zh-CN" {
		t.Fatalf("unexpected locale: %s", cfg.DefaultLocale)
	}
}

func TestRedisRequiresIndependentEncryptionKey(t *testing.T) {
	t.Setenv("DUJIAO_BASE_URL", "https://shop.example.com")
	t.Setenv("DUJIAO_CHANNEL_KEY", "key")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "secret")
	t.Setenv("BOT_REDIS_URL", "redis://redis:6379/0")
	for _, key := range []string{"", "short", "0123456789abcdef", "zz23456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		t.Setenv("BOT_SESSION_ENCRYPTION_KEY", key)
		if _, err := Load(); err == nil {
			t.Fatal("accepted missing/invalid encryption key")
		}
	}
	t.Setenv("BOT_SESSION_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if cfg, err := Load(); err != nil || cfg.RedisURL != "redis://redis:6379/0" {
		t.Fatal("valid Redis configuration rejected", err)
	}
}
