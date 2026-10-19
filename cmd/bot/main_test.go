package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

func TestVersionCommandDoesNotRequireCredentials(t *testing.T) {
	t.Setenv("DUJIAO_BASE_URL", "")
	t.Setenv("DUJIAO_CHANNEL_KEY", "")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "")
	for _, flag := range []string{"--version", "-version"} {
		var output bytes.Buffer
		if err := command([]string{flag}, &output); err != nil || output.String() != "0.4.0\n" {
			t.Fatal("version check loaded configuration or returned wrong version", err, output.String())
		}
	}
}

func TestUnknownArgumentsDoNotStartBot(t *testing.T) {
	var output bytes.Buffer
	if err := command([]string{"--unknown"}, &output); err == nil || output.Len() != 0 {
		t.Fatal("unknown arguments should return usage before startup")
	}
}

type healthNotifier struct{ err error }

func (n healthNotifier) SessionHealth() error                                        { return n.err }
func (n healthNotifier) Notify(context.Context, int64, string, callback.Event) error { return nil }

func TestHealthReflectsSessionAvailability(t *testing.T) {
	for _, err := range []error{nil, session.ErrUnavailable} {
		server := httpServer(":0", "key", "secret", healthNotifier{err: err}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		want := http.StatusOK
		if err != nil {
			want = http.StatusServiceUnavailable
		}
		if response.Code != want {
			t.Fatal("health status", response.Code, "want", want)
		}
	}
}
