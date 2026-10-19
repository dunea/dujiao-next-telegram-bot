package config

import "testing"

func TestLoadRequiresDujiaoCredentials(t *testing.T) {
	t.Setenv("DUJIAO_BASE_URL", "")
	t.Setenv("DUJIAO_CHANNEL_KEY", "")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected validation error")
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
