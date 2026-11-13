package main

import (
	"context"
	"io"
)

func cmdRun(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	return exitFailure
}
