// Command agent-receipts reports what coding agents left behind.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Audarya07/agent-receipts/internal/env"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "%s %s\n", env.AppName, env.Version)
		return 0
	default:
		fmt.Fprintf(stderr, "%s: unknown command %q\n", env.AppName, args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "usage: %s <command>\n\ncommands:\n  report   show what agents left behind\n  clean    review and remove leftovers (interactive)\n  stats    show totals over time\n  hook     record agent activity (for agent hooks)\n  version  print version\n", env.AppName)
}
