package compatibility

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestCompatibilityProbeReadOnlySignedAndSanitized(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		stamp, _ := strconv.ParseInt(r.Header.Get("Dujiao-Next-Channel-Timestamp"), 10, 64)
		if r.Method != "GET" || !dujiao.Verify("secret", r.Method, r.URL.Path, stamp, nil, r.Header.Get("Dujiao-Next-Channel-Signature")) {
			t.Error("unsigned or non-GET probe")
		}
		body := `{"items":[],"future_field":true}`
		switch r.URL.Path {
		case "/api/v1/channel/telegram/config":
			body = `{"config":{"enabled":true,"bot_token":"PRIVATE-BOT-TOKEN"},"config_version":9321}`
		case "/api/v1/channel/wallet":
			body = `{"balance":"1.00","currency":"CNY"}`
		case "/api/v1/channel/affiliate/dashboard":
			body = `{"opened":true,"min_withdraw_amount":"10.00"}`
		}
		fmt.Fprintf(w, `{"status_code":0,"data":%s}`, body)
	}))
	defer server.Close()
	client, _ := dujiao.NewReadOnlyClient(server.URL, "key", "secret", time.Second)
	report := Probe(context.Background(), client, "42")
	encoded, _ := json.Marshal(report)
	if !report.ReadChecksOK || len(report.Checks) != 8 || requests.Load() != 8 || report.UpstreamVersion != "unknown" || report.ConfigRevision != 9321 || report.WriteChecks != "not_run" || strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), server.URL) {
		t.Fatal("unsafe/misleading report", string(encoded))
	}
}
func TestCompatibilityProbeErrorClassesAndNoRetry(t *testing.T) {
	for _, tc := range []struct {
		code   int
		status string
	}{{404, "missing"}, {501, "missing"}, {401, "denied"}, {403, "denied"}, {429, "throttled"}, {500, "unavailable"}, {307, "rejected"}} {
		t.Run(strconv.Itoa(tc.code), func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(tc.code)
				fmt.Fprint(w, "PRIVATE-UPSTREAM-ERROR")
			}))
			defer server.Close()
			client, _ := dujiao.NewReadOnlyClient(server.URL, "key", "secret", time.Second)
			report := Probe(context.Background(), client, "42")
			encoded, _ := json.Marshal(report)
			if report.ReadChecksOK || requests.Load() != 1 || report.Checks[0].Status != tc.status || strings.Contains(string(encoded), "PRIVATE") {
				t.Fatal("misclassified or retried", string(encoded))
			}
		})
	}
}
func TestCompatibilityOptionalMissingDoesNotMisreportCoreFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "affiliate") {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{"status_code":0,"data":{"config":{"enabled":true},"items":[]}}`)
	}))
	defer server.Close()
	client, _ := dujiao.NewReadOnlyClient(server.URL, "key", "secret", time.Second)
	report := Probe(context.Background(), client, "42")
	if !report.ReadChecksOK || report.Checks[5].Status != "missing" || report.WriteChecks != "not_run" {
		t.Fatal("optional route confused with full compatibility", report)
	}
}
func TestCompatibilityDisabledOrIncompleteConfigStopsProbe(t *testing.T) {
	for _, body := range []string{`{"status_code":0,"data":{"config":{"enabled":false}}}`, `{"status_code":0,"data":null}`, `{"status_code":0}`} {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); fmt.Fprint(w, body) }))
		client, _ := dujiao.NewReadOnlyClient(server.URL, "key", "secret", time.Second)
		report := Probe(context.Background(), client, "")
		server.Close()
		if report.ReadChecksOK || requests.Load() != 1 {
			t.Fatal("incomplete config treated as usable", report)
		}
	}
}

func TestCompatibilityIncompleteSuccessIsNotAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/telegram/config") {
			fmt.Fprint(w, `{"status_code":0,"data":{"config":{"enabled":true}}}`)
			return
		}
		fmt.Fprint(w, `{"status_code":0,"data":{}}`)
	}))
	defer server.Close()
	client, _ := dujiao.NewReadOnlyClient(server.URL, "key", "secret", time.Second)
	report := Probe(context.Background(), client, "")
	if report.ReadChecksOK || report.Checks[1].Status != "invalid_response" {
		t.Fatal("empty success misreported as usable", report)
	}
}
func TestCompatibilitySourceVerificationFailsClosed(t *testing.T) {
	for _, mode := range []string{"matched", "hash_mismatch", "missing", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			body := "reviewed source"
			expected := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/commit/source.go" {
					t.Error("wrong source request")
				}
				switch mode {
				case "missing":
					w.WriteHeader(404)
					return
				case "hash_mismatch":
					body = "changed"
				case "oversized":
					body = strings.Repeat("x", (1<<20)+1)
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			results := verifySources(context.Background(), server.Client(), server.URL+"/", []Version{{Version: "test", Commit: "commit", Sources: []Source{{"source.go", expected}}}})
			want := mode
			if mode == "missing" || mode == "oversized" {
				want = "fetch_error"
			}
			if len(results) != 1 || results[0].Status != want {
				t.Fatal("source audit accepted invalid evidence", results)
			}
		})
	}
}
func TestCompatibilityMatrixPinsAndCopies(t *testing.T) {
	versions := Versions()
	if len(versions) != 3 || versions[0].ChannelInviteCode || versions[1].ChannelInviteCode || !versions[2].ChannelInviteCode {
		t.Fatal("incorrect invite-code boundary")
	}
	for _, version := range versions {
		if len(version.Commit) != 40 || len(version.Sources) != 8 {
			t.Fatal("missing pinned evidence", version.Version)
		}
		for _, source := range version.Sources {
			if len(source.SHA256) != 64 || !strings.HasPrefix(source.Path, "internal/") {
				t.Fatal("unbounded source", source)
			}
		}
	}
	versions[0].Sources[0].SHA256 = "mutated"
	if Versions()[0].Sources[0].SHA256 == "mutated" {
		t.Fatal("matrix aliases global state")
	}
}
