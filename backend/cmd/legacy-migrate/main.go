package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/legacymigration"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/mediatools"
)

const applyConfirmation = "APPLY_LEGACY_MIGRATION"

type pathMapFlags []legacymigration.PathMapping

func (flags *pathMapFlags) String() string {
	values := make([]string, 0, len(*flags))
	for _, mapping := range *flags {
		values = append(values, mapping.From+"="+mapping.To)
	}
	return strings.Join(values, ",")
}

func (flags *pathMapFlags) Set(value string) error {
	from, to, found := strings.Cut(value, "=")
	if !found || strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
		return fmt.Errorf("path map must use non-empty OLD=NEW values")
	}
	*flags = append(*flags, legacymigration.PathMapping{From: strings.TrimSpace(from), To: strings.TrimSpace(to)})
	return nil
}

type commandOptions struct {
	runtimeDirectory    string
	legacyDatabaseURL   string
	targetDatabaseURL   string
	profileID           string
	profileExtension    string
	ffprobePath         string
	reportPath          string
	expectedFingerprint string
	confirmation        string
	apply               bool
	verifyFiles         bool
	failAfter           int
	pathMappings        pathMapFlags
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		logger.Error("legacy migration failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	options, err := parseFlags(arguments)
	if err != nil {
		return err
	}
	startedAt := time.Now().UTC()
	runID := uuid.New()
	report := legacymigration.Report{
		Version: legacymigration.ReportVersion, RunID: runID, Mode: "dry-run", StartedAt: startedAt,
		Inventory: map[string]int{}, Issues: []legacymigration.Issue{},
	}
	if options.apply {
		report.Mode = "apply"
	}
	writeFinalReport := func(operationErr error) error {
		report.CompletedAt = time.Now().UTC()
		report.Succeeded = operationErr == nil
		if operationErr != nil {
			report.ErrorCode, report.ErrorMessage = commandErrorDetails(operationErr)
		}
		if reportErr := legacymigration.WriteReport(options.reportPath, report); reportErr != nil {
			if operationErr != nil {
				return errors.Join(operationErr, reportErr)
			}
			return reportErr
		}
		return operationErr
	}

	var source legacymigration.Source
	switch {
	case options.runtimeDirectory != "":
		source = legacymigration.RuntimeJSONSource{Directory: options.runtimeDirectory}
		report.SourceKind = "runtime_json"
	case options.legacyDatabaseURL != "":
		source = legacymigration.PostgresSource{DatabaseURL: options.legacyDatabaseURL}
		report.SourceKind = "legacy_postgres"
	default:
		return writeFinalReport(fmt.Errorf("exactly one legacy source is required"))
	}
	if options.legacyDatabaseURL != "" && sameDatabaseURL(options.legacyDatabaseURL, options.targetDatabaseURL) {
		return writeFinalReport(fmt.Errorf("legacy source and target database URLs must differ"))
	}

	profileExtension := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(options.profileExtension), "."))
	var artifactProfile *legacymigration.ArtifactProfile
	var artifactProbe legacymigration.ArtifactProbeFunc
	var pool *pgxpool.Pool
	profileUUID := uuid.Nil
	if options.profileID != "" {
		profileUUID, err = uuid.Parse(options.profileID)
		if err != nil {
			return writeFinalReport(fmt.Errorf("parse --profile-id: %w", err))
		}
	}
	if options.targetDatabaseURL != "" && profileUUID != uuid.Nil {
		if strings.TrimSpace(options.ffprobePath) == "" {
			return writeFinalReport(fmt.Errorf("--ffprobe is required with a target profile"))
		}
		pool, err = database.Open(ctx, options.targetDatabaseURL)
		if err != nil {
			return writeFinalReport(err)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			return writeFinalReport(fmt.Errorf("connect target PostgreSQL: %w", err))
		}
		var videoCodec, container, targetExtension, audioPolicy, audioCodec string
		err := pool.QueryRow(ctx, `
SELECT video_codec, container, file_extension, audio_policy, COALESCE(audio_codec, '')
FROM transcode_profiles
WHERE id = $1 AND active
`, profileUUID).Scan(&videoCodec, &container, &targetExtension, &audioPolicy, &audioCodec)
		if errors.Is(err, pgx.ErrNoRows) {
			return writeFinalReport(fmt.Errorf("selected transcode profile does not exist or is inactive"))
		}
		if err != nil {
			return writeFinalReport(fmt.Errorf("load selected transcode profile: %w", err))
		}
		if profileExtension != "" && profileExtension != targetExtension {
			return writeFinalReport(fmt.Errorf("--profile-extension does not match the selected target profile"))
		}
		artifactProfile, err = legacymigration.NewArtifactProfile(videoCodec, container, targetExtension, audioPolicy, audioCodec)
		if err != nil {
			return writeFinalReport(fmt.Errorf("validate selected transcode profile: %w", err))
		}
		tools := mediatools.New(nil)
		artifactProbe = func(probeContext context.Context, path string) (domainProbe domain.MediaProbe, probeErr error) {
			return tools.Probe(probeContext, options.ffprobePath, path)
		}
		profileExtension = targetExtension
	}
	if profileExtension == "" {
		return writeFinalReport(fmt.Errorf("--profile-extension, or --target-database-url with --profile-id, is required"))
	}

	snapshot, err := source.Load(ctx)
	if err != nil {
		return writeFinalReport(err)
	}
	if options.apply && snapshot.DatabaseIdentity != "" && pool != nil {
		var targetDatabase, targetAddress string
		var targetPort int
		if err := pool.QueryRow(ctx, `
SELECT current_database(), COALESCE(inet_server_addr()::text, 'local'), COALESCE(inet_server_port(), 0)
`).Scan(&targetDatabase, &targetAddress, &targetPort); err != nil {
			return writeFinalReport(fmt.Errorf("identify target PostgreSQL: %w", err))
		}
		targetIdentity := fmt.Sprintf("%s:%d/%s", targetAddress, targetPort, targetDatabase)
		if targetIdentity == snapshot.DatabaseIdentity {
			return writeFinalReport(fmt.Errorf("legacy source and target resolve to the same PostgreSQL database"))
		}
	}
	report.Inventory = snapshot.Inventory
	plan, err := legacymigration.BuildPlan(ctx, snapshot, legacymigration.PlanOptions{
		ProfileExtension: profileExtension,
		VerifyFiles:      options.verifyFiles,
		PathMappings:     options.pathMappings,
		ArtifactProfile:  artifactProfile,
		Probe:            artifactProbe,
	})
	if err != nil {
		return writeFinalReport(err)
	}
	report.Fingerprint = hex.EncodeToString(plan.Fingerprint)
	report.Counts = plan.Counts
	report.Issues = plan.Issues

	if options.apply {
		if options.confirmation != applyConfirmation {
			return writeFinalReport(fmt.Errorf("--confirm %s is required for apply", applyConfirmation))
		}
		if pool == nil || profileUUID == uuid.Nil {
			return writeFinalReport(fmt.Errorf("--target-database-url and --profile-id are required for apply"))
		}
		expected, decodeErr := hex.DecodeString(strings.TrimSpace(options.expectedFingerprint))
		if decodeErr != nil || len(expected) != 32 {
			return writeFinalReport(fmt.Errorf("--expected-fingerprint from a completed dry-run is required for apply"))
		}
		if !strings.EqualFold(hex.EncodeToString(expected), report.Fingerprint) {
			return writeFinalReport(fmt.Errorf("legacy snapshot differs from the dry-run fingerprint"))
		}
		result, applyErr := legacymigration.Apply(ctx, pool, plan, legacymigration.ApplyOptions{
			RunID: runID, ProfileID: profileUUID, FailAfter: options.failAfter,
		})
		report.Counts = result.Counts
		if applyErr != nil {
			return writeFinalReport(applyErr)
		}
	}
	if err := writeFinalReport(nil); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "mode=%s succeeded=true fingerprint=%s discovered=%d tasks=%d rss=%d invalid=%d skipped=%d unchanged=%d\n",
		report.Mode, report.Fingerprint, report.Counts.Discovered, report.Counts.PlannedTasks, report.Counts.PlannedRSS,
		report.Counts.Invalid, report.Counts.Skipped, report.Counts.Unchanged)
	return err
}

func parseFlags(arguments []string) (commandOptions, error) {
	options := commandOptions{targetDatabaseURL: os.Getenv("DATABASE_URL"), ffprobePath: "ffprobe", verifyFiles: true}
	set := flag.NewFlagSet("legacy-migrate", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&options.runtimeDirectory, "runtime-dir", "", "legacy runtime JSON directory")
	set.StringVar(&options.legacyDatabaseURL, "legacy-database-url", "", "read-only legacy PostgreSQL URL")
	set.StringVar(&options.targetDatabaseURL, "target-database-url", options.targetDatabaseURL, "target PostgreSQL URL")
	set.StringVar(&options.profileID, "profile-id", "", "active target transcode profile UUID")
	set.StringVar(&options.profileExtension, "profile-extension", "", "dry-run profile file extension")
	set.StringVar(&options.ffprobePath, "ffprobe", options.ffprobePath, "ffprobe executable used for target profile validation")
	set.StringVar(&options.reportPath, "report", "", "JSON report output path")
	set.StringVar(&options.expectedFingerprint, "expected-fingerprint", "", "fingerprint produced by dry-run")
	set.StringVar(&options.confirmation, "confirm", "", "apply confirmation token")
	set.BoolVar(&options.apply, "apply", false, "apply the plan; default is dry-run")
	set.BoolVar(&options.verifyFiles, "verify-files", true, "hash and validate legacy artifact files")
	set.IntVar(&options.failAfter, "fail-after", 0, "inject an apply failure after N new records")
	set.Var(&options.pathMappings, "path-map", "rewrite a legacy path prefix as OLD=NEW; repeatable")
	if err := set.Parse(arguments); err != nil {
		return commandOptions{}, err
	}
	if set.NArg() != 0 {
		return commandOptions{}, fmt.Errorf("unexpected positional arguments")
	}
	if (options.runtimeDirectory == "") == (options.legacyDatabaseURL == "") {
		return commandOptions{}, fmt.Errorf("exactly one of --runtime-dir or --legacy-database-url is required")
	}
	if options.reportPath == "" {
		return commandOptions{}, fmt.Errorf("--report is required")
	}
	if options.failAfter < 0 {
		return commandOptions{}, fmt.Errorf("--fail-after must be nonnegative")
	}
	if !options.apply && options.failAfter != 0 {
		return commandOptions{}, fmt.Errorf("--fail-after is only valid with --apply")
	}
	if options.apply && !options.verifyFiles {
		return commandOptions{}, fmt.Errorf("--verify-files=false is not allowed with --apply")
	}
	return options, nil
}

func sameDatabaseURL(left, right string) bool {
	return strings.TrimRight(strings.TrimSpace(left), "/") == strings.TrimRight(strings.TrimSpace(right), "/")
}

func commandErrorDetails(err error) (string, string) {
	var applyErr *legacymigration.ApplyError
	if errors.As(err, &applyErr) {
		return applyErr.Code, applyErr.Message
	}
	return "migration_command_failed", err.Error()
}
