package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// 缓存仅包含尚无 acquisition 的条目，且仅在持有订阅锁的一次 PersistPoll 事务内使用。
// 已有获取任务的条目保持即时读取，避免使用后台入库更新前的状态。
type rssPollEntryCache struct {
	entries     map[string]db.RssEntry
	keysByID    map[uuid.UUID][]string
	unacquired  map[uuid.UUID]struct{}
	occupancies map[domain.EpisodeCoordinate]rssTargetOccupancy
}

func loadRSSPollEntryCache(ctx context.Context, queries *db.Queries, subscriptionID uuid.UUID, feed domain.RSSFeed) (*rssPollEntryCache, error) {
	identities := make([]string, 0, len(feed.Entries))
	hashes := make([]string, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		identity, err := domain.BuildRSSFeedEntryIdentity(entry)
		if err != nil {
			continue
		}
		identities = append(identities, identity)
		if strings.HasPrefix(identity, "btih:") {
			hashes = append(hashes, strings.TrimPrefix(identity, "btih:"))
		}
	}
	rows, err := queries.ListUnacquiredRSSFeedEntriesByIdentity(ctx, db.ListUnacquiredRSSFeedEntriesByIdentityParams{
		SubscriptionID: repository.UUIDToPG(subscriptionID), IdentityKeys: identities, Btihs: hashes,
	})
	if err != nil {
		return nil, err
	}
	cache := &rssPollEntryCache{entries: make(map[string]db.RssEntry, len(rows)), keysByID: make(map[uuid.UUID][]string, len(rows)), unacquired: make(map[uuid.UUID]struct{}, len(rows))}
	for _, entry := range rows {
		if _, exists := cache.entries[entry.IdentityKey]; !exists {
			cache.remember(entry.IdentityKey, entry)
		}
		if identity, err := domain.BuildRSSIdentity(domain.RSSIdentityInput{BTIH: valueOrEmpty(entry.Btih)}); err == nil {
			if _, exists := cache.entries[identity]; !exists {
				cache.remember(identity, entry)
			}
		}
		cache.unacquired[repository.UUIDFromPG(entry.ID)] = struct{}{}
	}
	return cache, nil
}

func (cache *rssPollEntryCache) remember(identity string, entry db.RssEntry) {
	id := repository.UUIDFromPG(entry.ID)
	if _, exists := cache.entries[identity]; !exists {
		cache.keysByID[id] = append(cache.keysByID[id], identity)
	}
	for _, key := range cache.keysByID[id] {
		cache.entries[key] = entry
	}
}

func (cache *rssPollEntryCache) invalidate(id uuid.UUID) {
	for _, key := range cache.keysByID[id] {
		delete(cache.entries, key)
	}
	delete(cache.keysByID, id)
}
