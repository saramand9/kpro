package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"runtime/debug"
)

func runVersion(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: kpro version")
	}
	resolvedVersion, resolvedCommit, resolvedDate := resolvedBuildMetadata()
	fmt.Fprintf(stdout, "kpro %s (commit %s, built %s, %s/%s, %s)\n", resolvedVersion, resolvedCommit, resolvedDate, runtime.GOOS, runtime.GOARCH, runtime.Version())
	return nil
}

func runDoctor(args []string, stdout, stderr io.Writer) error {
	return runDoctorWithLookPath(args, stdout, stderr, exec.LookPath)
}

type doctorLookPathFunc func(string) (string, error)

func runDoctorWithLookPath(args []string, stdout, stderr io.Writer, lookPath doctorLookPathFunc) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: kpro doctor")
	}
	fmt.Fprintf(stdout, "go: %s\nplatform: %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if info, ok := debug.ReadBuildInfo(); ok {
		moduleVersion := info.Main.Version
		if moduleVersion == "" {
			moduleVersion = "(devel)"
		}
		fmt.Fprintf(stdout, "module: %s %s\n", info.Main.Path, moduleVersion)
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				fmt.Fprintf(stdout, "revision: %s\n", setting.Value)
				break
			}
		}
	}
	fmt.Fprintln(stdout, "proto generation tools (required only when generating proto sources):")
	for _, tool := range []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"} {
		path, err := lookPath(tool)
		if err != nil {
			fmt.Fprintf(stdout, "%s: optional missing\n", tool)
			continue
		}
		fmt.Fprintf(stdout, "%s: available: %s\n", tool, path)
	}
	fmt.Fprintln(stdout, "status: ok")
	return nil
}
