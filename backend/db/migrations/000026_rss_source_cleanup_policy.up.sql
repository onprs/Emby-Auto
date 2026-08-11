ALTER TABLE rss_subscriptions
    RENAME COLUMN delete_imported_on_completion TO cleanup_source_on_completion;

-- Completion jobs queued by older releases prepared acquisitions for deletion
-- before the Worker ran. New completion jobs retain history, so unhide only
-- acquisitions owned by an active automatic completion operation.
UPDATE acquisitions AS acquisition
SET deletion_requested_at = NULL,
    updated_at = now()
FROM rss_entries AS entry
JOIN rss_subscriptions AS subscription ON subscription.id = entry.subscription_id
WHERE acquisition.rss_entry_id = entry.id
  AND acquisition.deletion_requested_at IS NOT NULL
  AND subscription.completed_at IS NOT NULL
  AND EXISTS (
      SELECT 1
      FROM operations AS operation
      WHERE operation.resource_type = 'rss_subscription'
        AND operation.resource_id = subscription.id
        AND operation.status IN ('queued', 'running')
        AND operation.cancel_requested_at IS NULL
        AND (
            operation.kind = 'rss.subscription.complete'
            OR (
                operation.kind = 'rss.subscription.delete'
                AND operation.payload->>'trigger' = 'final_import'
            )
        )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM operations AS manual_subscription_deletion
      WHERE manual_subscription_deletion.resource_type = 'rss_subscription'
        AND manual_subscription_deletion.resource_id = subscription.id
        AND manual_subscription_deletion.kind = 'rss.subscription.delete'
        AND manual_subscription_deletion.status IN ('queued', 'running')
        AND manual_subscription_deletion.cancel_requested_at IS NULL
        AND COALESCE(manual_subscription_deletion.payload->>'trigger', 'manual') <> 'final_import'
  )
  AND NOT EXISTS (
      SELECT 1
      FROM operations AS manual_acquisition_deletion
      WHERE manual_acquisition_deletion.resource_type = 'acquisition'
        AND manual_acquisition_deletion.resource_id = acquisition.id
        AND manual_acquisition_deletion.kind = 'acquisition.delete'
        AND manual_acquisition_deletion.status IN ('queued', 'running')
        AND manual_acquisition_deletion.cancel_requested_at IS NULL
  );
