package domain

import "github.com/google/uuid"

// AcquisitionDeletionDownload describes one source download owned by a
// lifecycle task. Shared external resources are retained while the workflow
// record itself is still removed.
type AcquisitionDeletionDownload struct {
	ID              uuid.UUID
	TorrentHash     string
	SavePath        string
	PreserveTorrent bool
	PreservePath    bool
}

// AcquisitionDeletionLibraryFile is one successful import destination. Preserve
// is set when another live acquisition still references the same path.
type AcquisitionDeletionLibraryFile struct {
	FilePath string
	Preserve bool
}

// AcquisitionDeletionCommand is the complete cleanup inventory for one
// acquisition. LibraryFiles are acted on only by an explicit delete-imported
// subscription command.
type AcquisitionDeletionCommand struct {
	AcquisitionID uuid.UUID
	TaskIDs       []uuid.UUID
	ArtifactPaths []string
	LibraryFiles  []AcquisitionDeletionLibraryFile
	Downloads     []AcquisitionDeletionDownload
}

// AcquisitionDeletionResult records the database resources removed after the
// external qBittorrent and filesystem cleanup has completed.
type AcquisitionDeletionResult struct {
	AcquisitionID uuid.UUID
	TasksRemoved  int64
	Downloads     int
	Artifacts     int
}
