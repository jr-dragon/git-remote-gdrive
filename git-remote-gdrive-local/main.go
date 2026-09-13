package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/jr-dragon/git-remote-gdrive/internal/localstore"
	"github.com/jr-dragon/git-remote-gdrive/internal/remotehelper"
	"github.com/jr-dragon/git-remote-gdrive/internal/repository"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "git-remote-gdrive-local:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: git-remote-gdrive-local <remote> [gdrive-local:///absolute/path]")
	}
	path, err := localstore.Path(args[len(args)-1])
	if err != nil {
		return err
	}
	h := remotehelper.Helper{Diagnostics: os.Stderr, OpenStore: func(context.Context) (repository.Store, error) {
		return localstore.New(path)
	}}
	return h.Run(ctx, input, output)
}
