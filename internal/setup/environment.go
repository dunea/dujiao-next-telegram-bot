package setup

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	deployment "github.com/dunea/dujiao-next-telegram-bot"
)

const maxEnvSize = 64 << 10

// FileIssue deliberately carries no file names, values or underlying OS errors.
type FileIssue struct {
	Code string
	Line int
}

func (*FileIssue) Error() string { return "environment file rejected" }

func variableNames() map[string]bool {
	names := map[string]bool{"COMPOSE_PROJECT_NAME": true}
	for _, line := range strings.Split(deployment.EnvExample, "\n") {
		if name, _, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			names[name] = true
		}
	}
	return names
}

// ParseEnv accepts a strict single-line dotenv subset. It never invokes a shell,
// expands variables, interprets escapes, or imports ambient process values.
func ParseEnv(body []byte) (map[string]string, *FileIssue) {
	if len(body) > maxEnvSize {
		return nil, &FileIssue{Code: "file_too_large"}
	}
	if !utf8.Valid(body) {
		return nil, &FileIssue{Code: "invalid_encoding"}
	}
	values := make(map[string]string)
	known := variableNames()
	for i, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		fail := func(code string) (map[string]string, *FileIssue) { return nil, &FileIssue{Code: code, Line: i + 1} }
		if !ok {
			return fail("unsupported_syntax")
		}
		if !known[name] {
			return fail("unknown_variable")
		}
		if _, exists := values[name]; exists {
			return fail("duplicate_variable")
		}
		literal := strings.HasPrefix(value, "'")
		if literal || strings.HasPrefix(value, `"`) {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return fail("unsupported_quoting")
			}
			quote := value[0]
			value = value[1 : len(value)-1]
			if strings.ContainsRune(value, rune(quote)) {
				return fail("unsupported_quoting")
			}
		} else if strings.ContainsAny(value, " \t#'\"") {
			return fail("unsupported_syntax")
		}
		if strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.Contains(value, "\\") || (!literal && strings.Contains(value, "$")) {
			return fail("expansion_or_escape_rejected")
		}
		if len(value) > 4096 || strings.TrimSpace(value) != value {
			return fail("invalid_value_layout")
		}
		values[name] = value
	}
	return values, nil
}

func ReadEnv(path string) (map[string]string, *FileIssue) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, &FileIssue{Code: "file_unavailable"}
	}
	if !info.Mode().IsRegular() {
		return nil, &FileIssue{Code: "regular_file_required"}
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, &FileIssue{Code: "private_permissions_required"}
	}
	// Non-blocking + no-follow also protects against replacement with a FIFO or
	// symlink between Lstat and Open on our supported Linux/macOS targets.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &FileIssue{Code: "file_unavailable"}
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 {
		return nil, &FileIssue{Code: "file_changed_or_unsafe"}
	}
	body, err := io.ReadAll(io.LimitReader(f, maxEnvSize+1))
	if err != nil {
		return nil, &FileIssue{Code: "file_unavailable"}
	}
	return ParseEnv(body)
}

// InitEnv exclusively creates one private file; it never reads/replaces an old
// environment, regenerates an existing key, or creates a parent directory.
func InitEnv(path string) *FileIssue {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return &FileIssue{Code: "random_source_unavailable"}
	}
	body := strings.Replace(deployment.EnvExample, "\nBOT_SESSION_ENCRYPTION_KEY=\n", "\nBOT_SESSION_ENCRYPTION_KEY="+hex.EncodeToString(key)+"\n", 1)
	if body == deployment.EnvExample {
		return &FileIssue{Code: "invalid_embedded_template"}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return &FileIssue{Code: "new_file_required"}
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return &FileIssue{Code: "private_permissions_unavailable"}
	}
	// Failed/partial new files are left private for manual inspection. Never
	// remove a path which another process might have replaced in the meantime.
	_, writeErr := io.WriteString(f, body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return &FileIssue{Code: "write_incomplete_inspect_new_file"}
	}
	return nil
}
