CREATE TABLE emby_scraping_proxy_state (
    id uuid PRIMARY KEY,
    active boolean NOT NULL DEFAULT false,
    current_operation_id uuid REFERENCES operations (id) ON DELETE SET NULL,
    version integer NOT NULL DEFAULT 1,
    applied_at timestamptz,
    restored_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT emby_scraping_proxy_singleton CHECK (id = 'ee000000-0000-0000-0000-000000000001'::uuid),
    CONSTRAINT emby_scraping_proxy_version_positive CHECK (version > 0),
    CONSTRAINT emby_scraping_proxy_active_applied CHECK (NOT active OR applied_at IS NOT NULL)
);

INSERT INTO emby_scraping_proxy_state (id)
VALUES ('ee000000-0000-0000-0000-000000000001');
