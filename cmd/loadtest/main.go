package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func main() {
	baseURL := flag.String("url", "http://127.0.0.1:18444", "bot HTTP base URL")
	mode := flag.String("mode", "callback", "health or callback")
	requests := flag.Int("n", 2000, "total request count")
	concurrency := flag.Int("c", 50, "worker count")
	flag.Parse()
	if *requests < 1 || *concurrency < 1 {
		fmt.Fprintln(os.Stderr, "n and c must be positive")
		os.Exit(2)
	}

	transport := &http.Transport{
		MaxIdleConns:        *concurrency * 2,
		MaxIdleConnsPerHost: *concurrency,
		IdleConnTimeout:     30 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	jobs := make(chan int)
	durations := make(chan time.Duration, *requests)
	var failures atomic.Int64
	var workers sync.WaitGroup
	started := time.Now()

	for range *concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				request, err := makeRequest(*baseURL, *mode, index)
				if err != nil {
					failures.Add(1)
					continue
				}
				requestStarted := time.Now()
				response, err := client.Do(request)
				durations <- time.Since(requestStarted)
				if err != nil {
					failures.Add(1)
					continue
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode < 200 || response.StatusCode >= 300 {
					failures.Add(1)
				}
			}
		}()
	}
	for index := range *requests {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	close(durations)

	values := make([]time.Duration, 0, *requests)
	for duration := range durations {
		values = append(values, duration)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	elapsed := time.Since(started)
	fmt.Printf("mode=%s requests=%d concurrency=%d failures=%d elapsed=%s throughput=%.1f req/s p50=%s p95=%s p99=%s\n",
		*mode, *requests, *concurrency, failures.Load(), elapsed.Round(time.Millisecond), float64(*requests)/elapsed.Seconds(),
		percentile(values, 50), percentile(values, 95), percentile(values, 99))
	if failures.Load() > 0 {
		os.Exit(1)
	}
}

func makeRequest(baseURL, mode string, index int) (*http.Request, error) {
	if mode == "health" {
		return http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/healthz", nil)
	}
	body := []byte(`{"order_id":` + strconv.Itoa(index+1) + `,"telegram_user_id":"42"}`)
	path := "/internal/order-paid"
	timestamp := time.Now().Unix()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Dujiao-Next-Channel-Key", envOr("DUJIAO_CHANNEL_KEY", "integration-key"))
	request.Header.Set("Dujiao-Next-Channel-Timestamp", strconv.FormatInt(timestamp, 10))
	request.Header.Set("Dujiao-Next-Channel-Signature", dujiao.Sign(envOr("DUJIAO_CHANNEL_SECRET", "integration-secret"), http.MethodPost, path, timestamp, body))
	return request, nil
}

func percentile(values []time.Duration, percent int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := (len(values)*percent + 99) / 100
	if index > len(values) {
		index = len(values)
	}
	return values[index-1].Round(time.Microsecond)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
