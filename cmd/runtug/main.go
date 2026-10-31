package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mgh3326/runtug/internal/runtug"
)

var version = "dev"
var commit = "unknown"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Printf("runtug %s (%s)\n", version, commit)
		return
	}
	runtug.BuildVersion, runtug.BuildCommit = version, commit
	os.Exit(runtug.Main(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
