package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/onprs/emby-auto/backend/internal/maintenance"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(parent context.Context, args []string, output, errorOutput io.Writer) int {
	flags := flag.NewFlagSet("emby-auto-migrate-runtime-addresses", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	from := flags.String("from-host", "host.docker.internal", "原服务主机名")
	to := flags.String("to-host", "127.0.0.1", "目标回环 IP")
	backup := flags.String("backup", "", "首次应用时保存私有地址快照")
	restore := flags.String("restore", "", "恢复私有地址快照")
	apply := flags.Bool("apply", false, "实际应用，默认只读预览")
	expected := flags.Int("expected-changes", -1, "预期变更字段数量")
	containerHost := flags.String("container-host-address", "", "只在当前命令中使用的容器服务回环替代地址")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || (*apply && *backup == "" && *restore == "") ||
		(*backup != "" && *restore != "") || *expected < -1 || *expected > 2 {
		if err == flag.ErrHelp {
			_, _ = fmt.Fprintln(output, "用法：emby-auto-migrate-runtime-addresses [--from-host HOST --to-host IP] [--backup PATH|--restore PATH] [--apply]")
			return 0
		}
		maintenance.WriteFailure(errorOutput, "invalid_arguments")
		return 2
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	scope, err := maintenance.Open(ctx, *containerHost)
	if err != nil {
		maintenance.WriteFailure(errorOutput, "runtime_configuration_unavailable")
		return 1
	}
	defer scope.Close()
	result, err := scope.MigrateAddresses(ctx, *from, *to, *backup, *restore, *apply, *expected)
	if err != nil {
		maintenance.WriteFailure(errorOutput, "address_migration_unverified")
		return 1
	}
	if err := maintenance.WriteJSON(output, result); err != nil {
		return 1
	}
	return 0
}
