package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

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
