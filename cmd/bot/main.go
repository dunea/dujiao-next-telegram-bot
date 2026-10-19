package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/config"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	telegramapp "github.com/dunea/dujiao-next-telegram-bot/internal/telegram"
)

const version = "0.3.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := newLogger(cfg.LogLevel)

	api, err := dujiao.NewClient(cfg.DujiaoBaseURL, cfg.DujiaoChannelKey, cfg.DujiaoChannelSecret, cfg.PollTimeout)
	if err != nil {
		return err
	}
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	botConfig, err := api.BotConfig(startupCtx)
	startupCancel()
	if err != nil {
		return fmt.Errorf("load bot config from Dujiao-Next: %w", err)
	}
	if !botConfig.Config.Enabled {
		return errors.New("Telegram bot is disabled in the Dujiao-Next channel configuration")
	}
	token := cfg.TelegramBotToken
	if token == "" {
		token = botConfig.Config.BotToken
	}
	defaultLocale := botConfig.Config.DefaultLocale
	if defaultLocale == "" {
		defaultLocale = cfg.DefaultLocale
	}
	app, err := telegramapp.New(token, api, botConfig.Config, defaultLocale, cfg.PollTimeout, cfg.TelegramAPIURL, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := httpServer(cfg.HTTPListen, cfg.DujiaoChannelKey, cfg.DujiaoChannelSecret, app, logger)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("health server listening", "address", cfg.HTTPListen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()
	go heartbeatLoop(ctx, logger, api, app)
	go func() {
		logger.Info("Telegram bot started", "version", version)
		app.Start(ctx)
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		stop()
		return fmt.Errorf("health server: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("shutdown health server", "error", err)
	}
	logger.Info("Telegram bot stopped")
	return nil
}

func heartbeatLoop(ctx context.Context, logger *slog.Logger, api *dujiao.Client, app *telegramapp.App) {
	send := func() {
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := api.Heartbeat(requestCtx, version, "long_polling", nil); err != nil {
			logger.Warn("send channel heartbeat", "error", err)
		}
		config, err := api.BotConfig(requestCtx)
		if err != nil {
			logger.Warn("refresh bot config", "error", err)
			return
		}
		app.UpdateConfig(config.Config)
	}
	send()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func httpServer(address, channelKey, channelSecret string, notifier callback.Notifier, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	callback.New(channelKey, channelSecret, notifier, logger).Register(mux)
	return &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
}

func newLogger(level string) *slog.Logger {
	var parsed slog.Level
	switch strings.ToLower(level) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}
