package service

import (
	"testing"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func TestValidateDownloadFileResolutionAcceptsCompleteVideoAndSubtitleManifest(t *testing.T) {
	videoID := uuid.MustParse("43000000-0000-0000-0000-000000000001")
	subtitleID := uuid.MustParse("43000000-0000-0000-0000-000000000002")
	extraID := uuid.MustParse("43000000-0000-0000-0000-000000000003")
	season, episode := 1, 7
	items := []domain.DownloadFileResolutionItem{
		{FileID: videoID, Selected: true, SourceSeason: &season, SourceEpisode: &episode},
		{FileID: subtitleID, Selected: true, SourceSeason: &season, SourceEpisode: &episode},
		{FileID: extraID, Selected: false},
	}
	files := []db.DownloadFile{
		{ID: repository.UUIDToPG(videoID), MediaKind: "video"},
		{ID: repository.UUIDToPG(subtitleID), MediaKind: "subtitle"},
		{ID: repository.UUIDToPG(extraID), MediaKind: "extra"},
	}
	result, err := validateDownloadFileResolution(files, items)
	if err != nil {
		t.Fatalf("validateDownloadFileResolution() error = %v", err)
	}
	if len(result) != 3 || !result[0].Selected || !result[1].Selected || result[2].Selected {
		t.Fatalf("normalized result = %#v", result)
	}
}

func TestValidateDownloadFileResolutionRejectsUnsafeSelections(t *testing.T) {
	video1 := uuid.MustParse("43000000-0000-0000-0000-000000000011")
	video2 := uuid.MustParse("43000000-0000-0000-0000-000000000012")
	subtitle := uuid.MustParse("43000000-0000-0000-0000-000000000013")
	extra := uuid.MustParse("43000000-0000-0000-0000-000000000014")
	files := []db.DownloadFile{
		{ID: repository.UUIDToPG(video1), MediaKind: "video"},
		{ID: repository.UUIDToPG(video2), MediaKind: "video"},
		{ID: repository.UUIDToPG(subtitle), MediaKind: "subtitle"},
		{ID: repository.UUIDToPG(extra), MediaKind: "extra"},
	}
	one, two, seven := 1, 2, 7
	tests := []struct {
		name  string
		items []domain.DownloadFileResolutionItem
		code  string
	}{
		{
			name:  "requires the complete manifest",
			items: []domain.DownloadFileResolutionItem{{FileID: video1, Selected: true, SourceSeason: &one, SourceEpisode: &one}},
			code:  "download_file_resolution_invalid",
		},
		{
			name: "rejects duplicate video coordinates",
			items: []domain.DownloadFileResolutionItem{
				{FileID: video1, Selected: true, SourceSeason: &one, SourceEpisode: &seven},
				{FileID: video2, Selected: true, SourceSeason: &one, SourceEpisode: &seven},
				{FileID: subtitle}, {FileID: extra},
			},
			code: "download_coordinate_duplicate",
		},
		{
			name: "rejects subtitle without selected video coordinate",
			items: []domain.DownloadFileResolutionItem{
				{FileID: video1, Selected: true, SourceSeason: &one, SourceEpisode: &one},
				{FileID: video2},
				{FileID: subtitle, Selected: true, SourceSeason: &one, SourceEpisode: &two},
				{FileID: extra},
			},
			code: "download_subtitle_video_invalid",
		},
		{
			name: "rejects selected extra",
			items: []domain.DownloadFileResolutionItem{
				{FileID: video1, Selected: true, SourceSeason: &one, SourceEpisode: &one},
				{FileID: video2}, {FileID: subtitle},
				{FileID: extra, Selected: true, SourceSeason: &one, SourceEpisode: &one},
			},
			code: "download_media_kind_invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateDownloadFileResolution(files, test.items)
			serviceErr, ok := err.(*Error)
			if !ok || serviceErr.Code != test.code {
				t.Fatalf("error = %#v, want code %q", err, test.code)
			}
		})
	}
}
