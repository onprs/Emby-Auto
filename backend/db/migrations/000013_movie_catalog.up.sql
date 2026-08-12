ALTER TABLE emby_library_items
    DROP CONSTRAINT emby_library_items_type_valid,
    ADD CONSTRAINT emby_library_items_type_valid CHECK (item_type IN ('Series', 'Season', 'Episode', 'Movie'));
