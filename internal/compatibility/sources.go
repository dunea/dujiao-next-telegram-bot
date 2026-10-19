package compatibility

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type SourceResult struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Path    string `json:"path"`
	Status  string `json:"status"`
}

func VerifySources(ctx context.Context) []SourceResult {
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return verifySources(ctx, client, "https://raw.githubusercontent.com/dujiao-next/dujiao-next/", Versions())
}
func verifySources(ctx context.Context, client *http.Client, base string, versions []Version) []SourceResult {
	var results []SourceResult
	var expected []string
	for _, v := range versions {
		for _, s := range v.Sources {
			results = append(results, SourceResult{Version: v.Version, Commit: v.Commit, Path: s.Path, Status: "fetch_error"})
			expected = append(expected, s.SHA256)
		}
	}
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()
			r := &results[i]
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+r.Commit+"/"+r.Path, nil)
			if err != nil {
				return
			}
			response, err := client.Do(request)
			if err != nil {
				return
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return
			}
			body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
			if err != nil || len(body) > 1<<20 {
				return
			}
			r.Status = "hash_mismatch"
			if fmt.Sprintf("%x", sha256.Sum256(body)) == expected[i] {
				r.Status = "matched"
			}
		}()
	}
	wg.Wait()
	return results
}
