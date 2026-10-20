package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/alicebob/miniredis/v2"
	deployment "github.com/dunea/dujiao-next-telegram-bot"
)

func validEnvironment() map[string]string {
	return map[string]string{"DUJIAO_BASE_URL": "https://merchant.example.net", "DUJIAO_CHANNEL_KEY": "PRIVATE_CHANNEL_KEY", "DUJIAO_CHANNEL_SECRET": "PRIVATE_CHANNEL_SECRET", "TELEGRAM_BOT_TOKEN": "999001:PRIVATE_TELEGRAM_TOKEN", "BOT_REDIS_URL": "redis://localhost:6379/0", "BOT_SESSION_ENCRYPTION_KEY": strings.Repeat("a1b2c3d4", 8)}
}
func copyEnvironment(v map[string]string) map[string]string {
	out := map[string]string{}
	for k, val := range v {
		out[k] = val
	}
	return out
}
func containsCheck(r Report, name, code string) bool {
	for _, c := range r.Checks {
		if c.Name == name && c.Code == code {
			return true
		}
	}
	return false
}

func TestInitExclusivePrivateAndIndependent(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "first.env"), filepath.Join(dir, "second.env")
	for _, path := range []string{a, b} {
		if issue := InitEnv(path); issue != nil {
			t.Fatal(issue.Code)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("generated file is not private")
		}
	}
	first, issue := ReadEnv(a)
	if issue != nil {
		t.Fatal(issue.Code)
	}
	second, issue := ReadEnv(b)
	if issue != nil {
		t.Fatal(issue.Code)
	}
	if len(first["BOT_SESSION_ENCRYPTION_KEY"]) != 64 || first["BOT_SESSION_ENCRYPTION_KEY"] == second["BOT_SESSION_ENCRYPTION_KEY"] {
		t.Fatal("random keys were not independent")
	}
	if first["DUJIAO_CHANNEL_KEY"] != "" || first["DUJIAO_CHANNEL_SECRET"] != "" {
		t.Fatal("wizard invented merchant credentials")
	}
	before, _ := os.ReadFile(a)
	if issue := InitEnv(a); issue == nil || issue.Code != "new_file_required" {
		t.Fatal("existing env overwritten")
	}
	after, _ := os.ReadFile(a)
	if !bytes.Equal(before, after) {
		t.Fatal("existing key changed")
	}
	for _, body := range []string{deployment.EnvExample, string(before)} {
		if _, issue := ParseEnv([]byte(body)); issue != nil {
			t.Fatal("embedded defaults do not parse", issue.Code)
		}
	}
	if Diagnose(first, "compose-redis", "file").ConfigurationOK {
		t.Fatal("unedited template falsely reported ready")
	}
}

func TestInitRefusesSymlinksDirectoriesAndMissingParents(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("PRIVATE_DO_NOT_CHANGE"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	_ = os.Symlink(filepath.Join(dir, "absent"), dangling)
	for _, path := range []string{link, dangling, dir, filepath.Join(dir, "not-created", "env")} {
		if InitEnv(path) == nil {
			t.Fatal("unsafe create target accepted")
		}
	}
	body, _ := os.ReadFile(target)
	if string(body) != "PRIVATE_DO_NOT_CHANGE" {
		t.Fatal("symlink target modified")
	}
	if _, issue := ReadEnv(link); issue == nil {
		t.Fatal("symlink read accepted")
	}
}

func TestStrictEnvParser(t *testing.T) {
	for _, body := range []string{
		"export DUJIAO_CHANNEL_KEY=x", "UNKNOWN_PRIVATE_KEY=x", "DUJIAO_CHANNEL_KEY=a\nDUJIAO_CHANNEL_KEY=b", "DUJIAO_CHANNEL_KEY=${SECRET}", "DUJIAO_CHANNEL_KEY=$(command)", "DUJIAO_CHANNEL_KEY=x # comment", `DUJIAO_CHANNEL_KEY="unterminated`, `DUJIAO_CHANNEL_KEY="a\nb"`, "DUJIAO_CHANNEL_KEY=bad\x00value", "DUJIAO_CHANNEL_KEY='bad\x1bvalue'", "DUJIAO_CHANNEL_KEY=\xff", strings.Repeat("x", maxEnvSize+1),
	} {
		if _, issue := ParseEnv([]byte(body)); issue == nil {
			t.Fatal("ambiguous environment accepted")
		}
	}
	values, issue := ParseEnv([]byte("# comment\r\nDUJIAO_CHANNEL_KEY='literal$#value'\r\nDUJIAO_BASE_URL=\"https://shop.example.net\"\n"))
	if issue != nil || values["DUJIAO_CHANNEL_KEY"] != "literal$#value" {
		t.Fatal("literal quoting or CRLF changed")
	}
}

func TestReadRejectsPublicAndNonregularFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "public.env")
	_ = os.WriteFile(path, []byte("DUJIAO_CHANNEL_KEY=PRIVATE"), 0600)
	_ = os.Chmod(path, 0644)
	if _, issue := ReadEnv(path); issue == nil || issue.Code != "private_permissions_required" {
		t.Fatal("public secret file accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, issue := ReadEnv(fifo); issue == nil || issue.Code != "regular_file_required" {
		t.Fatal("nonregular input accepted")
	}
}

func TestConfigurationProfilesAndWarnings(t *testing.T) {
	v := validEnvironment()
	r := Diagnose(v, "runtime", "process")
	if !r.ConfigurationOK || r.NetworkChecks != "not_run" || r.SessionNamespace == "" {
		t.Fatal("valid offline configuration failed")
	}
	want := sha256.Sum256([]byte(v["DUJIAO_BASE_URL"] + "\x00" + v["DUJIAO_CHANNEL_KEY"]))
	if r.SessionNamespace != fmt.Sprintf("dujiao-bot:v1:{%x}", want[:16]) {
		t.Fatal("namespace differs from historical format")
	}
	noRedis := copyEnvironment(v)
	noRedis["BOT_REDIS_URL"] = ""
	if r := Diagnose(noRedis, "runtime", "file"); !r.ConfigurationOK || !containsCheck(r, "sessions", "memory_has_no_restart_protection") {
		t.Fatal("optional memory mode not explained")
	}
	delete(noRedis, "BOT_SESSION_ENCRYPTION_KEY")
	if Diagnose(noRedis, "compose-redis", "file").ConfigurationOK {
		t.Fatal("compose overlay hid missing encryption key")
	}
	if r := Diagnose(v, "compose-redis", "file"); !r.ConfigurationOK || !containsCheck(r, "BOT_REDIS_URL", "compose_overlay_overrides_file") {
		t.Fatal("compose precedence not explicit")
	}
	v["BOT_LOG_LEVEL"] = " WARN "
	if !Diagnose(v, "runtime", "process").ConfigurationOK {
		t.Fatal("checker differs from runtime normalization")
	}
}

func TestInvalidValuesAreRejectedWithoutDisclosure(t *testing.T) {
	for name, value := range map[string]string{
		"DUJIAO_BASE_URL":    "https://PRIVATE_USER:PRIVATE_PASSWORD@merchant.example.net/?PRIVATE_QUERY",
		"DUJIAO_CHANNEL_KEY": "integration-key", "TELEGRAM_BOT_TOKEN": "PRIVATE_INVALID_TOKEN",
		"BOT_REDIS_URL": "https://PRIVATE_REDIS_PASSWORD@invalid", "BOT_SESSION_ENCRYPTION_KEY": "PRIVATE_BAD_KEY",
		"BOT_ADMIN_USER_IDS": "-10001", "BOT_RATE_WRITE_PER_MINUTE": "PRIVATE_RATE", "BOT_POLL_TIMEOUT": "PRIVATE_DURATION",
		"BOT_BROADCAST_INTERVAL": "11s", "BOT_BROADCAST_MAX_RECIPIENTS": "5001", "BOT_HTTP_LISTEN": "PRIVATE_LISTEN",
		"BOT_DEFAULT_LOCALE": "PRIVATE_LOCALE", "BOT_LOG_LEVEL": "PRIVATE_LOG_LEVEL",
	} {
		t.Run(name, func(t *testing.T) {
			v := validEnvironment()
			v[name] = value
			r := Diagnose(v, "runtime", "file")
			if r.ConfigurationOK {
				t.Fatal("bad configuration passed")
			}
			body, _ := json.Marshal(r)
			if strings.Contains(string(body), "PRIVATE_") {
				t.Fatal("private values leaked")
			}
		})
	}
}

func TestCompareRefusesSafetyChangesAndAllowsEquivalentHex(t *testing.T) {
	old := validEnvironment()
	for _, name := range []string{"DUJIAO_BASE_URL", "DUJIAO_CHANNEL_KEY", "BOT_REDIS_URL", "BOT_SESSION_ENCRYPTION_KEY"} {
		v := copyEnvironment(old)
		switch name {
		case "DUJIAO_BASE_URL":
			v[name] = "https://other.example.net"
		case "DUJIAO_CHANNEL_KEY":
			v[name] = "OTHER_PRIVATE_KEY"
		case "BOT_REDIS_URL":
			v[name] = "redis://localhost:6379/1"
		default:
			v[name] = strings.Repeat("c1d2e3f4", 8)
		}
		r := Diagnose(v, "runtime", "file")
		Compare(&r, v, old)
		if r.ConfigurationOK || !containsCheck(r, "stable."+name, "changed_requires_manual_review") {
			t.Fatal("stable setting change ignored", name)
		}
	}
	v := copyEnvironment(old)
	v["DUJIAO_BASE_URL"] += "/"
	v["BOT_SESSION_ENCRYPTION_KEY"] = strings.ToUpper(v["BOT_SESSION_ENCRYPTION_KEY"])
	r := Diagnose(v, "runtime", "file")
	Compare(&r, v, old)
	if !r.ConfigurationOK || !containsCheck(r, "comparison.data", "volumes_snapshots_and_pending_operations_not_checked") {
		t.Fatal("equivalent settings rejected or data scope overclaimed")
	}
}

func TestOfflineCommandsNeverDialMutateOrEvaluate(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	redisServer := miniredis.RunT(t)
	v := validEnvironment()
	v["DUJIAO_BASE_URL"] = server.URL
	v["BOT_REDIS_URL"] = "redis://" + redisServer.Addr()
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "must-not-exist")
	v["DUJIAO_CHANNEL_SECRET"] = "$(touch " + sentinel + ")"
	var content strings.Builder
	for k, value := range v {
		fmt.Fprintf(&content, "%s='%s'\n", k, value)
	}
	path := filepath.Join(dir, "private.env")
	_ = os.WriteFile(path, []byte(content.String()), 0600)
	before, _ := os.ReadFile(path)
	var out bytes.Buffer
	if err := Run([]string{"check", "--env-file", path}, &out, func(string) string { t.Fatal("file mode read process environment"); return "" }); err != nil {
		t.Fatal(err, out.String())
	}
	if calls.Load() != 0 || len(redisServer.Keys()) != 0 || redisServer.CommandCount() != 0 || redisServer.TotalConnectionCount() != 0 {
		t.Fatal("offline check contacted a service")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("dotenv executed a command")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("checker modified credentials")
	}
	for _, private := range []string{v["DUJIAO_CHANNEL_KEY"], v["DUJIAO_CHANNEL_SECRET"], v["TELEGRAM_BOT_TOKEN"], v["BOT_SESSION_ENCRYPTION_KEY"], path} {
		if strings.Contains(out.String(), private) {
			t.Fatal("output leaked input")
		}
	}
}

func TestFileComparisonDoesNotMutateEitherInput(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "current.env"), filepath.Join(dir, "before.env")}
	write := func(path string, v map[string]string) []byte {
		var body strings.Builder
		for k, value := range v {
			fmt.Fprintf(&body, "%s='%s'\n", k, value)
		}
		data := []byte(body.String())
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return data
	}
	v := validEnvironment()
	v["COMPOSE_PROJECT_NAME"] = "original-project"
	first, second := write(paths[0], v), write(paths[1], v)
	var out bytes.Buffer
	if err := Run([]string{"check", "--env-file", paths[0], "--compare-env", paths[1]}, &out, func(string) string { return "ignored" }); err != nil {
		t.Fatal(err)
	}
	var r Report
	if json.Unmarshal(out.Bytes(), &r) != nil || !r.ConfigurationOK {
		t.Fatal("valid file comparison failed")
	}
	v["COMPOSE_PROJECT_NAME"] = "changed-project"
	first = write(paths[0], v)
	out.Reset()
	if err := Run([]string{"check", "--env-file", paths[0], "--compare-env", paths[1]}, &out, func(string) string { return "ignored" }); err == nil {
		t.Fatal("project change silently approved")
	}
	if json.Unmarshal(out.Bytes(), &r) != nil || !containsCheck(r, "stable.COMPOSE_PROJECT_NAME", "changed_requires_manual_review") {
		t.Fatal("missing project safety check")
	}
	for i, want := range [][]byte{first, second} {
		got, _ := os.ReadFile(paths[i])
		if !bytes.Equal(got, want) {
			t.Fatal("comparison modified input")
		}
	}
	if strings.Contains(out.String(), "PRIVATE_") || strings.Contains(out.String(), "original-project") || strings.Contains(out.String(), paths[0]) {
		t.Fatal("comparison disclosed input")
	}
}

func TestCommandErrorsDoNotEchoArguments(t *testing.T) {
	for _, args := range [][]string{{"check", "--PRIVATE_SECRET"}, {"check", "unexpected_PRIVATE_SECRET"}, {"init", "--profile", "PRIVATE_SECRET"}, {"check", "--profile", "PRIVATE_SECRET"}, {"check", "--env-file", "/PRIVATE_SECRET/nonexistent"}, {"check", "--env-file", ""}, {"check", "--compare-env", ""}} {
		var out bytes.Buffer
		err := Run(args, &out, func(string) string { return "" })
		if err == nil || strings.Contains(out.String()+err.Error(), "PRIVATE_SECRET") {
			t.Fatal("unsafe command error")
		}
	}
}
