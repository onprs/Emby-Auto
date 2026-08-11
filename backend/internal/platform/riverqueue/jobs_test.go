package riverqueue

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewJobArgsUsesStableKindsAndOperationPayload(t *testing.T) {
	operationID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	kinds := []string{
		KindSearchRun,
		KindRSSPoll,
		KindRSSSubscriptionComplete,
		KindDownloadEnqueue,
		KindDownloadSync,
		KindDownloadMaterialize,
		KindSubtitlePrepare,
		KindTranscodeRun,
		KindMediaFinalize,
		KindEmbyImport,
		KindCleanupRun,
		KindEmbyRefresh,
	}

	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			args, err := NewJobArgs(kind, operationID, 90*time.Second)
			if err != nil {
				t.Fatalf("NewJobArgs() error = %v", err)
			}
			if args.Kind() != kind {
				t.Fatalf("Kind() = %q, want %q", args.Kind(), kind)
			}
			encoded, err := json.Marshal(args)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			want := `{"operationId":"10000000-0000-0000-0000-000000000001","timeoutSeconds":90}`
			if string(encoded) != want {
				t.Fatalf("job JSON = %s, want %s", encoded, want)
			}
		})
	}
}

func TestInsertOptionsIsolateTranscodeConcurrency(t *testing.T) {
	transcode, err := InsertOptions(KindTranscodeRun, 4)
	if err != nil {
		t.Fatalf("InsertOptions(transcode) error = %v", err)
	}
	if transcode.Queue != QueueTranscode || transcode.MaxAttempts != 4 {
		t.Fatalf("transcode options = queue %q attempts %d, want transcode/4", transcode.Queue, transcode.MaxAttempts)
	}

	for _, kind := range []string{KindSearchRun, KindRSSPoll, KindRSSSubscriptionComplete, KindDownloadEnqueue, KindSubtitlePrepare, KindMediaFinalize, KindEmbyImport, KindCleanupRun} {
		options, err := InsertOptions(kind, 3)
		if err != nil {
			t.Fatalf("InsertOptions(%s) error = %v", kind, err)
		}
		if options.Queue != QueueGeneral {
			t.Fatalf("queue for %s = %q, want %q", kind, options.Queue, QueueGeneral)
		}
		if !options.UniqueOpts.ByArgs || !options.UniqueOpts.ByQueue {
			t.Fatalf("unique options for %s = %#v, want args+queue uniqueness", kind, options.UniqueOpts)
		}
	}
}

func TestOperationIDIsTheOnlyRiverUniqueArgument(t *testing.T) {
	field, ok := reflect.TypeFor[OperationArgs]().FieldByName("OperationID")
	if !ok || field.Tag.Get("river") != "unique" {
		t.Fatalf("OperationID river tag = %q, want unique", field.Tag.Get("river"))
	}
	timeoutField, ok := reflect.TypeFor[OperationArgs]().FieldByName("TimeoutSecond")
	if !ok || timeoutField.Tag.Get("river") != "" {
		t.Fatalf("TimeoutSecond river tag = %q, want no unique tag", timeoutField.Tag.Get("river"))
	}
}

func TestJobArgumentsRejectUnknownKindAndInvalidTimeout(t *testing.T) {
	operationID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	if _, err := NewJobArgs("unknown.job", operationID, time.Minute); err == nil {
		t.Fatal("NewJobArgs(unknown) error = nil")
	}
	if _, err := NewJobArgs(KindRSSPoll, operationID, 1500*time.Millisecond); err == nil {
		t.Fatal("NewJobArgs(fractional timeout) error = nil")
	}
	if _, err := InsertOptions(KindRSSPoll, 0); err == nil {
		t.Fatal("InsertOptions(zero attempts) error = nil")
	}
}
