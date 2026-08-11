CREATE UNIQUE INDEX acquisitions_release_candidate_unique
    ON acquisitions (release_candidate_id)
    WHERE release_candidate_id IS NOT NULL;

CREATE INDEX search_runs_created_idx
    ON search_runs (created_at DESC, id DESC);

CREATE INDEX release_candidates_search_created_idx
    ON release_candidates (search_run_id, created_at, id);
