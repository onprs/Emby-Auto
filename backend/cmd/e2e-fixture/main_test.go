package main

import "testing"

func newSeriesTestFixture() *fixtureServer {
	return &fixtureServer{isolatedSeries: make(map[int]fixtureSeriesSpec)}
}

func TestIsolatedSeriesSeparatesRunsAndReusesTokens(t *testing.T) {
	fixture := newSeriesTestFixture()

	first := fixture.isolatedSeriesForToken("run-a")
	second := fixture.isolatedSeriesForToken("run-b")
	reused := fixture.isolatedSeriesForToken("run-a")

	if first.ID == second.ID || first.EpisodeID == second.EpisodeID {
		t.Fatalf("isolated runs share identifiers: first=%+v second=%+v", first, second)
	}
	if first.Title != "Fixture Show run-a" || first.EpisodeTitle != "Pilot run-a" {
		t.Fatalf("unexpected isolated series title: %+v", first)
	}
	if reused != first {
		t.Fatalf("repeated token should return the same series: first=%+v reused=%+v", first, reused)
	}
}

func TestFixtureSeriesForSearchRoutesIsolatedQueries(t *testing.T) {
	fixture := newSeriesTestFixture()

	shared := fixture.fixtureSeriesForSearch("Fixture Show")
	if shared.ID != fixtureSeriesSpecs[0].ID {
		t.Fatalf("shared query returned series %d, want %d", shared.ID, fixtureSeriesSpecs[0].ID)
	}

	isolated := fixture.fixtureSeriesForSearch("Fixture Isolated run-a")
	if isolated.Title != "Fixture Show run-a" || isolated.EpisodeTitle != "Pilot run-a" {
		t.Fatalf("isolated query was not routed: %+v", isolated)
	}
	if again := fixture.fixtureSeriesForSearch("Fixture Isolated run-a"); again != isolated {
		t.Fatalf("same isolated query should be stable: first=%+v again=%+v", isolated, again)
	}
}
