// Command kpro contains developer tooling for KPro projects.
package main

import (
	"errors"
	"flag"
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
	var err error
	switch args[0] {
	case "new":
		err = runNew(args[1:], stdout, stderr)
	case "add":
		err = runAdd(args[1:], stdout, stderr)
	case "validate":
		err = runValidate(args[1:], stdout, stderr)
	case "generate":
		err = runGenerate(args[1:], stdout, stderr)
	case "list":
		err = runList(args[1:], stdout, stderr)
	case "describe":
		err = runDescribe(args[1:], stdout, stderr)
	case "version":
		err = runVersion(args[1:], stdout, stderr)
	case "doctor":
		err = runDoctor(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run 'kpro help'", args[0])
	}
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "KPro developer tools")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  kpro new <directory> [--module example.com/service] [--name service] [--profile minimal|service] [--api http|grpc|message] [--transport tcp|kcp|quic|udp|websocket] [--template local-directory] [--kpro-replace path]")
	fmt.Fprintln(output, "  kpro add --api grpc [--root dir] [--config kpro.gen.yaml] [--dry-run]")
	fmt.Fprintln(output, "  kpro validate [--config kpro.gen.yaml] [--root dir]")
	fmt.Fprintln(output, "  kpro generate [--config kpro.gen.yaml] [--root dir] [--check] [--dry-run] [--allow-plugins]")
	fmt.Fprintln(output, "  kpro list api [--config kpro.gen.yaml] [--root dir] [--format text|json]")
	fmt.Fprintln(output, "  kpro describe service [--config kpro.gen.yaml] [--root dir] [--format text|json]")
	fmt.Fprintln(output, "  kpro version")
	fmt.Fprintln(output, "  kpro doctor")
}
