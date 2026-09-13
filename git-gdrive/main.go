package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/jr-dragon/git-remote-gdrive/internal/browser"
	"github.com/jr-dragon/git-remote-gdrive/internal/gdriveassets"
	"github.com/jr-dragon/git-remote-gdrive/internal/googleauth"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var output io.Writer = os.Stderr
	if len(os.Args) > 1 && (os.Args[1] == "asset-clean" || os.Args[1] == "asset-smudge") {
		output = os.Stdout
	}
	if err := run(ctx, os.Args[1:], os.Stdin, output); err != nil {
		fmt.Fprintln(os.Stderr, "git-gdrive:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(output, "Usage: git-gdrive config --client-file <desktop-oauth-client.json> [--manual]")
		fmt.Fprintln(output, "       git-gdrive install [--global]")
		return nil
	}
	if args[0] == "install" || args[0] == "asset-clean" || args[0] == "asset-smudge" {
		return gdriveassets.Run(ctx, args, input, output, nil)
	}
	if args[0] != "config" {
		return errors.New("unknown command; use git-gdrive config or install")
	}
	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	flags.SetOutput(output)
	clientFile := flags.String("client-file", os.Getenv("GIT_GDRIVE_CLIENT_FILE"), "Google Desktop app OAuth client JSON (or GIT_GDRIVE_CLIENT_FILE)")
	manual := flags.Bool("manual", false, "skip local callback server and paste the final redirect URL")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("config does not accept positional arguments")
	}
	if *clientFile == "" {
		return errors.New("provide --client-file or set GIT_GDRIVE_CLIENT_FILE to a Google Desktop app OAuth client JSON file")
	}
	path, err := googleauth.DefaultPath()
	if err != nil {
		return err
	}
	c, err := googleauth.Authenticate(ctx, googleauth.Options{ClientFile: *clientFile, Manual: *manual, Input: input, Output: output, OpenBrowser: browser.Open})
	if err != nil {
		return err
	}
	if err := googleauth.Save(path, c); err != nil {
		return fmt.Errorf("save credential: %w", err)
	}
	fmt.Fprintf(output, "Credentials saved to %s\n", path)
	return nil
}
