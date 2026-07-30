package main

import "io"

func runNew(args []string, stdout, stderr io.Writer) error {
	return scaffoldNew(args, stdout, stderr)
}
