package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

func TestSetupCommandsDoNotStartBot(t *testing.T) {
	t.Setenv("DUJIAO_BASE_URL", "")
	t.Setenv("DUJIAO_CHANNEL_KEY", "")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "")
	path := filepath.Join(t.TempDir(), "new.env")
	var out bytes.Buffer
	if err := command([]string{"setup", "init", "--env-file", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"status":"created"`) {
		t.Fatal("setup route was not invoked")
	}
	before, _ := os.ReadFile(path)
	out.Reset()
	if err := command([]string{"setup", "check", "--env-file", path, "--profile", "compose-redis"}, &out); err == nil {
		t.Fatal("unfinished setup passed")
	}
	if !strings.Contains(out.String(), `"network_checks":"not_run"`) {
		t.Fatal("missing explicit offline scope")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("setup check replaced key")
	}
}

func TestVersionCommandDoesNotRequireCredentials(t *testing.T) {
	t.Setenv("DUJIAO_BASE_URL", "")
	t.Setenv("DUJIAO_CHANNEL_KEY", "")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "")
	for _, flag := range []string{"--version", "-version"} {
		var output bytes.Buffer
		if err := command([]string{flag}, &output); err != nil || output.String() != version+"\n" {
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
