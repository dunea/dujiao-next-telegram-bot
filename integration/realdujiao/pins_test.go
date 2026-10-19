//go:build realdujiao

package realdujiao

import (
	"encoding/hex"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/compatibility"
)

func TestReleasePins(t *testing.T) {
	versions := compatibility.Versions()
	if len(versions) != len(releases) {
		t.Fatal("real matrix differs from reviewed source matrix")
	}
	seen := map[string]bool{}
	for i, release := range releases {
		if versions[i].Version != release.version || versions[i].Commit != release.commit {
			t.Fatal("real image matrix/source revision mismatch")
		}
		for _, digest := range []string{release.amd64, release.arm64} {
			b, err := hex.DecodeString(digest)
			if err != nil || len(b) != 32 || seen[digest] {
				t.Fatal("invalid or reused image digest")
			}
			seen[digest] = true
		}
	}
}
