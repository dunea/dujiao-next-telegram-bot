package releaseartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"runtime/debug"
	"strings"
	"testing"
)

func TestOnlyPublicReleaseFilesAllowed(t *testing.T) {
	for _, name := range []string{"README.md", ".env.example", "dujiao-next-telegram-bot", "docs/order-support.md", "third-party/Go/LICENSE"} {
		if !allowedFile(name) {
			t.Fatal("public file rejected", name)
		}
	}
	for _, name := range []string{".env", "data/state.json", "audit/audit.jsonl", ".git/config", "../secret", "/private/key", "docs/../../.env", "docs/secret.json", "third-party/../.env", "third-party/x/.env", "README.md\\.env"} {
		if allowedFile(name) {
			t.Fatal("private/unsafe file allowed", name)
		}
	}
}

func TestBinaryProvenanceRequiresExactCleanBuild(t *testing.T) {
	m := Manifest{Commit: strings.Repeat("a", 40), GoVersion: "go1.25.0"}
	base := debug.BuildInfo{GoVersion: m.GoVersion, Path: "github.com/dunea/dujiao-next-telegram-bot/cmd/bot", Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: m.Commit}, {Key: "vcs.modified", Value: "false"}, {Key: "CGO_ENABLED", Value: "0"}, {Key: "-trimpath", Value: "true"}}}
	if err := checkBuildInfo(&base, m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"vcs.revision", "vcs.modified", "CGO_ENABLED", "-trimpath"} {
		copy := base
		copy.Settings = append([]debug.BuildSetting(nil), base.Settings...)
		for i := range copy.Settings {
			if copy.Settings[i].Key == key {
				copy.Settings[i].Value = "invalid"
			}
		}
		if checkBuildInfo(&copy, m) == nil {
			t.Fatal("bad build flag accepted", key)
		}
	}
	base.GoVersion = "go1.24.0"
	if checkBuildInfo(&base, m) == nil {
		t.Fatal("wrong toolchain accepted")
	}
}

func testArchive(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	writer := tar.NewWriter(z)
	for _, h := range headers {
		if err := writer.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg && h.Size > 0 {
			_, _ = writer.Write(bytes.Repeat([]byte("x"), int(h.Size)))
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestArchiveRejectsTraversalLinksDuplicatesAndPrivateFiles(t *testing.T) {
	for _, headers := range [][]*tar.Header{
		{{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0644}},
		{{Name: "bundle/.env", Typeflag: tar.TypeReg, Mode: 0600}},
		{{Name: "bundle/data/", Typeflag: tar.TypeDir, Mode: 0755}},
		{{Name: "bundle/README.md", Typeflag: tar.TypeSymlink, Linkname: "/private/key", Mode: 0644}},
		{{Name: "bundle/README.md", Typeflag: tar.TypeReg, Mode: 0644}, {Name: "bundle/README.md", Typeflag: tar.TypeReg, Mode: 0644}},
		{{Name: "bundle/README.md", Typeflag: tar.TypeReg, Mode: 0666}},
	} {
		if checkArchive(testArchive(t, headers...), "bundle", "linux_amd64", Manifest{}) == nil {
			t.Fatal("unsafe archive accepted")
		}
	}
}

func TestArchiveRejectsInvalidBinaryAndCorruptGzip(t *testing.T) {
	b := testArchive(t, &tar.Header{Name: "bundle/dujiao-next-telegram-bot", Typeflag: tar.TypeReg, Mode: 0755, Size: 10})
	if err := checkArchive(b, "bundle", "linux_amd64", Manifest{}); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatal("invalid executable accepted", err)
	}
	b = testArchive(t, &tar.Header{Name: "bundle/README.md", Typeflag: tar.TypeReg, Mode: 0644, Size: 10})
	b[len(b)-5] ^= 0xff
	if checkArchive(b, "bundle", "linux_amd64", Manifest{}) == nil {
		t.Fatal("corrupt gzip accepted")
	}
}
