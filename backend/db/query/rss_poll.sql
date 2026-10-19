-- name: ListUnacquiredRSSFeedEntriesByIdentity :many
SELECT entry.*
FROM rss_entries AS entry
WHERE entry.subscription_id = sqlc.arg(subscription_id)
  AND NOT EXISTS (SELECT 1 FROM acquisitions AS acquisition WHERE acquisition.rss_entry_id = entry.id)
  AND (entry.identity_key = ANY(sqlc.arg(identity_keys)::text[])
       OR lower(entry.btih) = ANY(sqlc.arg(btihs)::text[]))
ORDER BY entry.discovered_at, entry.id;
