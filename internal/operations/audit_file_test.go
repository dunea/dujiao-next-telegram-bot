package operations

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAuditFileRotationAppendAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	file, err := OpenAuditFile(path, 24, 2)
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if _, err := file.Write([]byte("{\"ok\":true}\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(name)
		if err != nil || info.Size() > 24 || info.Mode().Perm() != 0600 {
			t.Fatal("unbounded/insecure file", info, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatal("retention exceeded")
	}
	file, err = OpenAuditFile(path, 24, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("{}\n")); err != nil {
		t.Fatal(err)
	}
}

func TestAuditFileConcurrentWritesAndInvalidPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	file, err := OpenAuditFile(path, 1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for i := range 100 {
		wait.Go(func() {
			if _, err := fmt.Fprintf(file, "{\"id\":%d}\n", i); err != nil {
				t.Error(err)
			}
		})
	}
	wait.Wait()
	_ = file.Close()
	if _, err := file.Write([]byte("closed")); err == nil {
		t.Fatal("write to closed file")
	}
	if _, err := OpenAuditFile(filepath.Join(path, "missing"), 100, 1); err == nil {
		t.Fatal("invalid path accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAuditFile(link, 100, 1); err == nil {
		t.Fatal("symlink audit path accepted")
	}
}
