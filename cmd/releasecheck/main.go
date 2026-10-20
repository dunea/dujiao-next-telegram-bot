// Command releasecheck verifies local candidate/stable archives without extracting
// or executing them, loading .env, or connecting to any service.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/dunea/dujiao-next-telegram-bot/internal/releaseartifact"
)

func main() {
	dir := flag.String("dir", "", "local release directory")
	flag.Parse()
	if *dir == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "releasecheck requires -dir")
		os.Exit(2)
	}
	report, err := releaseartifact.Check(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "release validation failed:", err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
}
