package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"quick-query/internal/database"
	"quick-query/internal/profile"
	"quick-query/internal/ui"
)

func run() error {
	configPath := flag.String("config", "", "connection configuration file (default: OS user-config directory)")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments; use quick-query --help")
	}
	store, err := profile.DefaultStore()
	if err != nil {
		return fmt.Errorf("locate configuration directory: %w", err)
	}
	if *configPath != "" {
		store.Path = *configPath
	}
	model := ui.New(profile.Manager{Profiles: store, Secrets: profile.SystemKeychain{}}, database.Connect)
	defer model.Close()
	_, err = tea.NewProgram(model).Run()
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "quick-query:", err)
		os.Exit(1)
	}
}
