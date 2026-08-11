package maintenance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxRSSHistoryDumpLineBytes = 16 << 20

type RSSSubscriptionHistory struct {
	ID                        uuid.UUID
	SeriesID                  uuid.UUID
	Name                      string
	FeedURL                   string
	AutoReview                bool
	CleanupSourceOnCompletion bool
	PollIntervalSeconds       int32
	LastPolledAt              *time.Time
	Version                   int32
	SourceSeason              int32
	CreatedAt                 time.Time
}

type RSSEntryHistory struct {
	ID              uuid.UUID
	IdentityKey     string
	GUID            *string
	BTIH            *string
	CanonicalURL    *string
	Title           string
	PublishedAt     *time.Time
	EnqueueAttempts int32
	UpstreamPayload json.RawMessage
	DiscoveredAt    time.Time
	DownloadURI     string
	SourceSeason    int32
	SourceEpisode   int32
	DuplicateCount  int32
}

type RSSHistorySnapshot struct {
	Subscription RSSSubscriptionHistory
	Entries      []RSSEntryHistory
}

func ParseRSSHistoryDump(reader io.Reader, subscriptionID uuid.UUID) (RSSHistorySnapshot, error) {
	if reader == nil {
		return RSSHistorySnapshot{}, fmt.Errorf("RSS history dump reader is required")
	}
	if subscriptionID == uuid.Nil {
		return RSSHistorySnapshot{}, fmt.Errorf("RSS subscription ID is required")
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxRSSHistoryDumpLineBytes)
	var snapshot RSSHistorySnapshot
	var subscriptionFound bool
	var sawSubscriptionCopy, sawEntryCopy bool
	var table string
	var columns []string
	for scanner.Scan() {
		line := scanner.Text()
		if table != "" {
			if line == `\.` {
				table = ""
				columns = nil
				continue
			}
			fields, err := parseCopyTextRow(line, columns)
			if err != nil {
				return RSSHistorySnapshot{}, fmt.Errorf("parse %s COPY row: %w", table, err)
			}
			switch table {
			case "rss_subscriptions":
				rowID, err := requiredUUID(fields, "id")
				if err != nil {
					return RSSHistorySnapshot{}, err
				}
				if rowID != subscriptionID {
					continue
				}
				if subscriptionFound {
					return RSSHistorySnapshot{}, fmt.Errorf("RSS subscription %s appears more than once in the dump", subscriptionID)
				}
				subscription, err := parseSubscriptionHistory(fields)
				if err != nil {
					return RSSHistorySnapshot{}, err
				}
				snapshot.Subscription = subscription
				subscriptionFound = true
			case "rss_entries":
				rowSubscriptionID, err := requiredUUID(fields, "subscription_id")
				if err != nil {
					return RSSHistorySnapshot{}, err
				}
				if rowSubscriptionID != subscriptionID {
					continue
				}
				entry, err := parseEntryHistory(fields)
				if err != nil {
					return RSSHistorySnapshot{}, err
				}
				snapshot.Entries = append(snapshot.Entries, entry)
			}
			continue
		}

		copyTable, copyColumns, ok, err := parseCopyHeader(line)
		if err != nil {
			return RSSHistorySnapshot{}, err
		}
		if !ok {
			continue
		}
		table, columns = copyTable, copyColumns
		switch table {
		case "rss_subscriptions":
			sawSubscriptionCopy = true
		case "rss_entries":
			sawEntryCopy = true
		}
	}
	if err := scanner.Err(); err != nil {
		return RSSHistorySnapshot{}, fmt.Errorf("read RSS history dump: %w", err)
	}
	if table != "" {
		return RSSHistorySnapshot{}, fmt.Errorf("unterminated COPY data for %s", table)
	}
	if !sawSubscriptionCopy || !sawEntryCopy {
		return RSSHistorySnapshot{}, fmt.Errorf("dump must contain rss_subscriptions and rss_entries COPY data")
	}
	if !subscriptionFound {
		return RSSHistorySnapshot{}, fmt.Errorf("RSS subscription %s was not found in the dump", subscriptionID)
	}
	if len(snapshot.Entries) == 0 {
		return RSSHistorySnapshot{}, fmt.Errorf("RSS subscription %s has no entries in the dump", subscriptionID)
	}
	return snapshot, nil
}

func parseCopyHeader(line string) (string, []string, bool, error) {
	const prefix = "COPY public."
	const suffix = ") FROM stdin;"
	if !strings.HasPrefix(line, prefix) {
		return "", nil, false, nil
	}
	separator := strings.Index(line, " (")
	if separator < len(prefix) || !strings.HasSuffix(line, suffix) {
		return "", nil, false, fmt.Errorf("unsupported PostgreSQL COPY header")
	}
	table := line[len(prefix):separator]
	columnText := line[separator+2 : len(line)-len(suffix)]
	if table != "rss_subscriptions" && table != "rss_entries" {
		return "ignored", strings.Split(columnText, ", "), true, nil
	}
	columns := strings.Split(columnText, ", ")
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		column = strings.TrimSpace(column)
		if column == "" {
			return "", nil, false, fmt.Errorf("COPY header contains a blank column")
		}
		if _, exists := seen[column]; exists {
			return "", nil, false, fmt.Errorf("COPY header contains duplicate column %q", column)
		}
		seen[column] = struct{}{}
	}
	return table, columns, true, nil
}

func parseCopyTextRow(line string, columns []string) (map[string]*string, error) {
	rawFields := strings.Split(line, "\t")
	if len(rawFields) != len(columns) {
		return nil, fmt.Errorf("field count %d does not match column count %d", len(rawFields), len(columns))
	}
	fields := make(map[string]*string, len(columns))
	for index, raw := range rawFields {
		if raw == `\N` {
			fields[columns[index]] = nil
			continue
		}
		decoded, err := decodeCopyText(raw)
		if err != nil {
			return nil, fmt.Errorf("decode column %s: %w", columns[index], err)
		}
		fields[columns[index]] = &decoded
	}
	return fields, nil
}

func decodeCopyText(value string) (string, error) {
	var decoded strings.Builder
	decoded.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			decoded.WriteByte(value[index])
			continue
		}
		index++
		if index >= len(value) {
			return "", fmt.Errorf("trailing escape")
		}
		switch value[index] {
		case 'b':
			decoded.WriteByte('\b')
		case 'f':
			decoded.WriteByte('\f')
		case 'n':
			decoded.WriteByte('\n')
		case 'r':
			decoded.WriteByte('\r')
		case 't':
			decoded.WriteByte('\t')
		case 'v':
			decoded.WriteByte('\v')
		case 'x':
			start := index + 1
			end := start
			for end < len(value) && end < start+2 && isHexDigit(value[end]) {
				end++
			}
			if end == start {
				return "", fmt.Errorf("hex escape has no digits")
			}
			parsed, err := strconv.ParseUint(value[start:end], 16, 8)
			if err != nil {
				return "", fmt.Errorf("parse hex escape: %w", err)
			}
			decoded.WriteByte(byte(parsed))
			index = end - 1
		default:
			if value[index] >= '0' && value[index] <= '7' {
				start := index
				end := start
				for end < len(value) && end < start+3 && value[end] >= '0' && value[end] <= '7' {
					end++
				}
				parsed, err := strconv.ParseUint(value[start:end], 8, 8)
				if err != nil {
					return "", fmt.Errorf("parse octal escape: %w", err)
				}
				decoded.WriteByte(byte(parsed))
				index = end - 1
			} else {
				decoded.WriteByte(value[index])
			}
		}
	}
	return decoded.String(), nil
}

func isHexDigit(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func parseSubscriptionHistory(fields map[string]*string) (RSSSubscriptionHistory, error) {
	id, err := requiredUUID(fields, "id")
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	seriesID, err := requiredUUID(fields, "series_id")
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	name, err := requiredString(fields, "name")
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	feedURL, err := requiredString(fields, "feed_url")
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	pollInterval, err := requiredInt32(fields, "poll_interval_seconds")
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	lastPolledAt, err := optionalTime(fields, "last_polled_at")
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	version, err := requiredInt32(fields, "version")
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	sourceSeason, err := requiredInt32(fields, "source_season")
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	createdAt, err := requiredTime(fields, "created_at")
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	autoReview, err := requiredBool(fields, "auto_review")
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	cleanupSource, err := requiredBool(fields, "cleanup_source_on_completion")
	if err != nil {
		// Backups created before the source-cleanup policy rename remain restorable.
		cleanupSource, err = requiredBool(fields, "delete_imported_on_completion")
	}
	if err != nil {
		return RSSSubscriptionHistory{}, err
	}
	return RSSSubscriptionHistory{
		ID: id, SeriesID: seriesID, Name: name, FeedURL: feedURL, AutoReview: autoReview,
		CleanupSourceOnCompletion: cleanupSource, PollIntervalSeconds: pollInterval,
		LastPolledAt: lastPolledAt, Version: version, SourceSeason: sourceSeason, CreatedAt: createdAt,
	}, nil
}

func parseEntryHistory(fields map[string]*string) (RSSEntryHistory, error) {
	id, err := requiredUUID(fields, "id")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	identityKey, err := requiredString(fields, "identity_key")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	title, err := requiredString(fields, "title")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	publishedAt, err := optionalTime(fields, "published_at")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	enqueueAttempts, err := requiredInt32(fields, "enqueue_attempts")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	payload, err := requiredJSONObject(fields, "upstream_payload")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	discoveredAt, err := requiredTime(fields, "discovered_at")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	downloadURI, err := requiredString(fields, "download_uri")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	downloadable, err := requiredBool(fields, "downloadable")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	if !downloadable {
		return RSSEntryHistory{}, fmt.Errorf("RSS history entry %s is not downloadable", id)
	}
	sourceSeason, err := requiredInt32(fields, "source_season")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	sourceEpisode, err := requiredInt32(fields, "source_episode")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	duplicateCount, err := requiredInt32(fields, "duplicate_count")
	if err != nil {
		return RSSEntryHistory{}, err
	}
	return RSSEntryHistory{
		ID: id, IdentityKey: identityKey, GUID: optionalString(fields, "guid"), BTIH: optionalString(fields, "btih"),
		CanonicalURL: optionalString(fields, "canonical_url"), Title: title, PublishedAt: publishedAt,
		EnqueueAttempts: enqueueAttempts, UpstreamPayload: payload, DiscoveredAt: discoveredAt,
		DownloadURI: downloadURI, SourceSeason: sourceSeason, SourceEpisode: sourceEpisode, DuplicateCount: duplicateCount,
	}, nil
}

func requiredString(fields map[string]*string, name string) (string, error) {
	value, exists := fields[name]
	if !exists {
		return "", fmt.Errorf("COPY data is missing required column %s", name)
	}
	if value == nil || strings.TrimSpace(*value) == "" {
		return "", fmt.Errorf("COPY column %s must not be null or blank", name)
	}
	return *value, nil
}

func optionalString(fields map[string]*string, name string) *string {
	value := fields[name]
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func requiredUUID(fields map[string]*string, name string) (uuid.UUID, error) {
	value, err := requiredString(fields, name)
	if err != nil {
		return uuid.Nil, err
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("COPY column %s is not a UUID: %w", name, err)
	}
	return parsed, nil
}

func requiredInt32(fields map[string]*string, name string) (int32, error) {
	value, err := requiredString(fields, name)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("COPY column %s is not an int32: %w", name, err)
	}
	return int32(parsed), nil
}

func requiredBool(fields map[string]*string, name string) (bool, error) {
	value, err := requiredString(fields, name)
	if err != nil {
		return false, err
	}
	switch value {
	case "t", "true":
		return true, nil
	case "f", "false":
		return false, nil
	default:
		return false, fmt.Errorf("COPY column %s is not a boolean", name)
	}
}

func optionalTime(fields map[string]*string, name string) (*time.Time, error) {
	value, exists := fields[name]
	if !exists {
		return nil, fmt.Errorf("COPY data is missing required column %s", name)
	}
	if value == nil {
		return nil, nil
	}
	parsed, err := parsePostgresTime(*value)
	if err != nil {
		return nil, fmt.Errorf("COPY column %s is not a timestamp: %w", name, err)
	}
	return &parsed, nil
}

func requiredTime(fields map[string]*string, name string) (time.Time, error) {
	parsed, err := optionalTime(fields, name)
	if err != nil {
		return time.Time{}, err
	}
	if parsed == nil {
		return time.Time{}, fmt.Errorf("COPY column %s must not be null", name)
	}
	return *parsed, nil
}

func parsePostgresTime(value string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z07",
		time.RFC3339Nano,
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp format")
}

func requiredJSONObject(fields map[string]*string, name string) (json.RawMessage, error) {
	value, err := requiredString(fields, name)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &object); err != nil || object == nil {
		return nil, fmt.Errorf("COPY column %s must be a JSON object", name)
	}
	return json.RawMessage(append([]byte(nil), value...)), nil
}
