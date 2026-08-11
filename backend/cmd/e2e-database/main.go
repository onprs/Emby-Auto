package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	action := flag.String("action", "reset", "reset or drop the E2E database")
	baseURL := flag.String("base-url", os.Getenv("TEST_DATABASE_URL"), "administrator PostgreSQL URL")
	name := flag.String("name", "emby_auto_e2e", "database name")
	flag.Parse()
	if *baseURL == "" {
		*baseURL = os.Getenv("DATABASE_URL")
	}
	if *baseURL == "" || !validIdentifier(*name) {
		fmt.Fprintln(os.Stderr, "a base URL and a simple database name are required")
		os.Exit(2)
	}

	config, err := pgx.ParseConfig(*baseURL)
	if err != nil {
		fatal(err)
	}
	databaseName := *name
	config.Database = "postgres"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		fatal(err)
	}
	defer func() { _ = connection.Close(context.Background()) }()

	quoted := pgx.Identifier{databaseName}.Sanitize()
	_, _ = connection.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", databaseName)
	if _, err := connection.Exec(ctx, "DROP DATABASE IF EXISTS "+quoted); err != nil {
		fatal(err)
	}
	if *action == "reset" {
		if _, err := connection.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
			fatal(err)
		}
	} else if *action != "drop" {
		fmt.Fprintln(os.Stderr, "action must be reset or drop")
		os.Exit(2)
	}

	outputURL, err := databaseURL(*baseURL, databaseName)
	if err != nil {
		fatal(err)
	}
	fmt.Println(outputURL)
}

func databaseURL(baseURL, databaseName string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return "", fmt.Errorf("base URL must be a PostgreSQL URL")
	}
	parsed.Path = "/" + databaseName
	return parsed.String(), nil
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return !strings.HasPrefix(value, "pg_")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
