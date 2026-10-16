package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/maintenance"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(parent context.Context, args []string, output, errorOutput io.Writer) int {
	flags := flag.NewFlagSet("emby-auto-background-runtime", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	state := flags.String("state", "status", "status、stopped 或 running")
	timeout := flags.Duration("timeout", 3*time.Minute, "操作期限")
	containerHost := flags.String("container-host-address", "", "迁移期间将容器服务地址映射到回环 IP")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		if err == flag.ErrHelp {
			_, _ = fmt.Fprintln(output, "用法：emby-auto-background-runtime --state status|stopped|running [--timeout 3m] [--container-host-address 127.0.0.1]")
			return 0
		}
		maintenance.WriteFailure(errorOutput, "invalid_arguments")
		return 2
	}
	if (*state != "status" && *state != "stopped" && *state != "running") || *timeout <= 0 || *timeout > 10*time.Minute {
		maintenance.WriteFailure(errorOutput, "invalid_arguments")
		return 2
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	scope, err := maintenance.Open(ctx, *containerHost)
	if err != nil {
		maintenance.WriteFailure(errorOutput, "runtime_configuration_unavailable")
		return 1
	}
	defer scope.Close()
	var requested *domain.BackgroundRuntimeState
	if *state != "status" {
		value := domain.BackgroundRuntimeState(*state)
		requested = &value
	}
	status, err := scope.Runtime(ctx, requested)
	if err != nil {
		maintenance.WriteFailure(errorOutput, "runtime_transition_unverified")
		return 1
	}
	if err := maintenance.WriteJSON(output, status); err != nil {
		return 1
	}
	return 0
}
