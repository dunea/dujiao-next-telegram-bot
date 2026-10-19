// Command compatcheck checks read paths only; it never provisions identities,
// posts a heartbeat, creates orders, redeems cards or transfers money.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/compatibility"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)) }
func run(args []string, out, errOut io.Writer, env func(string) string) int {
	flags := flag.NewFlagSet("compatcheck", flag.ContinueOnError)
	flags.SetOutput(errOut)
	userID := flags.String("user-id", "", "optional existing test user's numeric Telegram ID (no provisioning)")
	timeout := flags.Duration("timeout", 5*time.Second, "per-request timeout, 1s..30s")
	verify := flags.Bool("verify-upstream", false, "verify pinned public upstream source hashes; no merchant credentials needed")
	matrix := flags.Bool("matrix", false, "print reviewed versions and evidence, offline")
	if flags.Parse(args) != nil {
		return 2
	}
	if flags.NArg() != 0 || *timeout < time.Second || *timeout > 30*time.Second {
		fmt.Fprintln(errOut, "invalid arguments")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if *matrix && *verify {
		fmt.Fprintln(errOut, "choose only one mode")
		return 2
	}
	if *matrix {
		if json.NewEncoder(out).Encode(compatibility.Versions()) != nil {
			return 1
		}
		return 0
	}
	if *verify {
		results := compatibility.VerifySources(ctx)
		if json.NewEncoder(out).Encode(results) != nil {
			return 1
		}
		for _, result := range results {
			if result.Status != "matched" {
				return 1
			}
		}
		return 0
	}
	if *userID != "" {
		if value, err := strconv.ParseInt(*userID, 10, 64); err != nil || value <= 0 {
			fmt.Fprintln(errOut, "user-id must be a positive integer")
			return 2
		}
	}
	client, err := dujiao.NewReadOnlyClient(env("DUJIAO_BASE_URL"), env("DUJIAO_CHANNEL_KEY"), env("DUJIAO_CHANNEL_SECRET"), *timeout)
	if err != nil {
		fmt.Fprintln(errOut, "invalid or missing Dujiao configuration (values omitted)")
		return 2
	}
	report := compatibility.Probe(ctx, client, *userID)
	if json.NewEncoder(out).Encode(report) != nil {
		return 1
	}
	if !report.ReadChecksOK {
		return 1
	}
	return 0
}
