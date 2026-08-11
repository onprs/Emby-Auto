DO $migration$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM emby_scraping_proxy_state AS state
        LEFT JOIN operations AS operation ON operation.id = state.current_operation_id
        WHERE state.active
           OR operation.status IN ('queued', 'running')
    ) THEN
        RAISE EXCEPTION 'restore the retired Emby scraping network configuration and finish its active command before upgrading';
    END IF;
END
$migration$;

DROP TABLE IF EXISTS emby_scraping_proxy_state;
