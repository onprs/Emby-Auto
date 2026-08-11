package legacymigration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxLegacyJSONItemBytes = 16 << 20

type Source interface {
	Load(context.Context) (Snapshot, error)
}

type RuntimeJSONSource struct {
	Directory string
}

func (source RuntimeJSONSource) Load(_ context.Context) (Snapshot, error) {
	directory, err := filepath.Abs(filepath.Clean(strings.TrimSpace(source.Directory)))
	if err != nil || strings.TrimSpace(source.Directory) == "" {
		return Snapshot{}, fmt.Errorf("legacy runtime directory is required")
	}
	snapshot := Snapshot{SourceKind: "runtime_json", Inventory: map[string]int{}}
	tasks, err := readJSONObjectArray(filepath.Join(directory, "tasks.json"), "task_id", "task")
	if err != nil {
		return Snapshot{}, fmt.Errorf("read legacy tasks: %w", err)
	}
	snapshot.Inventory["tasks"] = len(tasks)
	byID := make(map[string]struct{}, len(tasks))
	for _, record := range tasks {
		byID[record.LegacyID] = struct{}{}
		snapshot.Records = append(snapshot.Records, record)
	}
	queue, err := readJSONObjectArray(filepath.Join(directory, "watch_queue.json"), "task_id", "watch_queue")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, fmt.Errorf("read legacy watch queue: %w", err)
	}
	snapshot.Inventory["watchQueue"] = len(queue)
	for _, record := range queue {
		if _, exists := byID[record.LegacyID]; exists {
			continue
		}
		byID[record.LegacyID] = struct{}{}
		snapshot.Records = append(snapshot.Records, record)
	}
	rss, err := readJSONObjectArray(filepath.Join(directory, "rss_drafts.json"), "draft_id", "rss_draft")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, fmt.Errorf("read legacy RSS drafts: %w", err)
	}
	snapshot.RSSDrafts = rss
	snapshot.Inventory["rssDrafts"] = len(rss)
	intake, err := readJSONObjectArray(filepath.Join(directory, "intake_drafts.json"), "draft_id", "intake_draft")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, fmt.Errorf("read legacy intake drafts: %w", err)
	}
	snapshot.Inventory["intakeDrafts"] = len(intake)
	snapshot.Records = mergeIntakeDrafts(snapshot.Records, intake)
	deletedCount, err := countJSONObjectArray(filepath.Join(directory, "deleted_tasks.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, fmt.Errorf("inventory deleted_tasks.json: %w", err)
	}
	snapshot.Inventory["deletedTasks"] = deletedCount
	sort.Slice(snapshot.Records, func(i, j int) bool { return snapshot.Records[i].LegacyID < snapshot.Records[j].LegacyID })
	sort.Slice(snapshot.RSSDrafts, func(i, j int) bool { return snapshot.RSSDrafts[i].LegacyID < snapshot.RSSDrafts[j].LegacyID })
	return snapshot, nil
}

type PostgresSource struct {
	DatabaseURL string
}

func (source PostgresSource) Load(ctx context.Context) (Snapshot, error) {
	connection, err := pgx.Connect(ctx, strings.TrimSpace(source.DatabaseURL))
	if err != nil {
		return Snapshot{}, fmt.Errorf("connect legacy PostgreSQL: %w", err)
	}
	defer func() { _ = connection.Close(context.Background()) }()
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return Snapshot{}, fmt.Errorf("begin legacy read-only transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return Snapshot{}, fmt.Errorf("enforce legacy read-only transaction: %w", err)
	}
	var databaseName, serverAddress string
	var serverPort int
	if err := tx.QueryRow(ctx, `
SELECT current_database(), COALESCE(inet_server_addr()::text, 'local'), COALESCE(inet_server_port(), 0)
`).Scan(&databaseName, &serverAddress, &serverPort); err != nil {
		return Snapshot{}, fmt.Errorf("identify legacy PostgreSQL: %w", err)
	}
	databaseIdentity := fmt.Sprintf("%s:%d/%s", serverAddress, serverPort, databaseName)

	tasks, err := readPostgresJSONRows(ctx, tx, "SELECT to_jsonb(task) FROM tasks AS task ORDER BY task_id", "task_id", "task")
	if err != nil {
		return Snapshot{}, fmt.Errorf("read legacy PostgreSQL tasks: %w", err)
	}
	watchQueue, err := readPostgresJSONRows(ctx, tx, `
SELECT payload || jsonb_build_object(
    'task_id', COALESCE(NULLIF(payload->>'task_id', ''), item_id),
    'created_at', created_at,
    'updated_at', updated_at
)
FROM watch_queue_items
ORDER BY item_id
`, "task_id", "watch_queue")
	if err != nil && !pgErrorTableMissing(err) {
		return Snapshot{}, fmt.Errorf("read legacy PostgreSQL watch queue: %w", err)
	}
	taskCount := len(tasks)
	byID := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		byID[task.LegacyID] = struct{}{}
	}
	for _, record := range watchQueue {
		if _, exists := byID[record.LegacyID]; exists {
			continue
		}
		byID[record.LegacyID] = struct{}{}
		tasks = append(tasks, record)
	}
	historyByTask, err := readPostgresHistory(ctx, tx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read legacy PostgreSQL task events: %w", err)
	}
	for index := range tasks {
		tasks[index].History = historyByTask[tasks[index].LegacyID]
	}
	rss, err := readPostgresJSONRows(ctx, tx, "SELECT payload || jsonb_build_object('draft_id', draft_id, 'created_at', created_at, 'updated_at', updated_at) FROM rss_drafts ORDER BY draft_id", "draft_id", "rss_draft")
	if err != nil {
		return Snapshot{}, fmt.Errorf("read legacy PostgreSQL RSS drafts: %w", err)
	}
	intake, err := readPostgresJSONRows(ctx, tx, "SELECT payload || jsonb_build_object('draft_id', draft_id) FROM intake_drafts ORDER BY draft_id", "draft_id", "intake_draft")
	if err != nil && !pgErrorTableMissing(err) {
		return Snapshot{}, fmt.Errorf("read legacy PostgreSQL intake drafts: %w", err)
	}
	tasks = mergeIntakeDrafts(tasks, intake)
	inventory := map[string]int{"tasks": taskCount, "watchQueue": len(watchQueue), "rssDrafts": len(rss), "intakeDrafts": len(intake)}
	for table, key := range map[string]string{"deleted_task_archive": "deletedTasks"} {
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			if pgErrorTableMissing(err) {
				count = 0
			} else {
				return Snapshot{}, fmt.Errorf("inventory legacy table %s: %w", table, err)
			}
		}
		inventory[key] = count
	}
	return Snapshot{SourceKind: "legacy_postgres", DatabaseIdentity: databaseIdentity, Records: tasks, RSSDrafts: rss, Inventory: inventory}, nil
}

func readJSONObjectArray(path, idKey, kind string) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(bufio.NewReaderSize(file, 128<<10))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return nil, fmt.Errorf("legacy file must contain a JSON array")
	}
	result := make([]Record, 0)
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		if len(raw) > maxLegacyJSONItemBytes {
			return nil, fmt.Errorf("legacy item exceeds %d bytes", maxLegacyJSONItemBytes)
		}
		record, err := recordFromRaw(raw, idKey, kind)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return result, nil
}

func mergeIntakeDrafts(records, drafts []Record) []Record {
	result := append([]Record(nil), records...)
	indexes := make(map[string]int, len(result))
	for index, record := range result {
		if record.LegacyID != "" {
			indexes[record.LegacyID] = index
		}
	}
	for _, draft := range drafts {
		runtimeTask := objectValue(draft.Payload["runtime_task"])
		queueItem := objectValue(draft.Payload["queue_item"])
		taskID := firstText(textFrom(runtimeTask, "task_id"), textFrom(queueItem, "task_id"), textFrom(draft.Payload, "task_id"))
		if taskID == "" {
			continue
		}
		if index, exists := indexes[taskID]; exists {
			result[index] = enrichTaskFromIntake(result[index], draft)
			continue
		}
		payload := cloneObject(queueItem)
		if len(payload) == 0 {
			payload = cloneObject(runtimeTask)
		}
		payload["task_id"] = taskID
		mergeMissingObject(payload, draft.Payload)
		payload["legacy_intake_draft"] = cloneObject(draft.Payload)
		raw, _ := json.Marshal(payload)
		createdAt := nonzeroTime(draft.CreatedAt, draft.UpdatedAt)
		result = append(result, Record{
			Kind: "intake_task", LegacyID: taskID, Payload: payload, Raw: raw,
			History: objectSlice(payload["history"]), CreatedAt: createdAt, UpdatedAt: nonzeroTime(draft.UpdatedAt, createdAt),
		})
		indexes[taskID] = len(result) - 1
	}
	return result
}

func enrichTaskFromIntake(task, draft Record) Record {
	payload := cloneObject(task.Payload)
	for _, key := range []string{
		"tmdb", "canonical_series", "canonical_episode_title", "tmdb_season", "tmdb_episode", "tmdb_total_episodes",
		"source_season", "source_episode", "series", "season", "episode", "episode_title",
	} {
		mergeMissingValue(payload, key, draft.Payload[key])
	}
	payload["legacy_intake_draft"] = cloneObject(draft.Payload)
	task.Payload = payload
	task.Raw, _ = json.Marshal(payload)
	return task
}

func mergeMissingObject(destination, source map[string]any) {
	for key, value := range source {
		mergeMissingValue(destination, key, value)
	}
}

func mergeMissingValue(destination map[string]any, key string, incoming any) {
	if incoming == nil {
		return
	}
	existing, exists := destination[key]
	if !exists || legacyValueEmpty(existing) {
		destination[key] = cloneLegacyValue(incoming)
		return
	}
	existingObject, existingOK := existing.(map[string]any)
	incomingObject, incomingOK := incoming.(map[string]any)
	if existingOK && incomingOK {
		mergeMissingObject(existingObject, incomingObject)
	}
}

func legacyValueEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

func cloneLegacyValue(value any) any {
	encoded, _ := json.Marshal(value)
	var clone any
	_ = json.Unmarshal(encoded, &clone)
	return clone
}

func countJSONObjectArray(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(bufio.NewReaderSize(file, 128<<10))
	token, err := decoder.Token()
	if err != nil {
		return 0, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return 0, fmt.Errorf("legacy file must contain a JSON array")
	}
	count := 0
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return 0, err
		}
		if len(raw) > maxLegacyJSONItemBytes {
			return 0, fmt.Errorf("legacy item exceeds %d bytes", maxLegacyJSONItemBytes)
		}
		count++
	}
	if _, err := decoder.Token(); err != nil {
		return 0, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return 0, err
	}
	return count, nil
}

func readPostgresJSONRows(ctx context.Context, tx pgx.Tx, query, idKey, kind string) ([]Record, error) {
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Record, 0)
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		record, err := recordFromRaw(raw, idKey, kind)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func readPostgresHistory(ctx context.Context, tx pgx.Tx) (map[string][]map[string]any, error) {
	rows, err := tx.Query(ctx, "SELECT task_id, to_jsonb(event) FROM task_events AS event ORDER BY task_id, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]map[string]any)
	for rows.Next() {
		var taskID string
		var raw json.RawMessage
		if err := rows.Scan(&taskID, &raw); err != nil {
			return nil, err
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		result[taskID] = append(result[taskID], value)
	}
	return result, rows.Err()
}

func recordFromRaw(raw json.RawMessage, idKey, kind string) (Record, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Record{}, fmt.Errorf("decode legacy %s: %w", kind, err)
	}
	history := objectSlice(payload["history"])
	canonical, err := json.Marshal(payload)
	if err != nil {
		return Record{}, err
	}
	return Record{
		Kind: kind, LegacyID: textValue(payload[idKey]), Payload: payload, Raw: canonical, History: history,
		CreatedAt: parseLegacyTime(textFrom(payload, "created_at", "createdAt")), UpdatedAt: parseLegacyTime(textFrom(payload, "updated_at", "updatedAt")),
	}, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("unexpected data after legacy JSON array")
}

func objectSlice(value any) []map[string]any {
	rows, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if object, ok := row.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func parseLegacyTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func pgErrorTableMissing(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}
