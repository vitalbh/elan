package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// The --json output is the contract of frontends (the herdr mod): only the
// JSON document goes to stdout; on error, {"error": "..."} on stdout and
// exit 1. Documented in docs/cli-json.md.

var stdout io.Writer = os.Stdout

// hasJSONFlag reports whether args ask for JSON output, before the flags are
// parsed (to report a flag error as JSON).
func hasJSONFlag(args []string) bool {
	for _, a := range args {
		if a == "--json" || a == "-json" || strings.HasPrefix(a, "--json=true") || strings.HasPrefix(a, "-json=true") {
			return true
		}
	}
	return false
}

// parseFlags parses args, reporting a bad flag as JSON when --json is among
// them. ok false means exit with code.
func parseFlags(fs *flag.FlagSet, args []string) (ok bool, code int) {
	fs.Init(fs.Name(), flag.ContinueOnError)
	if hasJSONFlag(args) {
		fs.SetOutput(io.Discard)
	}
	err := fs.Parse(args)
	switch {
	case err == nil:
		return true, 0
	case errors.Is(err, flag.ErrHelp):
		return false, 0
	case hasJSONFlag(args):
		return false, jsonError(err)
	default:
		return false, 2
	}
}

// parseArgs is parseFlags allowing flags before and after the positional
// arguments, which it returns.
func parseArgs(fs *flag.FlagSet, args []string) (positional []string, ok bool, code int) {
	rest := args
	for {
		if ok, code := parseFlags(fs, rest); !ok {
			return nil, false, code
		}
		if fs.NArg() == 0 {
			return positional, true, 0
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// jsonError prints {"error": msg} to stdout and returns exit code 1.
func jsonError(err error) int {
	_ = printJSON(map[string]string{"error": err.Error()})
	return 1
}

// finish ends a command: with asJSON, prints v (or the error) as JSON;
// otherwise runs human (when there is no error) and prints the error to
// stderr.
func finish(asJSON bool, v any, err error, human func()) int {
	if asJSON {
		if err != nil {
			return jsonError(err)
		}
		if err := printJSON(v); err != nil {
			fmt.Fprintln(os.Stderr, "erro:", err)
			return 1
		}
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	if human != nil {
		human()
	}
	return 0
}
