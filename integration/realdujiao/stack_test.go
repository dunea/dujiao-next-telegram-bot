//go:build realdujiao

package realdujiao

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Official per-platform manifests, not mutable tags. Never accept a caller's
// merchant URL, credentials, image override, database DSN or existing volume.
var releases = []struct {
	version, commit, amd64, arm64 string
}{
	{"v1.4.8", "d2e44618de03068cbbe874a23a6d7c6a249a79ec", "6d87d4d7019a15c7719b934ef7cca1115d9d976376590e728749fd80e87197a5", "675323cc6e223e84ac992b1e3333b9c3896ff822c41d09c0ef612189402416e1"},
	{"v1.4.9", "b20da89eef9bbfbc454e028a39f1cc7b93596d37", "e0658d4ac6020f5ee2b2f11fd5b691dbbb2c3c22b98af1812a12a881bee0882e", "ac0e4324993d9167451cb697b18ebea2f4a8cf90b62be486030e5b172c1bb7c7"},
	{"v1.5.0", "41b86260dc9a60269526057eed8d915844ceb3a3", "7176b3da99b6e4acb0b9e18331c46cc6f7e2f4a66101fe51080bf351ceec7fd4", "6365a3d7f99ca89d9423b3c1a41bafd88c9d7a709826d6db616e9db6785499d7"},
}

const postgresImage = "postgres:16-alpine@sha256:721873c34ceb9f8d8fc265984940dc982404c105f19ad51be9fdc5970a6080ea"
const redisImage = "redis:7.4-alpine@sha256:858f009f9709ce576febc734aa78b8f6d624b82571f9ddb6bda4377c833b3499"
const ownerLabel = "io.dujiao-bot.real-test"

type stack struct {
	t                                                           *testing.T
	id, dir, driver, arch, api, pg, volume, base, adminPassword string
	containers                                                  []string
	client                                                      *http.Client
	proxy                                                       *httptest.Server
	dbName                                                      string
	adminToken                                                  string
	redis                                                       string
	dbDir                                                       string
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *stack) command(input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, "docker", args...)
	c.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		// Full Docker diagnostics may contain synthetic credentials. Save privately,
		// never put arbitrary command arguments or server bodies in CI logs.
		_ = os.WriteFile(filepath.Join(s.dir, "docker-error.log"), stderr.Bytes(), 0600)
		return nil, fmt.Errorf("docker %s failed (private diagnostics: %s): %w", args[0], s.dir, err)
	}
	return out, nil
}

func (s *stack) docker(args ...string) string {
	s.t.Helper()
	out, err := s.command(nil, args...)
	if err != nil {
		s.t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func newStack(t *testing.T, driver string) *stack {
	t.Helper()
	dir, err := os.MkdirTemp("", "dujiao-real-"+driver+"-")
	if err != nil {
		t.Fatal(err)
	}
	s := &stack{t: t, id: "dj-real-" + randomHex(6), dir: dir, driver: driver, dbName: "fixture",
		adminPassword: "Test!" + randomHex(20), client: &http.Client{Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.dbDir = filepath.Join(dir, "db")
	t.Logf("private test workspace: %s; owner: %s", dir, s.id)
	// Remote engines would interpret our bind mounts on another host. Fail closed.
	endpoint := s.docker("context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if env := os.Getenv("DOCKER_HOST"); env != "" {
		endpoint = env
	}
	if !strings.HasPrefix(endpoint, "unix://") {
		t.Fatal("real tests require a local Unix-socket Docker engine")
	}
	s.arch = s.docker("info", "--format", "{{.Architecture}}")
	if s.arch != "aarch64" && s.arch != "arm64" && s.arch != "x86_64" && s.arch != "amd64" {
		t.Fatal("unsupported Docker architecture")
	}
	t.Cleanup(s.cleanup)
	// Internal Docker networks have no host-published ports. A loopback-only
	// tunnel over docker exec gives the real Go client access without granting
	// the upstream application an Internet route.
	s.proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Close = true
		var request bytes.Buffer
		if err := r.Write(&request); err != nil {
			http.Error(w, "invalid test request", 502)
			return
		}
		// Read the HTTP response as a stream, not via Cmd.Output: waiting for
		// netcat's idle timeout after the response can manufacture client timeouts.
		cmd := exec.CommandContext(r.Context(), "docker", "exec", "-i", s.api, "nc", "-w", "15", "127.0.0.1", "8080")
		cmd.Stdin = bytes.NewReader(request.Bytes())
		out, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			http.Error(w, "isolated backend not ready", 502)
			return
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		response, err := http.ReadResponse(bufio.NewReader(out), r)
		if err != nil {
			http.Error(w, "invalid backend response", 502)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	s.base = s.proxy.URL
	s.docker("network", "create", "--internal", "--label", ownerLabel+"="+s.id, s.id)
	for i := range releases {
		s.ensureImage(s.image(i))
	}
	for _, sub := range []string{"db", "backup"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	s.ensureImage(redisImage)
	s.startRedis("queue")
	dsn := "/app/db/dujiao.db"
	if driver == "postgres" {
		s.ensureImage(postgresImage)
		s.volume = s.id + "-pg"
		s.docker("volume", "create", "--label", ownerLabel+"="+s.id, s.volume)
		s.pg = s.id + "-pg"
		s.containers = append(s.containers, s.pg)
		s.docker("run", "-d", "--name", s.pg, "--label", ownerLabel+"="+s.id,
			"--network", s.id, "--network-alias", "postgres", "--mount", "type=volume,src="+s.volume+",dst=/var/lib/postgresql/data",
			"-e", "POSTGRES_USER=fixture", "-e", "POSTGRES_PASSWORD="+s.adminPassword, "-e", "POSTGRES_DB=fixture", postgresImage)
		s.wait(func() bool {
			// The official image starts a temporary Unix-socket-only server
			// during initdb. Probe TCP so a fast runner cannot start the API
			// before the final network-listening PostgreSQL process is ready.
			_, err := s.command(nil, "exec", s.pg, "pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", "fixture", "-d", "fixture")
			return err == nil
		})
		dsn = "host=postgres user=fixture password=" + s.adminPassword + " dbname=fixture port=5432 sslmode=disable"
	} else if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Fatal("sqlite3 CLI required")
	}
	config := fmt.Sprintf(`app:
  secret_key: %s
server:
  host: 0.0.0.0
  port: 8080
  mode: release
database:
  driver: %s
  dsn: %s
  pool:
    max_open_conns: 1
    max_idle_conns: 1
jwt:
  secret: %s
user_jwt:
  secret: %s
bootstrap:
  default_admin_username: fixture_admin
  default_admin_password: %s
redis:
  enabled: true
  host: redis
  port: 6379
queue:
  enabled: true
  host: redis
  port: 6379
  db: 1
email:
  enabled: false
telegram_auth:
  enabled: false
`, randomHex(32), driver, dsn, randomHex(32), randomHex(32), s.adminPassword)
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *stack) ensureImage(ref string) {
	// A digest-addressed local image is immutable; reuse it without another
	// registry request. Fresh machines still pull the exact pinned manifest.
	if _, err := s.command(nil, "image", "inspect", ref); err != nil {
		s.docker("pull", ref)
	}
}

func (s *stack) startRedis(suffix string) {
	s.redis = s.id + "-redis-" + suffix
	s.containers = append(s.containers, s.redis)
	s.docker("run", "-d", "--name", s.redis, "--label", ownerLabel+"="+s.id, "--network", s.id, "--network-alias", "redis", redisImage)
	s.wait(func() bool {
		out, err := s.command(nil, "exec", s.redis, "redis-cli", "ping")
		return err == nil && strings.TrimSpace(string(out)) == "PONG"
	})
}

func (s *stack) image(i int) string {
	digest := releases[i].amd64
	if s.arch == "aarch64" || s.arch == "arm64" {
		digest = releases[i].arm64
	}
	return "dujiaonext/api@sha256:" + digest
}

func (s *stack) wait(ready func() bool) {
	s.t.Helper()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		if ready() {
			return
		}
		time.Sleep(time.Second)
	}
	s.t.Fatal("isolated service did not become ready within 90 seconds")
}

func (s *stack) start(i int, suffix string) {
	s.t.Helper()
	s.api = s.id + "-" + strings.TrimPrefix(releases[i].version, "v") + "-" + suffix
	s.containers = append(s.containers, s.api)
	s.docker("run", "-d", "--name", s.api, "--label", ownerLabel+"="+s.id, "--network", s.id,
		"--mount", "type=bind,src="+filepath.Join(s.dir, "config.yml")+",dst=/app/config.yml,readonly",
		"--mount", "type=bind,src="+s.dbDir+",dst=/app/db", s.image(i))
	s.wait(func() bool {
		if s.docker("inspect", "--format", "{{.State.Running}}", s.api) != "true" {
			_, _ = s.command(nil, "cp", s.api+":/app/logs", filepath.Join(s.dir, s.api+"-logs"))
			s.t.Fatal("isolated upstream exited during startup; inspect private workspace logs")
		}
		r, err := s.client.Get(s.base + "/health")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	})
	logs := s.docker("logs", s.api)
	if !strings.Contains(logs, releases[i].version) {
		s.t.Fatal("official binary startup did not report expected version")
	}
	s.t.Logf("started real %s (%s; source %s) on %s", releases[i].version, s.image(i), releases[i].commit, s.driver)
}

func (s *stack) stop() {
	if s.api != "" {
		s.docker("stop", "--time", "15", s.api)
		s.api = ""
	}
}

func (s *stack) cleanup() {
	if s.proxy != nil {
		s.proxy.Close()
	}
	// Never enumerate/delete pre-existing resources. Every target is recorded
	// when created and its unique ownership label is checked again before removal.
	for _, name := range s.containers {
		label, err := s.command(nil, "inspect", "--format", `{{index .Config.Labels "`+ownerLabel+`"}}`, name)
		if err != nil || strings.TrimSpace(string(label)) != s.id {
			continue
		}
		logs, _ := s.command(nil, "logs", name)
		_ = os.WriteFile(filepath.Join(s.dir, name+".log"), logs, 0600)
		if name != s.pg && !strings.HasPrefix(name, s.id+"-redis-") {
			_, _ = s.command(nil, "cp", name+":/app/logs", filepath.Join(s.dir, name+"-logs"))
		}
		_, _ = s.command(nil, "stop", "--time", "15", name)
		_, _ = s.command(nil, "rm", name)
	}
	if !s.t.Failed() && s.volume != "" {
		label, err := s.command(nil, "volume", "inspect", "--format", `{{index .Labels "`+ownerLabel+`"}}`, s.volume)
		if err == nil && strings.TrimSpace(string(label)) == s.id {
			_, _ = s.command(nil, "volume", "rm", s.volume)
		}
	}
	label, err := s.command(nil, "network", "inspect", "--format", `{{index .Labels "`+ownerLabel+`"}}`, s.id)
	if err == nil && strings.TrimSpace(string(label)) == s.id {
		_, _ = s.command(nil, "network", "rm", s.id)
	}
	// Private config, synthetic database/backups and logs remain locally for
	// diagnosis. CI must not upload them; only go test's sanitized output is public.
}

func (s *stack) admin(method, path, token string, body, target any) {
	s.t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, s.base+"/api/v1/admin"+path, bytes.NewReader(data))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("admin %s %s: transport failed", method, path)
	}
	defer r.Body.Close()
	var envelope struct {
		Status  int             `json:"status_code"`
		Data    json.RawMessage `json:"data"`
		Message string          `json:"msg"`
	}
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		s.t.Fatalf("admin %s %s: invalid JSON", method, path)
	}
	if r.StatusCode != 200 || envelope.Status != 0 {
		_ = os.WriteFile(filepath.Join(s.dir, "admin-error.json"), []byte(envelope.Message), 0600)
		s.t.Fatalf("admin %s %s: HTTP %d, status %d (details private)", method, path, r.StatusCode, envelope.Status)
	}
	if target != nil {
		if err := json.Unmarshal(envelope.Data, target); err != nil {
			s.t.Fatalf("admin %s: invalid data structure", path)
		}
	}
}

func (s *stack) login() string {
	// Reuse the one genuine login across upgrades; repeated logins would
	// legitimately exhaust upstream's unchanged login rate limit.
	if s.adminToken != "" {
		return s.adminToken
	}
	var result struct {
		Token string `json:"token"`
	}
	s.admin("POST", "/login", "", map[string]any{"username": "fixture_admin", "password": s.adminPassword}, &result)
	if result.Token == "" {
		s.t.Fatal("login returned no token")
	}
	s.adminToken = result.Token
	return result.Token
}
