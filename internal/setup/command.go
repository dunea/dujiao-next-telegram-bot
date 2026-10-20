// Package setup provides explicit, offline installation operations.
package setup

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

var ErrInvalid = errors.New("setup failed; see sanitized checks")

const usage = `Offline setup:
  dujiao-next-telegram-bot setup init [--env-file .env]
  dujiao-next-telegram-bot setup check [--env-file PATH] [--profile runtime|compose-redis] [--compare-env PATH]
init creates a NEW 0600 file with an independent random key; edit merchant credentials privately.
check uses only the explicit file, or process environment when no file is given.
No shell evaluation, network calls, service startup, data migration or automatic rollback.
`

func Run(args []string, out io.Writer, env func(string) string) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		_, err := fmt.Fprint(out, usage)
		return err
	}
	op := args[0]
	if op != "init" && op != "check" {
		return ErrInvalid
	}
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // Flag errors may contain a secret passed by mistake.
	defaultPath := ""
	if op == "init" {
		defaultPath = ".env"
	}
	path := flags.String("env-file", defaultPath, "explicit environment file")
	profile, compare := "runtime", ""
	if op == "check" {
		flags.StringVar(&profile, "profile", "runtime", "runtime or compose-redis")
		flags.StringVar(&compare, "compare-env", "", "previous private environment file")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprint(out, usage)
			return err
		}
		return ErrInvalid
	}
	if flags.NArg() != 0 {
		return ErrInvalid
	}
	emptyExplicitPath := false
	flags.Visit(func(f *flag.Flag) {
		if (f.Name == "env-file" && strings.TrimSpace(*path) == "") || (f.Name == "compare-env" && strings.TrimSpace(compare) == "") {
			emptyExplicitPath = true
		}
	})
	if emptyExplicitPath {
		return ErrInvalid
	}
	emit := func(r Report) error {
		if err := json.NewEncoder(out).Encode(r); err != nil {
			return ErrInvalid
		}
		if r.Status == "rejected" || op == "check" && !r.ConfigurationOK {
			return ErrInvalid
		}
		return nil
	}
	fail := func(issue *FileIssue) error {
		return emit(Report{Schema: 1, Operation: op, Status: "rejected", NetworkChecks: "not_run", Checks: []Check{{Name: "environment", Status: "failed", Code: issue.Code, Line: issue.Line}}})
	}
	if op == "check" && profile != "runtime" && profile != "compose-redis" {
		return emit(Diagnose(nil, profile, "not_read"))
	}
	if op == "init" {
		if *path == "" {
			return fail(&FileIssue{Code: "new_file_required"})
		}
		if issue := InitEnv(*path); issue != nil {
			return fail(issue)
		}
		return emit(Report{Schema: 1, Operation: op, Status: "created", NetworkChecks: "not_run", Checks: []Check{{Name: "environment", Status: "warning", Code: "edit_credentials_then_check"}}})
	}
	values := make(map[string]string)
	source := "process"
	if *path != "" {
		var issue *FileIssue
		values, issue = ReadEnv(*path)
		if issue != nil {
			return fail(issue)
		}
		source = "file"
	} else {
		for name := range variableNames() {
			values[name] = env(name)
		}
	}
	r := Diagnose(values, profile, source)
	if compare != "" {
		previous, issue := ReadEnv(compare)
		if issue != nil {
			r.add("comparison", "failed", issue.Code)
		} else {
			Compare(&r, values, previous)
		}
	}
	return emit(r)
}
