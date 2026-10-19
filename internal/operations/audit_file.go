package operations

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
)

// AuditFile is a bounded append-only JSONL sink with local rotation. It is not
// a transactional, crash-fsynced or tamper-proof financial ledger.
type AuditFile struct {
	mu             sync.Mutex
	file           *os.File
	path           string
	size, maxBytes int64
	backups        int
}

func OpenAuditFile(path string, maxBytes int64, backups int) (*AuditFile, error) {
	if maxBytes < 1 || backups < 1 {
		return nil, errors.New("invalid audit file retention")
	}
	w := &AuditFile{path: path, maxBytes: maxBytes, backups: backups}
	if err := w.open(); err != nil {
		return nil, errors.New("cannot open audit file; check path and permissions")
	}
	return w, nil
}

func (w *AuditFile) open() error {
	info, err := os.Lstat(w.path)
	if err == nil && !info.Mode().IsRegular() {
		return errors.New("audit path is not a regular file")
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	info, err = file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	w.file, w.size = file, info.Size()
	return nil
}

func (w *AuditFile) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, errors.New("audit file closed")
	}
	if int64(len(data)) > w.maxBytes {
		return 0, errors.New("audit record exceeds size limit")
	}
	if w.size > 0 && w.size+int64(len(data)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			fmt.Fprintln(os.Stderr, "audit rotation failed; stdout audit remains available")
			return 0, err
		}
	}
	n, err := w.file.Write(data)
	w.size += int64(n)
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit write failed; stdout audit remains available")
	}
	return n, err
}

func (w *AuditFile) rotate() error {
	if err := w.file.Sync(); err != nil {
		return err
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	for index := w.backups - 1; index >= 1; index-- {
		if err := os.Rename(w.path+"."+strconv.Itoa(index), w.path+"."+strconv.Itoa(index+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open()
}

func (w *AuditFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	syncErr := w.file.Sync()
	err := w.file.Close()
	w.file = nil
	return errors.Join(syncErr, err)
}
