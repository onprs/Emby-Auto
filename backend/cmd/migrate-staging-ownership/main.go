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
	flags := flag.NewFlagSet("emby-auto-migrate-staging-ownership", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	uid := flags.Int("uid", 0, "目标媒体账户 UID")
	backup := flags.String("backup", "", "应用前保存私有目录元数据快照")
	restore := flags.String("restore", "", "恢复私有目录元数据快照")
	apply := flags.Bool("apply", false, "实际应用，默认只读预览")
	expected := flags.Int("expected-tasks", -1, "预期迁移任务数量")
	containerHost := flags.String("container-host-address", "", "当前命令的容器服务回环替代地址")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *uid < 0 || uint64(*uid) >= 1<<32-1 || (*restore == "" && *uid == 0) ||
		(*apply && *backup == "" && *restore == "") || (*backup != "" && *restore != "") || *expected < -1 {
		if err == flag.ErrHelp {
			_, _ = fmt.Fprintln(output, "用法：emby-auto-migrate-staging-ownership --uid UID [--backup PATH|--restore PATH] [--expected-tasks N] [--apply]")
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
	result, err := scope.MigrateStaging(ctx, *uid, *backup, *restore, *apply, *expected)
	if err != nil {
		maintenance.WriteFailure(errorOutput, "staging_migration_unverified")
		return 1
	}
	if err := maintenance.WriteJSON(output, result); err != nil {
		return 1
	}
	return 0
}
