package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "load configuration: %v\n", err)
			os.Exit(2)
		}
		databaseURL = cfg.DatabaseURL
	}
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL or completed bootstrap configuration is required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()
	status, err := database.CheckMigrationStatus(ctx, pool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check migrations: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("application: %d/%d dirty=%t\n", status.ApplicationCurrent, status.ApplicationLatest, status.ApplicationDirty)
	fmt.Printf("river: %d/%d\n", status.RiverCurrent, status.RiverLatest)
	if err := database.RequireCurrentMigrations(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
