package domain

import "github.com/google/uuid"

type SetupState string

const (
	SetupRequired     SetupState = "required"
	SetupInitializing SetupState = "initializing"
	SetupCompleted    SetupState = "completed"
)

type SetupStatus struct {
	State                     SetupState
	DatabaseConfigured        bool
	DatabaseManagedExternally bool
	AdministratorConfigured   bool
}

type SetupDatabase struct {
	Host     string
	Port     int
	Database string
	Username string
	Password string
	SSLMode  string
}

type SetupSecrets struct {
	QBittorrentPassword string
	EmbyAPIKey          string
	TMDbAPIToken        string
}

type InitializeSetup struct {
	Database              *SetupDatabase
	AdministratorUsername string
	AdministratorPassword string
	Settings              RuntimeSettings
	Secrets               SetupSecrets
}

type RuntimeBootstrap struct {
	DatabaseURL         string
	ConfigEncryptionKey []byte
	AdminID             uuid.UUID
}
