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
