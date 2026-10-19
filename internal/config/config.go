package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	DujiaoBaseURL       string
	DujiaoChannelKey    string
	DujiaoChannelSecret string
	TelegramBotToken    string
	TelegramAPIURL      string
	DefaultLocale       string
	PollTimeout         time.Duration
	HTTPListen          string
	ShutdownTimeout     time.Duration
	LogLevel            string
}

func Load() (Config, error) {
	cfg := Config{
		DujiaoBaseURL:       strings.TrimRight(strings.TrimSpace(os.Getenv("DUJIAO_BASE_URL")), "/"),
		DujiaoChannelKey:    strings.TrimSpace(os.Getenv("DUJIAO_CHANNEL_KEY")),
		DujiaoChannelSecret: strings.TrimSpace(os.Getenv("DUJIAO_CHANNEL_SECRET")),
		TelegramBotToken:    strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		TelegramAPIURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("TELEGRAM_API_URL")), "/"),
		DefaultLocale:       envOr("BOT_DEFAULT_LOCALE", "zh-CN"),
		HTTPListen:          envOr("BOT_HTTP_LISTEN", ":8444"),
		LogLevel:            strings.ToLower(envOr("BOT_LOG_LEVEL", "info")),
	}

	var err error
	if cfg.PollTimeout, err = durationEnv("BOT_POLL_TIMEOUT", 30*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = durationEnv("BOT_SHUTDOWN_TIMEOUT", 10*time.Second); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var missing []string
	if c.DujiaoBaseURL == "" {
		missing = append(missing, "DUJIAO_BASE_URL")
	}
	if c.DujiaoChannelKey == "" {
		missing = append(missing, "DUJIAO_CHANNEL_KEY")
	}
	if c.DujiaoChannelSecret == "" {
		missing = append(missing, "DUJIAO_CHANNEL_SECRET")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	switch c.DefaultLocale {
	case "zh-CN", "zh-TW", "en-US":
	default:
		return errors.New("BOT_DEFAULT_LOCALE must be one of zh-CN, zh-TW, en-US")
	}
	if c.PollTimeout <= 0 {
		return errors.New("BOT_POLL_TIMEOUT must be positive")
	}
	if c.ShutdownTimeout <= 0 {
		return errors.New("BOT_SHUTDOWN_TIMEOUT must be positive")
	}
	return nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return value, nil
}
