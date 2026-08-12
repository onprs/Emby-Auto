package service

import (
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestRSSNilOperationIDIsStoredAsNull(t *testing.T) {
	if operationID := nullableUUID(uuid.Nil); operationID.Valid {
		t.Fatalf("nullableUUID(nil).Valid = true, want false for events without an operation")
	}
}

func TestRSSPreacquisitionMappingScopeIdentityIncludesSubscriptionVersion(t *testing.T) {
	subscriptionID := uuid.New()
	fingerprint := sha256.Sum256([]byte("same-source-scope"))
	first := rssPreacquisitionMappingScopeID(subscriptionID, 1, fingerprint)
	replay := rssPreacquisitionMappingScopeID(subscriptionID, 1, fingerprint)
	updated := rssPreacquisitionMappingScopeID(subscriptionID, 2, fingerprint)
	if first != replay || first == updated {
		t.Fatalf("scope identities first/replay/updated = %s/%s/%s", first, replay, updated)
	}
}

func TestDeterministicRSSPollMappingAnchorUsesStandardExactCoordinates(t *testing.T) {
	targets := map[domain.EpisodeCoordinate]uuid.UUID{
		{Season: 1, Episode: 1}: uuid.New(),
		{Season: 1, Episode: 2}: uuid.New(),
	}
	feed := domain.RSSFeed{Entries: []domain.RSSFeedEntry{
		{Title: "Standard S01E02", DownloadURI: "https://example.test/e02.torrent"},
		{Title: "Standard S01E01", DownloadURI: "https://example.test/e01.torrent"},
		{Title: "Standard S01E01 alternate", DownloadURI: "https://example.test/e01-alt.torrent"},
		{Title: "Standard S01E01 collection", DownloadURI: "https://example.test/collection.torrent"},
	}}

	source, target, ok := deterministicRSSPollMappingAnchor(feed, 1, nil, []string{"collection"}, targets)
	want := domain.EpisodeCoordinate{Season: 1, Episode: 1}
	if !ok || source != want || target != want {
		t.Fatalf("deterministicRSSPollMappingAnchor() = %#v/%#v/%t, want %#v/%#v/true", source, target, ok, want, want)
	}
}

func TestDeterministicRSSPollMappingAnchorRejectsUnverifiedOffsets(t *testing.T) {
	targets := map[domain.EpisodeCoordinate]uuid.UUID{
		{Season: 1, Episode: 1}: uuid.New(),
		{Season: 2, Episode: 1}: uuid.New(),
	}
	tests := []struct {
		name string
		feed domain.RSSFeed
	}{
		{
			name: "target coordinate missing",
			feed: domain.RSSFeed{Entries: []domain.RSSFeedEntry{{
				Title: "Missing S01E02", DownloadURI: "https://example.test/e02.torrent",
			}}},
		},
		{
			name: "source season differs",
			feed: domain.RSSFeed{Entries: []domain.RSSFeedEntry{{
				Title: "Other S02E01", DownloadURI: "https://example.test/s02e01.torrent",
			}}},
		},
		{
			name: "no standard episode",
			feed: domain.RSSFeed{Entries: []domain.RSSFeedEntry{{
				Title: "Promotional video", DownloadURI: "https://example.test/pv.torrent",
			}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if source, target, ok := deterministicRSSPollMappingAnchor(test.feed, 1, nil, nil, targets); ok {
				t.Fatalf("deterministicRSSPollMappingAnchor() = %#v/%#v/true, want unresolved", source, target)
			}
		})
	}
}
