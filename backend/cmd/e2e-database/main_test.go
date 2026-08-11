package main

import "testing"

func TestDatabaseURLReplacesDatabaseAndPreservesConnectionOptions(t *testing.T) {
	got, err := databaseURL("postgres://user:pass@127.0.0.1:15432/emby_auto?sslmode=disable", "emby_auto_e2e")
	if err != nil {
		t.Fatal(err)
	}
	want := "postgres://user:pass@127.0.0.1:15432/emby_auto_e2e?sslmode=disable"
	if got != want {
		t.Fatalf("databaseURL() = %q, want %q", got, want)
	}
}
