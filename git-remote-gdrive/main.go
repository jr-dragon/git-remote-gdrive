package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/jr-dragon/git-remote-gdrive/internal/drive"
	"github.com/jr-dragon/git-remote-gdrive/internal/googleauth"
	"github.com/jr-dragon/git-remote-gdrive/internal/remotehelper"
	"github.com/jr-dragon/git-remote-gdrive/internal/repository"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "git-remote-gdrive:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: git-remote-gdrive <remote> [gdrive://{folder_id}]")
	}
	id, err := remotehelper.FolderID(args[len(args)-1])
	if err != nil {
		return err
	}
	h := remotehelper.Helper{Diagnostics: os.Stderr, OpenStore: func(ctx context.Context) (repository.Store, error) {
		path, err := googleauth.DefaultPath()
		if err != nil {
			return nil, err
		}
		client, err := googleauth.Client(ctx, path)
		if err != nil {
			return nil, err
		}
		api, err := drive.ConfiguredAPI(ctx, client)
		if err != nil {
			return nil, err
		}
		return &drive.Store{Client: api, Root: id}, nil
	}}
	return h.Run(ctx, input, output)
}
