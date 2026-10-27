package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/git-pkgs/purl2swhid/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	err := cli.Root().ExecuteContext(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
