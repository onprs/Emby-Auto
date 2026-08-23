package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestCreateSearchValidatesCommandBeforeDatabaseAccess(t *testing.T) {
	workflow := NewSearchWorkflow(nil, nil, nil)
	actor := uuid.MustParse("71000000-0000-0000-0000-000000000001")
	tests := []struct {
		name  string
		input domain.CreateSearch
		field string
	}{
		{name: "blank query", input: domain.CreateSearch{Query: "  ", IdempotencyKey: "search-1", ActorUserID: actor}, field: "query"},
		{name: "blank key", input: domain.CreateSearch{Query: "Show", IdempotencyKey: " ", ActorUserID: actor}, field: "idempotencyKey"},
		{name: "missing actor", input: domain.CreateSearch{Query: "Show", IdempotencyKey: "search-1"}, field: "actorUserId"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := workflow.CreateSearch(context.Background(), test.input)
			var serviceErr *Error
			if !errors.As(err, &serviceErr) || !errors.Is(err, ErrInvalidInput) || serviceErr.Code != "invalid_search" || serviceErr.Details["field"] != test.field {
				t.Fatalf("CreateSearch() error = %#v, want invalid field %q", err, test.field)
			}
		})
	}
}

func TestCreateSearchAcquisitionValidatesCoordinatesBeforeDatabaseAccess(t *testing.T) {
	workflow := NewSearchWorkflow(nil, nil, nil)
	base := domain.CreateSearchAcquisition{
		CandidateID:    uuid.MustParse("71000000-0000-0000-0000-000000000002"),
		TMDbSeriesID:   42,
		SeriesTitle:    "Canonical Show",
		SourceSeason:   1,
		SourceEpisode:  1,
		SingleEpisode:  true,
		IdempotencyKey: "acquire-1",
		ActorUserID:    uuid.MustParse("71000000-0000-0000-0000-000000000003"),
	}
	tests := []struct {
		name  string
		edit  func(*domain.CreateSearchAcquisition)
		field string
	}{
		{name: "missing candidate", edit: func(input *domain.CreateSearchAcquisition) { input.CandidateID = uuid.Nil }, field: "candidateId"},
		{name: "missing series", edit: func(input *domain.CreateSearchAcquisition) { input.TMDbSeriesID = 0 }, field: "tmdbSeriesId"},
		{name: "missing season", edit: func(input *domain.CreateSearchAcquisition) { input.SourceSeason = 0 }, field: "sourceSeason"},
		{name: "single episode missing coordinate", edit: func(input *domain.CreateSearchAcquisition) { input.SourceEpisode = 0 }, field: "sourceEpisode"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			_, err := workflow.CreateAcquisition(context.Background(), input)
			var serviceErr *Error
			if !errors.As(err, &serviceErr) || serviceErr.Code != "invalid_acquisition" || serviceErr.Details["field"] != test.field {
				t.Fatalf("CreateAcquisition() error = %#v, want invalid field %q", err, test.field)
			}
		})
	}
}

func TestCreateSearchAcquisitionAllowsSeasonPackWithZeroEpisode(t *testing.T) {
	valid := domain.CreateSearchAcquisition{
		CandidateID:    uuid.MustParse("71000000-0000-0000-0000-000000000002"),
		MediaType:      domain.TaskMediaEpisode,
		TMDbSeriesID:   42,
		SeriesTitle:    "Canonical Show",
		SourceSeason:   1,
		SourceEpisode:  0,
		SingleEpisode:  false,
		IdempotencyKey: "acquire-1",
		ActorUserID:    uuid.MustParse("71000000-0000-0000-0000-000000000003"),
	}
	if err := validateSearchAcquisition(valid); err != nil {
		t.Fatalf("validateSearchAcquisition() season pack zero episode error = %#v, want nil", err)
	}
	invalid := valid
	invalid.SingleEpisode = true
	if err := validateSearchAcquisition(invalid); err == nil {
		t.Fatal("validateSearchAcquisition() single episode zero episode error = nil, want invalid_acquisition")
	} else {
		var serviceErr *Error
		if !errors.As(err, &serviceErr) || serviceErr.Code != "invalid_acquisition" || serviceErr.Details["field"] != "sourceEpisode" {
			t.Fatalf("validateSearchAcquisition() single zero error = %#v, want invalid_acquisition field sourceEpisode", err)
		}
	}
}

func TestDeterministicResourceIDSeparatesCommands(t *testing.T) {
	first := deterministicResourceID("search.run:user:key")
	if first != deterministicResourceID("search.run:user:key") {
		t.Fatal("same command key produced different resource IDs")
	}
	if first == deterministicResourceID("search.run:user:other") {
		t.Fatal("different command keys produced the same resource ID")
	}
}
