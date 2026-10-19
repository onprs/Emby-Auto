package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func BenchmarkAgentStaticContextTool(b *testing.B) {
	for _, test := range []struct{ name, title string }{
		{name: "short_titles", title: strings.Repeat("title", 24)},
		{name: "long_unicode_titles", title: strings.Repeat("剧", 2048)},
	} {
		values := make([]scopedRSSReleaseHistory, 50)
		for index := range values {
			values[index] = scopedRSSReleaseHistory{EntryID: uuid.New(), Title: test.title}
		}
		b.Run(test.name+"/build", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				tool := staticAgentTool("list_release_history", "Read scoped history.", map[string]any{}, values)
				if tool.Execute == nil {
					b.Fatal("missing tool executor")
				}
			}
		})
		b.Run(test.name+"/read_first_result", func(b *testing.B) {
			tool := staticAgentTool("list_release_history", "Read scoped history.", map[string]any{}, values)
			arguments := json.RawMessage(`{}`)
			ctx := context.Background()
			var resultBytes int
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				result, err := tool.Execute(ctx, tool.Definition.Name, arguments)
				if err != nil {
					b.Fatal(err)
				}
				resultBytes = len(result)
			}
			b.ReportMetric(float64(resultBytes), "result-B/op")
		})
	}
}

func BenchmarkAgentRSSAdjudicationValidation(b *testing.B) {
	proposal := domain.AgentRSSReleaseAdjudicationProposal{BatchID: uuid.New(), Decision: "resolved"}
	snapshot := agentContextSnapshot{
		RSSAdjudicationEntries: make(map[uuid.UUID]scopedRSSReleaseEntry),
		RSSAdjudicationHistory: make(map[uuid.UUID]scopedRSSReleaseHistory),
	}
	for index := 0; index < 50; index++ {
		entryID := uuid.New()
		snapshot.RSSAdjudicationEntries[entryID] = scopedRSSReleaseEntry{EntryID: entryID}
		season, episode := 1, index+101
		proposal.ScopedEntryIDs = append(proposal.ScopedEntryIDs, entryID)
		proposal.Entries = append(proposal.Entries, domain.AgentRSSReleaseDisposition{
			EntryID: entryID, Disposition: "select", SourceSeason: &season, SourceEpisode: &episode, EvidenceCodes: []string{"scoped_history"},
		})
		historyID, historyEpisode := uuid.New(), index+1
		snapshot.RSSAdjudicationHistory[historyID] = scopedRSSReleaseHistory{EntryID: historyID, SourceSeason: &season, SourceEpisode: &historyEpisode}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		validation := validateRSSReleaseAdjudicationProposal(proposal, snapshot)
		if validation.Verdict != domain.AgentValidationAutoApplicable {
			b.Fatalf("RSS validation = %#v", validation)
		}
	}
}

func BenchmarkAgentDownloadFileValidation(b *testing.B) {
	proposal := domain.AgentDownloadFileResolutionProposal{Decision: "resolved"}
	snapshot := agentContextSnapshot{Files: make(map[uuid.UUID]scopedFile)}
	for index := 0; index < 128; index++ {
		videoID := uuid.New()
		snapshot.Files[videoID] = scopedFile{ID: videoID, MediaKind: string(domain.MediaVideo)}
		proposal.Videos = append(proposal.Videos, domain.AgentDownloadVideoProposal{FileID: videoID, SourceSeason: 1, SourceEpisode: index + 1})
		for range 8 {
			subtitleID := uuid.New()
			snapshot.Files[subtitleID] = scopedFile{ID: subtitleID, MediaKind: string(domain.MediaSubtitle)}
			proposal.Subtitles = append(proposal.Subtitles, domain.AgentDownloadSubtitleProposal{FileID: subtitleID, VideoFileID: videoID})
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		validation := validateDownloadFileProposal(proposal, snapshot)
		if validation.Verdict != domain.AgentValidationAutoApplicable {
			b.Fatalf("download validation = %#v", validation)
		}
	}
}
