package legacymigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

var (
	legacyNamespace        = uuid.MustParse("a71c6b1e-bf64-5ee8-94c8-5d97b847c942")
	profileExtensionRegexp = regexp.MustCompile(`^[a-z0-9]+$`)
	torrentHashRegexp      = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func BuildPlan(ctx context.Context, snapshot Snapshot, options PlanOptions) (Plan, error) {
	extension := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(options.ProfileExtension), "."))
	if !profileExtensionRegexp.MatchString(extension) {
		return Plan{}, fmt.Errorf("profile extension must contain lowercase letters and digits")
	}
	profile, err := normalizeArtifactProfile(options.ArtifactProfile, extension)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{SourceKind: snapshot.SourceKind, SourceIdentity: snapshot.DatabaseIdentity, ProfileExtension: extension, ArtifactProfile: profile, Inventory: cloneInventory(snapshot.Inventory), Issues: make([]Issue, 0)}
	acquisitionIndexes := make(map[string]map[int]struct{})
	for _, record := range snapshot.Records {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		planned, item, issues, include := planTask(ctx, snapshot.SourceKind, record, options, extension, acquisitionIndexes)
		plan.Issues = append(plan.Issues, issues...)
		if item.LegacyID != "" {
			plan.Items = append(plan.Items, item)
		} else {
			plan.Counts.Invalid++
		}
		if !include {
			continue
		}
		plan.Tasks = append(plan.Tasks, planned)
		plan.Counts.PlannedTasks++
		plan.Counts.Events += len(planned.History)
		if planned.ArtifactSetID != uuid.Nil {
			plan.Counts.ArtifactPairs++
		}
	}
	for _, record := range snapshot.RSSDrafts {
		planned, item, issues, include := planSubscription(snapshot.SourceKind, record)
		plan.Issues = append(plan.Issues, issues...)
		if item.LegacyID != "" {
			plan.Items = append(plan.Items, item)
		} else {
			plan.Counts.Invalid++
		}
		if include {
			plan.Subscriptions = append(plan.Subscriptions, planned)
			plan.Counts.PlannedRSS++
			plan.Counts.Events++
		}
	}
	plan.Counts.Discovered = len(snapshot.Records) + len(snapshot.RSSDrafts)
	for _, item := range plan.Items {
		switch item.Status {
		case "skipped":
			plan.Counts.Skipped++
		case "invalid":
			plan.Counts.Invalid++
		}
	}
	sort.Slice(plan.Tasks, func(i, j int) bool { return plan.Tasks[i].LegacyID < plan.Tasks[j].LegacyID })
	sort.Slice(plan.Subscriptions, func(i, j int) bool { return plan.Subscriptions[i].LegacyID < plan.Subscriptions[j].LegacyID })
	sort.Slice(plan.Items, func(i, j int) bool {
		if plan.Items[i].SourceKind == plan.Items[j].SourceKind {
			return plan.Items[i].LegacyID < plan.Items[j].LegacyID
		}
		return plan.Items[i].SourceKind < plan.Items[j].SourceKind
	})
	manifest := struct {
		Version int
		Options struct {
			ProfileExtension string
			VerifyFiles      bool
			PathMappings     []PathMapping
			ArtifactProfile  *ArtifactProfile
		}
		SourceKind     string
		SourceIdentity string
		Tasks          []PlannedTask
		Subscriptions  []PlannedSubscription
		Items          []PlannedItem
		Inventory      map[string]int
		Issues         []Issue
		Counts         Counts
	}{
		Version: ReportVersion, SourceKind: plan.SourceKind, SourceIdentity: plan.SourceIdentity, Tasks: plan.Tasks,
		Subscriptions: plan.Subscriptions, Items: plan.Items, Inventory: plan.Inventory, Issues: plan.Issues, Counts: plan.Counts,
	}
	manifest.Options.ProfileExtension = extension
	manifest.Options.VerifyFiles = options.VerifyFiles
	manifest.Options.PathMappings = options.PathMappings
	manifest.Options.ArtifactProfile = profile
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return Plan{}, fmt.Errorf("encode migration plan manifest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	plan.Fingerprint = sum[:]
	return plan, nil
}

func planTask(
	ctx context.Context,
	sourceKind string,
	record Record,
	options PlanOptions,
	profileExtension string,
	acquisitionIndexes map[string]map[int]struct{},
) (PlannedTask, PlannedItem, []Issue, bool) {
	issues := make([]Issue, 0)
	fingerprint := recordFingerprint(record)
	itemSourceKind := sourceKind + "/task"
	legacyID := strings.TrimSpace(record.LegacyID)
	if legacyID == "" {
		issues = append(issues, Issue{SourceKind: itemSourceKind, Code: "legacy_id_missing", Message: "legacy task has no stable task ID"})
		return PlannedTask{}, PlannedItem{}, issues, false
	}
	item := PlannedItem{
		ID: deterministicID("migration-item", itemSourceKind, legacyID), SourceKind: itemSourceKind, LegacyID: legacyID,
		Fingerprint: fingerprint, Status: "imported", ResourceType: "episode_task", Payload: sanitizeLegacyPayload(record.Payload),
	}
	payload := record.Payload
	statusStage := strings.ToLower(textFrom(payload, "status_stage", "statusStage"))
	taskType := strings.ToLower(textFrom(payload, "task_type", "taskType"))
	if (statusStage == "rss_scan" || taskType == "rss-follow") && positiveInt(valueFrom(payload, "episode", "source_episode")) == 0 {
		item.Status = "skipped"
		item.ResourceType = ""
		item.ResourceID = uuid.Nil
		item.ErrorCode = "tracking_task_not_episode"
		item.ErrorMessage = "RSS tracking parent is represented by a subscription, not an episode task"
		issues = append(issues, Issue{SourceKind: itemSourceKind, LegacyID: legacyID, Code: item.ErrorCode, Message: item.ErrorMessage})
		return PlannedTask{}, item, issues, false
	}

	seriesTitle := firstText(
		textFrom(payload, "canonical_series", "canonicalSeries"),
		textFrom(payload, "series"),
		textFrom(payload, "title"),
	)
	if seriesTitle == "" {
		item.Status = "invalid"
		item.ResourceType = ""
		item.ErrorCode = "series_title_missing"
		item.ErrorMessage = "legacy episode task has no series title"
		issues = append(issues, Issue{SourceKind: itemSourceKind, LegacyID: legacyID, Code: item.ErrorCode, Message: item.ErrorMessage})
		return PlannedTask{}, item, issues, false
	}
	tmdb := objectValue(payload["tmdb"])
	tmdbID := int64(positiveInt(firstValue(tmdb["tmdb_id"], payload["tmdb_id"], payload["tmdbId"])))
	seriesKey := legacySeriesKey(seriesTitle, tmdbID)
	seriesID := deterministicID("series", seriesKey)
	mappingProfileID := deterministicID("mapping-profile", seriesKey)
	source := objectValue(payload["source"])
	paths := objectValue(payload["paths"])
	artifacts := objectValue(payload["artifacts"])
	sourceSeason := positiveInt(firstValue(payload["source_season"], source["source_season"], payload["season"]))
	sourceEpisode := positiveInt(firstValue(payload["source_episode"], source["source_episode"], payload["episode"]))
	targetSeason, targetSeasonPresent := firstInteger(payload["tmdb_season"], source["tmdb_season"], payload["season"])
	targetEpisode, targetEpisodePresent := firstInteger(payload["tmdb_episode"], source["tmdb_episode"], payload["episode"])
	episodeTitle := firstText(
		textFrom(payload, "canonical_episode_title", "canonicalEpisodeTitle"),
		textFrom(payload, "episode_title", "episodeTitle"),
	)
	mappingValid := sourceSeason > 0 && sourceEpisode > 0 && targetSeasonPresent && targetSeason >= 0 && targetEpisodePresent && targetEpisode > 0 && episodeTitle != ""
	if !mappingValid {
		issues = append(issues, Issue{SourceKind: itemSourceKind, LegacyID: legacyID, Code: "mapping_context_incomplete", Message: "source/target coordinates or canonical episode title are incomplete"})
	}

	torrentHash := strings.ToLower(firstText(textFrom(source, "torrent_hash"), textFrom(payload, "hash")))
	if !torrentHashRegexp.MatchString(torrentHash) {
		torrentHash = ""
	}
	acquisitionKey := "task:" + legacyID
	if torrentHash != "" {
		acquisitionKey = "torrent:" + torrentHash
	}
	acquisitionID := deterministicID("acquisition", sourceKind, acquisitionKey)
	downloadID := deterministicID("download", sourceKind, acquisitionKey)
	preferredIndex := nonnegativeInt(firstValue(source["file_index"], artifacts["qb_file_index"]))
	fileIndex := reserveFileIndex(acquisitionIndexes, acquisitionKey, preferredIndex)
	sourcePath := firstText(textFrom(paths, "input"), textFrom(source, "file_name"), textFrom(source, "content_path"))
	sourceRelative := safeLegacyRelativePath(sourcePath, legacyID, profileExtension)
	sourceSize := regularFileSize(applyPathMappings(sourcePath, options.PathMappings))
	savePath := applyPathMappings(firstText(textFrom(source, "save_path"), textFrom(paths, "download_root"), textFrom(source, "download_root")), options.PathMappings)

	pair, pairIssues := inspectArtifactPair(ctx, payload, paths, artifacts, options, profileExtension)
	for _, issue := range pairIssues {
		issue.SourceKind = itemSourceKind
		issue.LegacyID = legacyID
		issues = append(issues, issue)
	}
	legacyStatus := strings.ToLower(textFrom(payload, "status"))
	reviewStatus := strings.ToLower(textFrom(payload, "review_status", "reviewStatus"))
	importStatus := strings.ToLower(textFrom(payload, "import_status", "importStatus"))
	state := "failed"
	videoState := "failed"
	subtitleState := "failed"
	errorCode := "legacy_migration_review_required"
	errorMessage := "legacy task requires manual repair after migration"
	reviewDecision := ""
	if pair.Valid {
		videoState = "video_ready"
		subtitleState = "ass_ready"
	}
	if mappingValid && pair.Valid {
		errorCode, errorMessage = "", ""
		switch {
		case (legacyStatus == "imported" || importStatus == "imported") && pair.Library:
			state = "imported"
			reviewDecision = "approved"
		case reviewStatus == "approved" || reviewStatus == "completed":
			state = "approved"
			reviewDecision = "approved"
		case reviewStatus == "rejected":
			state = "rejected"
			reviewDecision = "rejected"
		default:
			state = "awaiting_review"
		}
	}
	createdAt := nonzeroTime(record.CreatedAt, record.UpdatedAt)
	updatedAt := nonzeroTime(record.UpdatedAt, createdAt)
	planned := PlannedTask{
		ItemID: item.ID, LegacyID: legacyID, Fingerprint: fingerprint,
		SeriesKey: seriesKey, SeriesID: seriesID, TMDbSeriesID: tmdbID, SeriesTitle: seriesTitle, MappingProfileID: mappingProfileID,
		SourceSeason: sourceSeason, SourceEpisode: sourceEpisode, TargetSeason: targetSeason, TargetEpisode: targetEpisode, EpisodeTitle: episodeTitle,
		AcquisitionID: acquisitionID, AcquisitionKey: acquisitionKey, DownloadID: downloadID, TorrentHash: torrentHash, SavePath: savePath,
		SourceFileID: deterministicID("source-file", sourceKind, legacyID), SourceFileIndex: fileIndex, SourceRelativePath: sourceRelative, SourceFileSize: sourceSize,
		TaskID: deterministicID("episode-task", sourceKind, legacyID), TaskState: state, VideoState: videoState, SubtitleState: subtitleState,
		ErrorCode: errorCode, ErrorMessage: errorMessage, CreatedAt: createdAt, UpdatedAt: updatedAt,
		Payload: sanitizeLegacyPayload(payload), History: sanitizeLegacyHistory(record.History),
	}
	item.ResourceID = planned.TaskID
	if mappingValid {
		planned.SeasonID = deterministicID("season", seriesKey, strconv.Itoa(targetSeason))
		planned.EpisodeID = deterministicID("episode", seriesKey, strconv.Itoa(targetSeason), strconv.Itoa(targetEpisode))
		planned.MappingID = deterministicID("mapping", seriesKey, strconv.Itoa(sourceSeason), strconv.Itoa(sourceEpisode))
	}
	if pair.Valid {
		planned.ArtifactSetID = deterministicID("artifact-set", sourceKind, legacyID)
		planned.BaseName = pair.BaseName
		planned.VideoArtifactID = deterministicID("artifact-video", sourceKind, legacyID)
		planned.VideoPath, planned.VideoFormat, planned.VideoSize, planned.VideoChecksum = pair.VideoPath, pair.VideoFormat, pair.VideoSize, pair.VideoChecksum
		planned.SubtitleArtifactID = deterministicID("artifact-subtitle", sourceKind, legacyID)
		planned.SubtitlePath, planned.SubtitleSize, planned.SubtitleChecksum = pair.SubtitlePath, pair.SubtitleSize, pair.SubtitleChecksum
	}
	if reviewDecision != "" {
		planned.ReviewID = deterministicID("review", sourceKind, legacyID)
		planned.ReviewDecision = reviewDecision
		planned.ReviewNotes = textFrom(payload, "review_notes", "reviewNotes")
		planned.ReviewedAt = nonzeroTime(parseLegacyTime(textFrom(payload, "reviewed_at", "reviewedAt")), updatedAt)
	}
	if state == "imported" {
		planned.ImportID = deterministicID("import", sourceKind, legacyID)
		planned.LibraryVideoPath = pair.VideoPath
		planned.LibrarySubtitlePath = pair.SubtitlePath
		planned.ImportedAt = nonzeroTime(parseLegacyTime(textFrom(payload, "imported_at", "importedAt")), updatedAt)
	}
	return planned, item, issues, true
}

func planSubscription(sourceKind string, record Record) (PlannedSubscription, PlannedItem, []Issue, bool) {
	issues := make([]Issue, 0)
	fingerprint := recordFingerprint(record)
	itemSourceKind := sourceKind + "/rss_subscription"
	legacyID := strings.TrimSpace(record.LegacyID)
	if legacyID == "" {
		issues = append(issues, Issue{SourceKind: itemSourceKind, Code: "legacy_id_missing", Message: "legacy RSS draft has no stable draft ID"})
		return PlannedSubscription{}, PlannedItem{}, issues, false
	}
	item := PlannedItem{ID: deterministicID("migration-item", itemSourceKind, legacyID), SourceKind: itemSourceKind, LegacyID: legacyID, Fingerprint: fingerprint, Status: "imported", ResourceType: "rss_subscription", Payload: sanitizeLegacyPayload(record.Payload)}
	payload := record.Payload
	feedURL := firstText(textFrom(payload, "feed_url", "feedUrl"), textFrom(payload, "rss_feed_url", "rssFeedUrl"))
	if parsed, err := url.ParseRequestURI(feedURL); err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || urlContainsSensitiveData(parsed) {
		item.Status, item.ResourceType, item.ErrorCode, item.ErrorMessage = "invalid", "", "rss_feed_url_invalid", "legacy RSS draft has no credential-free HTTP(S) feed URL"
		issues = append(issues, Issue{SourceKind: itemSourceKind, LegacyID: legacyID, Code: item.ErrorCode, Message: item.ErrorMessage})
		return PlannedSubscription{}, item, issues, false
	}
	seriesTitle := firstText(textFrom(payload, "canonical_series", "canonicalSeries"), textFrom(payload, "series"), textFrom(payload, "title"))
	season := positiveInt(firstValue(payload["source_season"], payload["season"]))
	if seriesTitle == "" || season <= 0 {
		item.Status, item.ResourceType, item.ErrorCode, item.ErrorMessage = "invalid", "", "rss_mapping_context_incomplete", "legacy RSS draft has no series title or source season"
		issues = append(issues, Issue{SourceKind: itemSourceKind, LegacyID: legacyID, Code: item.ErrorCode, Message: item.ErrorMessage})
		return PlannedSubscription{}, item, issues, false
	}
	tmdb := objectValue(payload["tmdb"])
	tmdbID := int64(positiveInt(firstValue(tmdb["tmdb_id"], payload["tmdb_id"], payload["tmdbId"])))
	seriesKey := legacySeriesKey(seriesTitle, tmdbID)
	createdAt := nonzeroTime(record.CreatedAt, record.UpdatedAt)
	updatedAt := nonzeroTime(record.UpdatedAt, createdAt)
	planned := PlannedSubscription{
		ItemID: item.ID, LegacyID: legacyID, Fingerprint: fingerprint, SeriesKey: seriesKey,
		SeriesID: deterministicID("series", seriesKey), TMDbSeriesID: tmdbID, SeriesTitle: seriesTitle,
		MappingProfileID: deterministicID("mapping-profile", seriesKey), SubscriptionID: deterministicID("rss-subscription", sourceKind, legacyID),
		Name: firstText(textFrom(payload, "title"), seriesTitle+" legacy RSS"), FeedURL: feedURL, SourceSeason: season,
		Enabled: strings.ToLower(textFrom(payload, "status")) != "deleted", CreatedAt: createdAt, UpdatedAt: updatedAt, Payload: sanitizeLegacyPayload(payload),
	}
	item.ResourceID = planned.SubscriptionID
	return planned, item, issues, true
}

type artifactPair struct {
	Valid            bool
	Library          bool
	BaseName         string
	VideoPath        string
	VideoFormat      string
	VideoSize        int64
	VideoChecksum    []byte
	SubtitlePath     string
	SubtitleSize     int64
	SubtitleChecksum []byte
}

func inspectArtifactPair(ctx context.Context, payload, paths, artifacts map[string]any, options PlanOptions, profileExtension string) (artifactPair, []Issue) {
	libraryVideo := firstText(textFrom(paths, "library_video", "libraryVideo"), textFrom(artifacts, "library_video", "libraryVideo"))
	librarySubtitle := firstText(textFrom(paths, "library_sub", "librarySubtitle"), textFrom(artifacts, "library_sub", "librarySubtitle"))
	stagedVideo := firstText(textFrom(paths, "final_video", "finalVideo"), textFrom(artifacts, "output_video", "outputVideo"))
	stagedSubtitle := firstText(textFrom(paths, "final_sub", "finalSubtitle"), textFrom(artifacts, "output_sub", "outputSubtitle"))
	pair := artifactPair{Library: libraryVideo != "" && librarySubtitle != ""}
	if pair.Library {
		pair.VideoPath, pair.SubtitlePath = libraryVideo, librarySubtitle
	} else {
		pair.VideoPath, pair.SubtitlePath = stagedVideo, stagedSubtitle
	}
	pair.VideoPath = applyPathMappings(pair.VideoPath, options.PathMappings)
	pair.SubtitlePath = applyPathMappings(pair.SubtitlePath, options.PathMappings)
	if pair.VideoPath == "" || pair.SubtitlePath == "" {
		return artifactPair{}, []Issue{{Code: "artifact_pair_missing", Message: "legacy task does not reference both video and ASS artifacts"}}
	}
	videoExtension := strings.ToLower(strings.TrimPrefix(filepath.Ext(pair.VideoPath), "."))
	if videoExtension != profileExtension {
		return artifactPair{}, []Issue{{Code: "artifact_profile_mismatch", Message: "legacy video extension does not match the selected transcode profile"}}
	}
	if strings.ToLower(filepath.Ext(pair.SubtitlePath)) != ".ass" {
		return artifactPair{}, []Issue{{Code: "subtitle_format_invalid", Message: "legacy subtitle artifact is not ASS"}}
	}
	videoBase := strings.TrimSuffix(filepath.Base(pair.VideoPath), filepath.Ext(pair.VideoPath))
	subtitleBase := strings.TrimSuffix(filepath.Base(pair.SubtitlePath), filepath.Ext(pair.SubtitlePath))
	if videoBase == "" || videoBase != subtitleBase {
		return artifactPair{}, []Issue{{Code: "artifact_basename_mismatch", Message: "legacy video and subtitle do not share a basename"}}
	}
	pair.BaseName = videoBase
	pair.VideoFormat = videoFormatForExtension(videoExtension)
	if !options.VerifyFiles {
		return artifactPair{}, []Issue{{Code: "artifact_verification_disabled", Message: "artifact set was not planned because file verification is disabled"}}
	}
	videoSize, videoChecksum, err := migrationFileIdentity(ctx, pair.VideoPath)
	if err != nil {
		return artifactPair{}, []Issue{{Code: "video_artifact_unavailable", Message: "legacy video artifact is unavailable or unreadable"}}
	}
	subtitleSize, subtitleChecksum, err := migrationFileIdentity(ctx, pair.SubtitlePath)
	if err != nil {
		return artifactPair{}, []Issue{{Code: "subtitle_artifact_unavailable", Message: "legacy subtitle artifact is unavailable or unreadable"}}
	}
	if err := validateMigrationASS(pair.SubtitlePath); err != nil {
		return artifactPair{}, []Issue{{Code: "subtitle_artifact_invalid", Message: "legacy ASS subtitle does not contain the required sections and dialogue"}}
	}
	if options.ArtifactProfile == nil || options.Probe == nil {
		return artifactPair{}, []Issue{{Code: "artifact_profile_unverified", Message: "artifact set requires a target profile and ffprobe verification"}}
	}
	probe, err := options.Probe(ctx, pair.VideoPath)
	if err != nil {
		return artifactPair{}, []Issue{{Code: "video_profile_probe_failed", Message: "legacy video could not be probed against the target profile"}}
	}
	profile, err := normalizeArtifactProfile(options.ArtifactProfile, profileExtension)
	if err != nil || validateArtifactProfileProbe(*profile, probe) != nil {
		return artifactPair{}, []Issue{{Code: "video_profile_mismatch", Message: "legacy video container or codecs do not match the target profile"}}
	}
	pair.Valid = true
	pair.VideoSize, pair.VideoChecksum = videoSize, videoChecksum
	pair.SubtitleSize, pair.SubtitleChecksum = subtitleSize, subtitleChecksum
	return pair, nil
}

func validateMigrationASS(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	const maxASSBytes = 16 << 20
	content, err := io.ReadAll(io.LimitReader(file, maxASSBytes+1))
	if err != nil {
		return err
	}
	if len(content) > maxASSBytes {
		return fmt.Errorf("ASS subtitle exceeds %d bytes", maxASSBytes)
	}
	return domain.ValidateASS(content)
}

func validateArtifactProfileProbe(profile ArtifactProfile, probe domain.MediaProbe) error {
	containerMatched := false
	for _, actual := range probe.FormatNames {
		if slices.Contains(profile.ContainerNames, strings.ToLower(actual)) {
			containerMatched = true
			break
		}
	}
	if !containerMatched {
		return fmt.Errorf("video container does not match profile")
	}
	videoCodecs := make([]string, 0, 1)
	audioCodecs := make([]string, 0)
	for _, stream := range probe.Streams {
		switch stream.Type {
		case "video":
			videoCodecs = append(videoCodecs, strings.ToLower(stream.Codec))
		case "audio":
			audioCodecs = append(audioCodecs, strings.ToLower(stream.Codec))
		}
	}
	if len(videoCodecs) != 1 || videoCodecs[0] != profile.VideoCodec {
		return fmt.Errorf("video codec does not match profile")
	}
	if profile.AudioPolicy == "transcode" {
		for _, codec := range audioCodecs {
			if codec != profile.AudioCodec {
				return fmt.Errorf("audio codec does not match profile")
			}
		}
	}
	return nil
}

func migrationFileIdentity(ctx context.Context, path string) (int64, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return 0, nil, fmt.Errorf("artifact must be a non-empty regular file")
	}
	hasher := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return 0, nil, readErr
		}
	}
	return info.Size(), hasher.Sum(nil), nil
}

func applyPathMappings(path string, mappings []PathMapping) string {
	value := strings.TrimSpace(path)
	if value == "" {
		return ""
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	for _, mapping := range mappings {
		from := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(mapping.From), `\`, "/"), "/")
		to := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(mapping.To), `\`, "/"), "/")
		if from == "" || to == "" {
			continue
		}
		if normalized == from || strings.HasPrefix(normalized, from+"/") || strings.EqualFold(normalized, from) || strings.HasPrefix(strings.ToLower(normalized), strings.ToLower(from)+"/") {
			suffix := normalized[len(from):]
			return filepath.Clean(filepath.FromSlash(to + suffix))
		}
	}
	return filepath.Clean(filepath.FromSlash(normalized))
}

func reserveFileIndex(indexes map[string]map[int]struct{}, acquisitionKey string, preferred int) int {
	used := indexes[acquisitionKey]
	if used == nil {
		used = make(map[int]struct{})
		indexes[acquisitionKey] = used
	}
	if preferred < 0 {
		preferred = 0
	}
	for {
		if _, exists := used[preferred]; !exists {
			used[preferred] = struct{}{}
			return preferred
		}
		preferred++
	}
}

func safeLegacyRelativePath(sourcePath, legacyID, extension string) string {
	name := filepath.Base(filepath.Clean(strings.TrimSpace(sourcePath)))
	if name == "." || name == "" || name == string(filepath.Separator) {
		name = "source." + extension
	}
	name = strings.ReplaceAll(name, `\`, "_")
	return filepath.ToSlash(filepath.Join("legacy", hex.EncodeToString(hashText(legacyID)[:8]), name))
}

func regularFileSize(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 {
		return 0
	}
	return info.Size()
}

func videoFormatForExtension(extension string) string {
	switch extension {
	case "mkv":
		return "matroska"
	case "mp4", "m4v":
		return "mp4"
	case "webm":
		return "webm"
	default:
		return extension
	}
}

func NewArtifactProfile(videoCodec, container, extension, audioPolicy, audioCodec string) (*ArtifactProfile, error) {
	containers := map[string][]string{
		"matroska": {"matroska", "webm"},
		"mp4":      {"3g2", "3gp", "m4a", "mj2", "mov", "mp4"},
		"webm":     {"matroska", "webm"},
	}
	container = strings.ToLower(strings.TrimSpace(container))
	profile := &ArtifactProfile{
		VideoCodec: strings.ToLower(strings.TrimSpace(videoCodec)), ContainerNames: containers[container],
		FileExtension: strings.ToLower(strings.TrimPrefix(strings.TrimSpace(extension), ".")),
		AudioPolicy:   strings.ToLower(strings.TrimSpace(audioPolicy)), AudioCodec: strings.ToLower(strings.TrimSpace(audioCodec)),
	}
	return normalizeArtifactProfile(profile, profile.FileExtension)
}

func artifactProfilesEqual(left, right *ArtifactProfile) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.VideoCodec == right.VideoCodec && slices.Equal(left.ContainerNames, right.ContainerNames) &&
		left.FileExtension == right.FileExtension && left.AudioPolicy == right.AudioPolicy && left.AudioCodec == right.AudioCodec
}

func normalizeArtifactProfile(profile *ArtifactProfile, extension string) (*ArtifactProfile, error) {
	if profile == nil {
		return nil, nil
	}
	normalized := &ArtifactProfile{
		VideoCodec:     strings.ToLower(strings.TrimSpace(profile.VideoCodec)),
		ContainerNames: append([]string(nil), profile.ContainerNames...),
		FileExtension:  strings.ToLower(strings.TrimPrefix(strings.TrimSpace(profile.FileExtension), ".")),
		AudioPolicy:    strings.ToLower(strings.TrimSpace(profile.AudioPolicy)),
		AudioCodec:     strings.ToLower(strings.TrimSpace(profile.AudioCodec)),
	}
	for index := range normalized.ContainerNames {
		normalized.ContainerNames[index] = strings.ToLower(strings.TrimSpace(normalized.ContainerNames[index]))
	}
	slices.Sort(normalized.ContainerNames)
	normalized.ContainerNames = slices.Compact(normalized.ContainerNames)
	if normalized.VideoCodec == "" || len(normalized.ContainerNames) == 0 {
		return nil, fmt.Errorf("artifact profile requires a video codec and compatible containers")
	}
	if normalized.FileExtension != extension || !profileExtensionRegexp.MatchString(normalized.FileExtension) {
		return nil, fmt.Errorf("artifact profile extension does not match the migration profile extension")
	}
	if normalized.AudioPolicy != "copy" && normalized.AudioPolicy != "transcode" {
		return nil, fmt.Errorf("artifact profile audio policy must be copy or transcode")
	}
	if normalized.AudioPolicy == "transcode" && normalized.AudioCodec == "" {
		return nil, fmt.Errorf("artifact profile requires an audio codec for transcoding")
	}
	if normalized.AudioPolicy == "copy" {
		normalized.AudioCodec = ""
	}
	return normalized, nil
}

func recordFingerprint(record Record) []byte {
	payload := map[string]any{"payload": record.Payload, "history": record.History}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return sum[:]
}

func deterministicID(parts ...string) uuid.UUID {
	return uuid.NewSHA1(legacyNamespace, []byte(strings.Join(parts, "\x00")))
}

func legacySeriesKey(title string, tmdbID int64) string {
	if tmdbID > 0 {
		return "tmdb:" + strconv.FormatInt(tmdbID, 10)
	}
	return "title:" + strings.ToLower(strings.Join(strings.Fields(title), " "))
}

func hashText(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func textFrom(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := textValue(object[key]); value != "" {
			return value
		}
	}
	return ""
}

func textValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func valueFrom(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, exists := object[key]; exists && value != nil {
			return value
		}
	}
	return nil
}

func firstValue(values ...any) any {
	for _, value := range values {
		if value != nil && textValue(value) != "" {
			return value
		}
		if numberValue(value) != 0 {
			return value
		}
	}
	return nil
}

func firstInteger(values ...any) (int, bool) {
	for _, value := range values {
		switch typed := value.(type) {
		case float64:
			return int(typed), true
		case float32:
			return int(typed), true
		case int:
			return typed, true
		case int32:
			return int(typed), true
		case int64:
			return int(typed), true
		case json.Number:
			parsed, err := strconv.Atoi(typed.String())
			if err == nil {
				return parsed, true
			}
		case string:
			if strings.TrimSpace(typed) == "" {
				continue
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func positiveInt(value any) int {
	result := numberValue(value)
	if result <= 0 {
		return 0
	}
	return result
}

func nonnegativeInt(value any) int {
	result := numberValue(value)
	if result < 0 {
		return 0
	}
	return result
}

func numberValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case json.Number:
		parsed, _ := strconv.Atoi(typed.String())
		return parsed
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func objectValue(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

func cloneObject(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	var clone map[string]any
	_ = json.Unmarshal(encoded, &clone)
	if clone == nil {
		clone = map[string]any{}
	}
	return clone
}

func sanitizeLegacyPayload(value map[string]any) map[string]any {
	sanitized, _ := sanitizeLegacyValue("", value).(map[string]any)
	if sanitized == nil {
		return map[string]any{}
	}
	return sanitized
}

func sanitizeLegacyHistory(value []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(value))
	for _, item := range value {
		result = append(result, sanitizeLegacyPayload(item))
	}
	return result
}

func sanitizeLegacyValue(key string, value any) any {
	if sensitiveLegacyKey(key) {
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			result[childKey] = sanitizeLegacyValue(childKey, childValue)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = sanitizeLegacyValue(key, child)
		}
		return result
	case string:
		parsed, err := url.Parse(typed)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || !urlContainsSensitiveData(parsed) {
			return typed
		}
		parsed.User = nil
		query := parsed.Query()
		for queryKey := range query {
			if sensitiveLegacyKey(queryKey) {
				query.Set(queryKey, "[REDACTED]")
			}
		}
		parsed.RawQuery = query.Encode()
		return parsed.String()
	default:
		return typed
	}
}

func sensitiveLegacyKey(key string) bool {
	normalized := strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "password", "passwd", "secret", "token", "api_key", "apikey", "api_token", "access_token", "refresh_token", "cookie", "cookies", "authorization", "proxy_authorization":
		return true
	default:
		return strings.HasSuffix(normalized, "_password") || strings.HasSuffix(normalized, "_secret") || strings.HasSuffix(normalized, "_token")
	}
}

func urlContainsSensitiveData(parsed *url.URL) bool {
	if parsed == nil || parsed.User != nil {
		return parsed != nil && parsed.User != nil
	}
	for key := range parsed.Query() {
		if sensitiveLegacyKey(key) {
			return true
		}
	}
	return false
}

func cloneInventory(value map[string]int) map[string]int {
	result := make(map[string]int, len(value))
	for key, count := range value {
		result[key] = count
	}
	return result
}

func nonzeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Unix(1, 0).UTC()
}
