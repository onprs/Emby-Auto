package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrDuplicateTorrent = errors.New("torrent has already been downloaded")

type DownloadEnqueueCommand struct {
	DownloadID           uuid.UUID
	AcquisitionID        uuid.UUID
	Status               DownloadState
	SourceURI            string
	TorrentHash          string
	FileResolutionSource string
}

type DownloadManifestOutcome string

const (
	DownloadManifestResolved     DownloadManifestOutcome = "resolved"
	DownloadManifestUnresolved   DownloadManifestOutcome = "unresolved"
	DownloadManifestHardRejected DownloadManifestOutcome = "hard_rejected"
)

type DownloadEnqueueCompletion struct {
	OperationID uuid.UUID
	DownloadID  uuid.UUID
	TorrentHash string
	SavePath    string
	Files       []ClassifiedDownloadFile
	Outcome     DownloadManifestOutcome
	ReasonCode  string
}

type DownloadFileResolutionItem struct {
	FileID        uuid.UUID
	Selected      bool
	SourceSeason  *int
	SourceEpisode *int
}

type DownloadSelectionApplyCommand struct {
	DownloadID          uuid.UUID
	AcquisitionID       uuid.UUID
	Status              DownloadState
	TorrentHash         string
	SelectedFileIndexes []int
	AllFileIndexes      []int
}

type DownloadSyncFile struct {
	FileIndex int
	SizeBytes int64
}

type DownloadSyncCommand struct {
	DownloadID    uuid.UUID
	Status        DownloadState
	TorrentHash   string
	ClientState   string
	LastSyncedAt  *time.Time
	SelectedFiles []DownloadSyncFile
}
