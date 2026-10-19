package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompatibilityCommandSafeReportAndExitCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status_code":0,"data":{"config":{"enabled":true,"bot_token":"PRIVATE-TOKEN"},"config_version":44,"items":[]}}`)
	}))
	defer server.Close()
	env := func(name string) string {
		switch name {
		case "DUJIAO_BASE_URL":
			return server.URL
		case "DUJIAO_CHANNEL_KEY":
			return "key"
		case "DUJIAO_CHANNEL_SECRET":
			return "secret"
		default:
			t.Fatal("read unrelated credential", name)
			return ""
		}
	}
	var out, errOut bytes.Buffer
	if status := run(nil, &out, &errOut, env); status != 0 || strings.Contains(out.String(), "PRIVATE") || !strings.Contains(out.String(), `"upstream_version":"unknown"`) {
		t.Fatal("bad report", status, out.String(), errOut.String())
	}
	for _, args := range [][]string{{"-user-id", "-1"}, {"-timeout", "0s"}, {"extra"}} {
		if status := run(args, &bytes.Buffer{}, &bytes.Buffer{}, env); status != 2 {
			t.Fatal("invalid argument accepted", args, status)
		}
	}
	if status := run(nil, &bytes.Buffer{}, &bytes.Buffer{}, func(string) string { return "" }); status != 2 {
		t.Fatal("missing credentials accepted")
	}
}

func TestCompatibilityMatrixIsOffline(t *testing.T) {
	var out bytes.Buffer
	status := run([]string{"-matrix"}, &out, &bytes.Buffer{}, func(string) string { t.Fatal("offline matrix read credentials"); return "" })
	if status != 0 || !strings.Contains(out.String(), "41b86260dc9a60269526057eed8d915844ceb3a3") {
		t.Fatal("missing pinned matrix", out.String())
	}
	if status := run([]string{"-matrix", "-verify-upstream"}, &bytes.Buffer{}, &bytes.Buffer{}, func(string) string { return "" }); status != 2 {
		t.Fatal("conflicting modes accepted")
	}
}
