package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
)

type Config struct {
	DujiaoBaseURL          string
	DujiaoChannelKey       string
	DujiaoChannelSecret    string
	TelegramBotToken       string
	TelegramAPIURL         string
	DefaultLocale          string
	PollTimeout            time.Duration
	HTTPListen             string
	ShutdownTimeout        time.Duration
	LogLevel               string
	RedisURL               string
	SessionEncryptionKey   string
	AdminUserIDs           []int64
	AuditFile              string
	RatePolicy             ratelimit.Policy
	BroadcastInterval      time.Duration
	BroadcastMaxRecipients int
}

func Load() (Config, error) {
	cfg := Config{
		DujiaoBaseURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("DUJIAO_BASE_URL")), "/"),
		DujiaoChannelKey:     strings.TrimSpace(os.Getenv("DUJIAO_CHANNEL_KEY")),
		DujiaoChannelSecret:  strings.TrimSpace(os.Getenv("DUJIAO_CHANNEL_SECRET")),
		TelegramBotToken:     strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		TelegramAPIURL:       strings.TrimRight(strings.TrimSpace(os.Getenv("TELEGRAM_API_URL")), "/"),
		DefaultLocale:        envOr("BOT_DEFAULT_LOCALE", "zh-CN"),
		HTTPListen:           envOr("BOT_HTTP_LISTEN", ":8444"),
		LogLevel:             strings.ToLower(envOr("BOT_LOG_LEVEL", "info")),
		RedisURL:             strings.TrimSpace(os.Getenv("BOT_REDIS_URL")),
		SessionEncryptionKey: strings.TrimSpace(os.Getenv("BOT_SESSION_ENCRYPTION_KEY")),
		AuditFile:            strings.TrimSpace(os.Getenv("BOT_AUDIT_FILE")),
	}

	var err error
	if cfg.BroadcastInterval, err = durationEnv("BOT_BROADCAST_INTERVAL", time.Second); err != nil {
		return Config{}, err
	}
	if cfg.BroadcastInterval < time.Second || cfg.BroadcastInterval > 10*time.Second {
		return Config{}, errors.New("BOT_BROADCAST_INTERVAL must be between 1s and 10s")
	}
	cfg.BroadcastMaxRecipients = 1000
	if raw := strings.TrimSpace(os.Getenv("BOT_BROADCAST_MAX_RECIPIENTS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 5000 {
			return Config{}, errors.New("BOT_BROADCAST_MAX_RECIPIENTS must be between 1 and 5000")
		}
		cfg.BroadcastMaxRecipients = value
	}
	if cfg.AdminUserIDs, err = operations.ParseAdminIDs(os.Getenv("BOT_ADMIN_USER_IDS")); err != nil {
		return Config{}, err
	}
	cfg.RatePolicy = ratelimit.DefaultPolicy()
	for _, item := range []struct {
		key  string
		rule *ratelimit.Rule
	}{
		{"BOT_RATE_USER_PER_MINUTE", &cfg.RatePolicy.User},
		{"BOT_RATE_WRITE_PER_MINUTE", &cfg.RatePolicy.Write},
		{"BOT_RATE_WITHDRAWAL_PER_10_MINUTES", &cfg.RatePolicy.Withdrawal},
		{"BOT_RATE_CALLBACK_PER_MINUTE", &cfg.RatePolicy.Callback},
		{"BOT_RATE_HTTP_PER_MINUTE", &cfg.RatePolicy.HTTP},
		{"BOT_RATE_GLOBAL_PER_MINUTE", &cfg.RatePolicy.Global},
	} {
		if raw := strings.TrimSpace(os.Getenv(item.key)); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 100000 {
				return Config{}, fmt.Errorf("%s must be between 1 and 100000", item.key)
			}
			item.rule.Limit = value
		}
	}
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
	if c.RedisURL != "" {
		key, err := hex.DecodeString(c.SessionEncryptionKey)
		if err != nil || len(key) != 32 {
			return errors.New("BOT_SESSION_ENCRYPTION_KEY must be 64 hexadecimal characters when Redis is enabled")
		}
	}
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
