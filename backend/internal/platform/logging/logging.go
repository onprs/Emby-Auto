package logging

import (
	"fmt"
	"log/slog"
	"os"
)

// New creates the process JSON logger at the configured level.
func New(levelText string) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(levelText)); err != nil {
		return nil, fmt.Errorf("parse LOG_LEVEL: %w", err)
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})), nil
}
