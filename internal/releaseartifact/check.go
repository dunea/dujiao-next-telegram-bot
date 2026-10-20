// Package releaseartifact checks local, unextracted release archives. It never
// executes their contents or reads deployment credentials.
package releaseartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
)

type Manifest struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
	Candidate bool   `json:"candidate"`
}
type Report struct {
	Manifest
	Archives int      `json:"archives"`
	Checks   []string `json:"checks"`
}

var targets = []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"}
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-dev)?$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)

func readRegular(name string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("missing, nonregular or oversized artifact")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, errors.New("cannot read artifact")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("artifact read failed or exceeded limit")
	}
	return b, nil
}

func Check(dir string) (Report, error) {
	var report Report
	data, err := readRegular(filepath.Join(dir, "release-manifest.json"), 4096)
	if err != nil || json.Unmarshal(data, &report.Manifest) != nil {
		return report, errors.New("invalid release manifest")
	}
	m := report.Manifest
	if !versionPattern.MatchString(m.Version) || !commitPattern.MatchString(m.Commit) || !strings.HasPrefix(m.GoVersion, "go1.") || m.Candidate != strings.HasSuffix(m.Version, "-dev") {
		return report, errors.New("manifest version/provenance mismatch")
	}
	data, err = readRegular(filepath.Join(dir, "checksums.txt"), 4096)
	if err != nil {
		return report, err
	}
	checksums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || len(parts[0]) != 64 || strings.ContainsAny(parts[1], "/\\") || checksums[parts[1]] != "" {
			return report, errors.New("invalid checksum list")
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return report, errors.New("invalid checksum digest")
		}
		checksums[parts[1]] = parts[0]
	}
	if len(checksums) != len(targets) {
		return report, errors.New("expected four release checksums")
	}
	for _, target := range targets {
		root := "dujiao-next-telegram-bot_v" + m.Version + "_" + target
		name := root + ".tar.gz"
		data, err := readRegular(filepath.Join(dir, name), 128<<20)
		if err != nil {
			return report, err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != checksums[name] {
			return report, errors.New("release checksum mismatch")
		}
		if err := checkArchive(data, root, target, m); err != nil {
			return report, fmt.Errorf("%s: %w", target, err)
		}
		report.Archives++
	}
	report.Checks = []string{"sha256", "safe_paths_and_public_files", "four_target_binary_headers", "clean_vcs_revision", "go_toolchain", "licenses_and_documents", "relative_markdown_links"}
	return report, nil
}

var rootFiles = map[string]bool{
	"dujiao-next-telegram-bot": true, "dujiao-compatcheck": true,
	"build-info.txt": true, "compatcheck-build-info.txt": true,
	".env.example": true, "LICENSE": true, "THIRD_PARTY_NOTICES.md": true,
	"README.md": true, "CHANGELOG.md": true, "ROADMAP.md": true, "RELEASE.md": true,
}

func allowedFile(name string) bool {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".git" || part == ".." || part == ".env" || part == "data" || part == "audit" {
			return false
		}
	}
	if rootFiles[name] {
		return true
	}
	if strings.HasPrefix(name, "docs/") {
		return path.Dir(name) == "docs" && strings.HasSuffix(name, ".md")
	}
	return strings.HasPrefix(name, "third-party/") && !strings.HasSuffix(name, ".env") && !strings.HasSuffix(name, ".jsonl")
}

func checkArchive(data []byte, root, target string, m Manifest) error {
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid gzip archive")
	}
	defer z.Close()
	limited := &io.LimitedReader{R: z, N: 256 << 20}
	reader := tar.NewReader(limited)
	files := map[string][]byte{}
	seen := map[string]bool{}
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid tar stream")
		}
		full := strings.TrimSuffix(h.Name, "/")
		if full != root && !strings.HasPrefix(full, root+"/") {
			return errors.New("archive escapes bundle root")
		}
		if path.Clean(full) != full || strings.Contains(full, "\\") || seen[full] {
			return errors.New("unsafe or duplicate archive entry")
		}
		seen[full] = true
		name := strings.TrimPrefix(full, root+"/")
		if h.Typeflag == tar.TypeDir {
			if full != root && name != "docs" && name != "third-party" && !strings.HasPrefix(name, "third-party/") {
				return errors.New("nonpublic archive directory")
			}
			continue
		}
		if h.Typeflag != tar.TypeReg || !allowedFile(name) || h.Size < 0 || h.Size > 64<<20 || h.Mode&07022 != 0 || len(h.Xattrs) != 0 {
			return errors.New("nonpublic, unsafe or oversized archive file")
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			return errors.New("cannot read tar entry")
		}
		files[name] = body
		if name == "dujiao-next-telegram-bot" || name == "dujiao-compatcheck" {
			if h.Mode&0111 == 0 {
				return errors.New("binary is not executable")
			}
			command := "bot"
			if name == "dujiao-compatcheck" {
				command = "compatcheck"
			}
			if err := checkBinary(body, target, command, m); err != nil {
				return err
			}
		}
	}
	// Read through gzip's CRC/trailer too; tar EOF alone is not gzip integrity.
	buffer := make([]byte, 32768)
	for {
		n, err := limited.Read(buffer)
		if len(bytes.Trim(buffer[:n], "\x00")) != 0 || limited.N <= 0 {
			return errors.New("unexpected trailing archive data or expansion limit")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("gzip integrity failed")
		}
	}
	for name := range rootFiles {
		if len(files[name]) == 0 {
			return errors.New("required release file missing")
		}
	}
	for _, name := range []string{"third-party/Go/LICENSE", "docs/acceptance-v1.0.md", "docs/release-v1.0.md", "docs/order-support.md"} {
		if len(files[name]) == 0 {
			return errors.New("required license/document missing")
		}
	}
	for name, body := range files {
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		for _, match := range markdownLink.FindAllSubmatch(body, -1) {
			link := strings.SplitN(string(match[1]), "#", 2)[0]
			if link == "" || strings.Contains(link, ":") || strings.HasPrefix(link, "//") {
				continue
			}
			resolved := path.Clean(path.Join(path.Dir(name), link))
			if _, ok := files[resolved]; !ok {
				return errors.New("broken relative document link")
			}
		}
	}
	return nil
}

func checkBinary(body []byte, target, command string, m Manifest) error {
	if strings.HasPrefix(target, "linux_") {
		f, err := elf.NewFile(bytes.NewReader(body))
		if err != nil {
			return errors.New("invalid Linux binary")
		}
		defer f.Close()
		want := elf.EM_X86_64
		if strings.HasSuffix(target, "arm64") {
			want = elf.EM_AARCH64
		}
		if f.Machine != want || f.Class != elf.ELFCLASS64 || f.Type != elf.ET_EXEC {
			return errors.New("wrong Linux binary target")
		}
	} else {
		f, err := macho.NewFile(bytes.NewReader(body))
		if err != nil {
			return errors.New("invalid macOS binary")
		}
		defer f.Close()
		want := macho.CpuAmd64
		if strings.HasSuffix(target, "arm64") {
			want = macho.CpuArm64
		}
		if f.Cpu != want || f.Type != macho.TypeExec {
			return errors.New("wrong macOS binary target")
		}
	}
	info, err := buildinfo.Read(bytes.NewReader(body))
	if err != nil {
		return errors.New("missing Go build information")
	}
	if info.Path != "github.com/dunea/dujiao-next-telegram-bot/cmd/"+command {
		return errors.New("wrong executable command")
	}
	platform := strings.Split(target, "_")
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != platform[0] || settings["GOARCH"] != platform[1] {
		return errors.New("binary build target mismatch")
	}
	return checkBuildInfo(info, m)
}

func checkBuildInfo(info *debug.BuildInfo, m Manifest) error {
	values := map[string]string{}
	for _, v := range info.Settings {
		values[v.Key] = v.Value
	}
	if info.GoVersion != m.GoVersion || values["vcs.revision"] != m.Commit || values["vcs.modified"] != "false" || values["CGO_ENABLED"] != "0" || values["-trimpath"] != "true" {
		return errors.New("binary provenance/toolchain/build flags mismatch")
	}
	if !strings.HasPrefix(info.Path, "github.com/dunea/dujiao-next-telegram-bot/cmd/") {
		return errors.New("unexpected executable module")
	}
	return nil
}
