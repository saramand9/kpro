// Command kpro contains developer tooling for KPro projects.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if err := execute(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "kpro:", err)
		os.Exit(1)
	}
}

func execute(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printUsage(stdout)
		return nil
	}
	switch args[0] {
	case "new":
		return runNew(args[1:], stdout, stderr)
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run 'kpro help'", args[0])
	}
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "KPro developer tools")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  kpro new <directory> [--module example.com/service] [--name service] [--kpro-replace path]")
	fmt.Fprintln(output, "  kpro version")
	fmt.Fprintln(output, "  kpro doctor")
}
