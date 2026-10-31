//go:build integration

package service

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func sortDownloadViewsReference(views []domain.DownloadView, field string, direction int) {
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		comparison := 0
		switch field {
		case "attempt":
			comparison = cmp.Compare(left.Attempt, right.Attempt)
		case "status":
			comparison = strings.Compare(left.Status, right.Status)
		case "client_state":
			comparison = strings.Compare(left.ClientState, right.ClientState)
		case "progress":
			comparison = cmp.Compare(left.Progress, right.Progress)
		default:
			comparison = compareTime(left.UpdatedAt, right.UpdatedAt)
		}
		if comparison == 0 {
			comparison = strings.Compare(left.ID.String(), right.ID.String())
		}
		return direction*comparison < 0
	})
}

func sortRSSViewsReference(views []domain.RSSEntryView, field string, direction int) {
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		comparison := 0
		switch field {
		case "title":
			comparison = strings.Compare(strings.ToLower(left.Title), strings.ToLower(right.Title))
		case "episode":
			comparison = compareOptionalEpisode(left.SourceSeason, left.SourceEpisode, right.SourceSeason, right.SourceEpisode)
		case "progress":
			leftProgress, rightProgress := float64(0), float64(0)
			if left.AcquisitionProgress != nil {
				leftProgress = left.AcquisitionProgress.OverallProgress
			}
			if right.AcquisitionProgress != nil {
				rightProgress = right.AcquisitionProgress.OverallProgress
			}
			comparison = cmp.Compare(leftProgress, rightProgress)
		default:
			comparison = compareTime(left.CreatedAt, right.CreatedAt)
		}
		if comparison == 0 {
			comparison = strings.Compare(left.ID.String(), right.ID.String())
		}
		return direction*comparison < 0
	})
}

func paginationViewIDs[T any](rows []T, idOf func(T) uuid.UUID) []uuid.UUID {
	ids := make([]uuid.UUID, len(rows))
	for index, row := range rows {
		ids[index] = idOf(row)
	}
	return ids
}

func assertPaginationReference(t *testing.T, expected []uuid.UUID, read func(*uuid.UUID) ([]uuid.UUID, *uuid.UUID, error)) {
	t.Helper()
	actual := make([]uuid.UUID, 0, len(expected))
	seen := make(map[uuid.UUID]bool)
	var cursor *uuid.UUID
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > len(expected)+1 {
			t.Fatal("游标分页未结束")
		}
		ids, next, err := read(cursor)
		if err != nil {
			t.Fatalf("读取第 %d 页: %v", pageNumber, err)
		}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("跨页重复资源: %s", id)
			}
			seen[id] = true
		}
		actual = append(actual, ids...)
		if next == nil {
			break
		}
		if len(ids) == 0 || *next != ids[len(ids)-1] {
			t.Fatal("下一页游标没有指向当前页末项")
		}
		cursor = next
	}
	if !slices.Equal(actual, expected) {
		t.Fatalf("数据库分页与原排序不一致:\n实际=%v\n预期=%v", actual, expected)
	}
	if len(expected) > 0 {
		last := expected[len(expected)-1]
		ids, next, err := read(&last)
		if err != nil || len(ids) != 0 || next != nil {
			t.Fatalf("有效末项游标应返回空页: %v / %v / %v", ids, next, err)
		}
	}
}

func TestDatabaseListPaginationMatchesPreviousOrderingIntegration(t *testing.T) {
	fixture := newPaginationFixture(t)
	fixture.seed(t, 48)
	mediaIDs := []uuid.UUID{fixture.seriesID}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE media_series SET title='Same Show' WHERE id=$1`, fixture.seriesID); err != nil {
		t.Fatal(err)
	}
	earliestMovieYear, firstMovieYear, secondMovieYear := int32(1995), int32(2001), int32(2005)
	for _, item := range []struct {
		title string
		kind  string
		year  *int32
	}{
		{"Same Show", "movie", &firstMovieYear},
		{"Same Show", "movie", &secondMovieYear},
		{"Same Show", "movie", &earliestMovieYear},
		{"İSTANBUL", "tv", nil},
		{"istanbul", "tv", nil},
		{" Same Show", "tv", nil},
		{"Épisode", "tv", nil},
	} {
		id := uuid.New()
		mediaIDs = append(mediaIDs, id)
		var movieID any
		if item.kind == "movie" {
			movieID = int64(9000 + len(mediaIDs))
		}
		if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO media_series(id,title,media_type,tmdb_movie_id,release_year) VALUES($1,$2,$3,$4,$5)`, id, item.title, item.kind, movieID, item.year); err != nil {
			t.Fatal(err)
		}
	}
	labels := []string{"Item", "İSTANBUL", "istanbul", "Épisode", "100%_Complete", "100XYComplete", "Straße", "STRASSE"}
	acquisitionIDs := make([]pgtype.UUID, 0, fixture.count)
	downloadIDs := make([]pgtype.UUID, 0, fixture.count)
	entryIDs := make([]pgtype.UUID, 0, fixture.count)
	for number := 1; number <= fixture.count; number++ {
		acquisitionID, entryID := paginationID("acquisition", number), paginationID("entry", number)
		acquisitionIDs = append(acquisitionIDs, repository.UUIDToPG(acquisitionID))
		downloadIDs = append(downloadIDs, repository.UUIDToPG(paginationID("download", number)))
		entryIDs = append(entryIDs, repository.UUIDToPG(entryID))
		var season, episode any = int32(number%3 + 1), int32(number%4 + 1)
		if number%5 == 0 {
			season, episode = nil, nil
		}
		if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE acquisitions SET series_id=$2,
 source_kind=CASE WHEN $3::boolean THEN 'manual' ELSE 'rss' END,
 source_uri=CASE WHEN $3::boolean THEN 'fixture://sorting/' || id::text ELSE NULL END,
 rss_entry_id=CASE WHEN $3::boolean THEN NULL ELSE $4::uuid END,
 source_payload=jsonb_build_object('sourceSeason',$5::integer,'sourceEpisode',$6::integer,'sourceEpisodeFractionHundredths',$7::integer)
WHERE id=$1`, acquisitionID, mediaIDs[number%len(mediaIDs)], number%3 == 0, entryID, season, episode, (number%3)*25); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE rss_entries SET title=$2,source_season=$3,source_episode=$4 WHERE id=$1`, entryID, fmt.Sprintf("%s S01E%02d", labels[number%len(labels)], number%24+1), season, episode); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.reads.ReconcileListProjections(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	acquisitionRows, err := fixture.queries.ListAcquisitionsByIDs(fixture.ctx, acquisitionIDs)
	if err != nil {
		t.Fatal(err)
	}
	acquisitions, err := fixture.reads.acquisitionViews(fixture.ctx, acquisitionRows)
	if err != nil {
		t.Fatal(err)
	}
	downloadRows, err := fixture.queries.ListDownloadsByIDs(fixture.ctx, downloadIDs)
	if err != nil {
		t.Fatal(err)
	}
	downloads, err := fixture.reads.downloadViews(fixture.ctx, downloadRows)
	if err != nil {
		t.Fatal(err)
	}
	entryRows, err := fixture.queries.ListRSSEntryReadModelsByIDs(fixture.ctx, entryIDs)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fixture.reads.rssEntryViews(fixture.ctx, entryRows)
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{"asc", "desc"} {
		direction := 1
		if order == "desc" {
			direction = -1
		}
		for _, field := range []string{"updated_at", "content", "source_kind", "progress"} {
			t.Run("获取/"+field+"/"+order, func(t *testing.T) {
				expected := slices.Clone(acquisitions)
				sortAcquisitionViews(expected, field, direction)
				assertPaginationReference(t, paginationViewIDs(expected, func(view domain.AcquisitionView) uuid.UUID { return view.ID }), func(cursor *uuid.UUID) ([]uuid.UUID, *uuid.UUID, error) {
					page, err := fixture.reads.ListAcquisitions(fixture.ctx, cursor, 7, nil, nil, nil, &field, &order)
					return paginationViewIDs(page.Items, func(view domain.AcquisitionView) uuid.UUID { return view.ID }), page.NextCursor, err
				})
			})
		}
		for _, field := range []string{"updated_at", "attempt", "status", "client_state", "progress"} {
			t.Run("下载/"+field+"/"+order, func(t *testing.T) {
				expected := slices.Clone(downloads)
				sortDownloadViewsReference(expected, field, direction)
				assertPaginationReference(t, paginationViewIDs(expected, func(view domain.DownloadView) uuid.UUID { return view.ID }), func(cursor *uuid.UUID) ([]uuid.UUID, *uuid.UUID, error) {
					page, err := fixture.reads.ListDownloads(fixture.ctx, cursor, 7, nil, nil, nil, &field, &order)
					return paginationViewIDs(page.Items, func(view domain.DownloadView) uuid.UUID { return view.ID }), page.NextCursor, err
				})
			})
		}
		for _, field := range []string{"discovered_at", "title", "episode", "progress"} {
			t.Run("RSS/"+field+"/"+order, func(t *testing.T) {
				expected := slices.Clone(entries)
				sortRSSViewsReference(expected, field, direction)
				assertPaginationReference(t, paginationViewIDs(expected, func(view domain.RSSEntryView) uuid.UUID { return view.ID }), func(cursor *uuid.UUID) ([]uuid.UUID, *uuid.UUID, error) {
					page, err := fixture.reads.ListRSSEntries(fixture.ctx, fixture.subscriptionID, cursor, 7, nil, nil, nil, nil, &field, &order)
					return paginationViewIDs(page.Items, func(view domain.RSSEntryView) uuid.UUID { return view.ID }), page.NextCursor, err
				})
			})
		}
	}
	query := " %_ "
	expected := make([]domain.RSSEntryView, 0)
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.Title), strings.ToLower(strings.TrimSpace(query))) {
			expected = append(expected, entry)
		}
	}
	sortRSSViewsReference(expected, "discovered_at", -1)
	assertPaginationReference(t, paginationViewIDs(expected, func(view domain.RSSEntryView) uuid.UUID { return view.ID }), func(cursor *uuid.UUID) ([]uuid.UUID, *uuid.UUID, error) {
		page, err := fixture.reads.ListRSSEntries(fixture.ctx, fixture.subscriptionID, cursor, 2, nil, nil, &query, nil, nil, nil)
		return paginationViewIDs(page.Items, func(view domain.RSSEntryView) uuid.UUID { return view.ID }), page.NextCursor, err
	})
	manualID, rssKind := paginationID("acquisition", 3), "rss"
	if _, err := fixture.reads.ListAcquisitions(fixture.ctx, &manualID, 10, &rssKind, nil, nil, nil, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("其他来源筛选集合的游标未被拒绝: %v", err)
	}
	missing := uuid.New()
	if _, err := fixture.reads.ListDownloads(fixture.ctx, &missing, 10, nil, nil, nil, nil, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("不存在的下载游标未被拒绝: %v", err)
	}
	otherSubscription := uuid.New()
	if _, err := fixture.pool.Exec(fixture.ctx, `INSERT INTO rss_subscriptions(id,series_id,name,feed_url) VALUES($1,$2,'Other feed','https://fixture.test/other.xml')`, otherSubscription, fixture.seriesID); err != nil {
		t.Fatal(err)
	}
	foreignEntry := paginationID("entry", 1)
	if _, err := fixture.reads.ListRSSEntries(fixture.ctx, otherSubscription, &foreignEntry, 10, nil, nil, nil, nil, nil, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("其他订阅的游标未被拒绝: %v", err)
	}
	if page, err := fixture.reads.ListRSSEntries(fixture.ctx, otherSubscription, nil, 10, nil, nil, nil, nil, nil, nil); err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("空订阅应返回有效空数组: %#v / %v", page.Items, err)
	}
}
