package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func TestReconcileAutomaticEpisodeMappingPagesFreezesWindowAndSkipsReplacementCanonical(t *testing.T) {
	t.Parallel()

	groupOne := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	groupTwo := uuid.MustParse("20000000-0000-4000-8000-000000000001")
	groupThree := uuid.MustParse("30000000-0000-4000-8000-000000000001")
	acquisitionOne := uuid.MustParse("11000000-0000-4000-8000-000000000001")
	acquisitionTwo := uuid.MustParse("21000000-0000-4000-8000-000000000001")
	replacementAcquisition := uuid.MustParse("22000000-0000-4000-8000-000000000001")
	acquisitionThree := uuid.MustParse("31000000-0000-4000-8000-000000000001")
	createdOne := time.Date(2026, time.January, 1, 0, 0, 1, 0, time.UTC)
	createdTwo := createdOne.Add(time.Second)
	createdReplacement := createdTwo.Add(time.Second)
	createdThree := createdReplacement.Add(time.Second)
	windowCreatedBefore := createdThree.Add(time.Hour)
	row := func(acquisitionID, groupKey uuid.UUID, createdAt time.Time) db.ListAutomaticEpisodeMappingAcquisitionsRow {
		return db.ListAutomaticEpisodeMappingAcquisitionsRow{
			AcquisitionID:           repository.UUIDToPG(acquisitionID),
			GroupKey:                repository.UUIDToPG(groupKey),
			CreatedAt:               pgtype.Timestamptz{Time: createdAt, Valid: true},
			WindowCreatedBefore:     pgtype.Timestamptz{Time: windowCreatedBefore, Valid: true},
			WindowHighGroupKey:      repository.UUIDToPG(groupThree),
			WindowHighCreatedAt:     pgtype.Timestamptz{Time: createdThree, Valid: true},
			WindowHighAcquisitionID: repository.UUIDToPG(acquisitionThree),
		}
	}
	assertWindow := func(params db.ListAutomaticEpisodeMappingAcquisitionsParams) {
		t.Helper()
		if !params.WindowCreatedBefore.Valid || !params.WindowCreatedBefore.Time.Equal(windowCreatedBefore) ||
			repository.UUIDFromPG(params.WindowHighGroupKey) != groupThree ||
			!params.WindowHighCreatedAt.Valid || !params.WindowHighCreatedAt.Time.Equal(createdThree) ||
			repository.UUIDFromPG(params.WindowHighAcquisitionID) != acquisitionThree {
			t.Fatalf("reconciliation window = %+v", params)
		}
	}

	loadCalls := 0
	load := func(_ context.Context, params db.ListAutomaticEpisodeMappingAcquisitionsParams) ([]db.ListAutomaticEpisodeMappingAcquisitionsRow, error) {
		defer func() { loadCalls++ }()
		if params.PageSize != 2 {
			t.Fatalf("page size = %d, want 2", params.PageSize)
		}
		switch loadCalls {
		case 0:
			if params.CursorGroupKey.Valid || params.CursorCreatedAt.Valid || params.CursorAcquisitionID.Valid ||
				params.WindowCreatedBefore.Valid || params.WindowHighGroupKey.Valid || params.WindowHighCreatedAt.Valid || params.WindowHighAcquisitionID.Valid {
				t.Fatalf("first page parameters = %+v", params)
			}
			return []db.ListAutomaticEpisodeMappingAcquisitionsRow{
				row(acquisitionOne, groupOne, createdOne),
				row(acquisitionTwo, groupTwo, createdTwo),
			}, nil
		case 1:
			assertWindow(params)
			if repository.UUIDFromPG(params.CursorGroupKey) != groupTwo ||
				repository.UUIDFromPG(params.CursorAcquisitionID) != acquisitionTwo ||
				!params.CursorCreatedAt.Valid || !params.CursorCreatedAt.Time.Equal(createdTwo) {
				t.Fatalf("second page cursor = %+v", params)
			}
			replacement := row(replacementAcquisition, groupTwo, createdReplacement)
			replacement.WindowCreatedBefore = pgtype.Timestamptz{Time: windowCreatedBefore.Add(time.Hour), Valid: true}
			replacement.WindowHighGroupKey = repository.UUIDToPG(groupOne)
			replacement.WindowHighCreatedAt = pgtype.Timestamptz{Time: createdOne, Valid: true}
			replacement.WindowHighAcquisitionID = repository.UUIDToPG(acquisitionOne)
			return []db.ListAutomaticEpisodeMappingAcquisitionsRow{
				replacement,
				row(acquisitionThree, groupThree, createdThree),
			}, nil
		case 2:
			assertWindow(params)
			if repository.UUIDFromPG(params.CursorGroupKey) != groupThree ||
				repository.UUIDFromPG(params.CursorAcquisitionID) != acquisitionThree ||
				!params.CursorCreatedAt.Valid || !params.CursorCreatedAt.Time.Equal(createdThree) {
				t.Fatalf("third page cursor = %+v", params)
			}
			return nil, nil
		default:
			t.Fatalf("unexpected page load %d", loadCalls+1)
			return nil, nil
		}
	}

	visited := make([]uuid.UUID, 0, 3)
	attempted, err := reconcileAutomaticEpisodeMappingPages(context.Background(), 2, load, func(_ context.Context, acquisitionID uuid.UUID) error {
		visited = append(visited, acquisitionID)
		return nil
	})
	if err != nil {
		t.Fatalf("reconcileAutomaticEpisodeMappingPages() error = %v", err)
	}
	if attempted != 3 || loadCalls != 3 {
		t.Fatalf("attempted/load calls = %d/%d, want 3/3", attempted, loadCalls)
	}
	want := []uuid.UUID{acquisitionOne, acquisitionTwo, acquisitionThree}
	if len(visited) != len(want) {
		t.Fatalf("visited = %v, want %v", visited, want)
	}
	for index := range want {
		if visited[index] != want[index] {
			t.Fatalf("visited = %v, want %v", visited, want)
		}
	}
}
