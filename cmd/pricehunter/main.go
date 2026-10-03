// Command pricehunter is the local/server-mode entrypoint.
//
//	pricehunter check <url>   fetch a page and print the extracted price
//	pricehunter serve         run API + scheduler + workers (local or container)
package main

import (
	"fmt"
	"os"
)

const usage = `Price Hunter

Usage:
  pricehunter check [flags] <url>   Fetch a product page and print its price
  pricehunter serve                 Run the API, scheduler and workers
  pricehunter seed  [flags]         Create demo products with price history
  pricehunter dlq   peek|redrive    Inspect or replay dead-lettered checks

Run "pricehunter <command> -h" for command flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "check":
		err = runCheck(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "seed":
		err = runSeed(os.Args[2:])
	case "dlq":
		err = runDLQ(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
