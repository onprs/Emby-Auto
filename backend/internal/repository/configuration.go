package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

type configurationQueries interface {
	GetAppSetting(context.Context, string) (db.AppSetting, error)
	ListAppSecrets(context.Context) ([]db.AppSecret, error)
	GetAppSecret(context.Context, string) (db.AppSecret, error)
}

type Configuration struct {
	queries    configurationQueries
	transactor *database.Transactor
}

func NewConfiguration(queries configurationQueries, transactor *database.Transactor) *Configuration {
	return &Configuration{queries: queries, transactor: transactor}
}

func (repository *Configuration) Load(ctx context.Context) (domain.Configuration, error) {
	configuration := domain.Configuration{
		Settings: domain.RuntimeSettings{
			Agent:     domain.DefaultAgentSettings(),
			Transcode: domain.DefaultTranscodeProfile(),
		},
		Secrets: map[string]domain.SecretMetadata{
			domain.SecretQBittorrentPassword: {Name: domain.SecretQBittorrentPassword},
			domain.SecretEmbyAPIKey:          {Name: domain.SecretEmbyAPIKey},
			domain.SecretTMDbAPIToken:        {Name: domain.SecretTMDbAPIToken},
			domain.SecretAgentAPIKey:         {Name: domain.SecretAgentAPIKey},
		},
	}

	setting, err := repository.queries.GetAppSetting(ctx, domain.RuntimeSettingsName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.Configuration{}, fmt.Errorf("get runtime settings: %w", err)
	}
	if err == nil {
		if unmarshalErr := json.Unmarshal(setting.Value, &configuration.Settings); unmarshalErr != nil {
			return domain.Configuration{}, fmt.Errorf("decode runtime settings: %w", unmarshalErr)
		}
		configuration.Version = setting.Version
	}

	secrets, err := repository.queries.ListAppSecrets(ctx)
	if err != nil {
		return domain.Configuration{}, fmt.Errorf("list application secrets: %w", err)
	}
	for _, secret := range secrets {
		if _, known := configuration.Secrets[secret.Name]; !known {
			continue
		}
		configuration.Secrets[secret.Name] = domain.SecretMetadata{
			Name:       secret.Name,
			Configured: true,
			MaskedHint: secret.MaskedHint,
		}
	}
	return configuration, nil
}

func (repository *Configuration) Save(
	ctx context.Context,
	save domain.SaveConfiguration,
) (domain.Configuration, error) {
	current, err := repository.Load(ctx)
	if err != nil {
		return domain.Configuration{}, err
	}

	var savedSetting db.SaveAppSettingRow
	err = repository.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		var saveErr error
		savedSetting, saveErr = SaveConfigurationInTx(ctx, scope, save)
		return saveErr
	})
	if err != nil {
		return domain.Configuration{}, err
	}

	current.Version = savedSetting.Version
	current.Settings = save.Settings
	for _, mutation := range save.Secrets {
		if mutation.Delete {
			current.Secrets[mutation.Name] = domain.SecretMetadata{Name: mutation.Name}
			continue
		}
		current.Secrets[mutation.Name] = domain.SecretMetadata{
			Name:       mutation.Name,
			Configured: true,
			MaskedHint: mutation.Value.MaskedHint,
		}
	}
	return current, nil
}

func SaveConfigurationInTx(
	ctx context.Context,
	scope database.TxScope,
	save domain.SaveConfiguration,
) (db.SaveAppSettingRow, error) {
	settingsJSON, err := json.Marshal(save.Settings)
	if err != nil {
		return db.SaveAppSettingRow{}, fmt.Errorf("encode runtime settings: %w", err)
	}
	setting, err := scope.Queries.SaveAppSetting(ctx, db.SaveAppSettingParams{
		Name:            domain.RuntimeSettingsName,
		Value:           settingsJSON,
		UpdatedBy:       UUIDToPG(save.UpdatedBy),
		ExpectedVersion: save.ExpectedVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.SaveAppSettingRow{}, domain.ErrVersionConflict
	}
	if err != nil {
		return db.SaveAppSettingRow{}, fmt.Errorf("save runtime settings: %w", err)
	}

	profile := save.Settings.Transcode
	var audioCodec *string
	if profile.AudioCodec != "" {
		audioCodec = &profile.AudioCodec
	}
	if _, err := scope.Queries.ReplaceDefaultTranscodeProfile(ctx, db.ReplaceDefaultTranscodeProfileParams{
		ID:                UUIDToPG(uuid.New()),
		Name:              profile.Name,
		VideoCodec:        profile.VideoCodec,
		Encoder:           profile.Encoder,
		Container:         profile.Container,
		FileExtension:     profile.FileExtension,
		QualityMode:       profile.QualityMode,
		QualityValueMilli: int64(math.Round(profile.QualityValue * 1000)),
		AudioPolicy:       profile.AudioPolicy,
		AudioCodec:        audioCodec,
		Preset:            profile.Preset,
		PixelFormat:       profile.PixelFormat,
		ThreadCount:       int32(profile.ThreadCount),
		MaxConcurrency:    int32(profile.MaxConcurrency),
		CreatedBy:         UUIDToPG(save.UpdatedBy),
	}); err != nil {
		return db.SaveAppSettingRow{}, fmt.Errorf("save default transcode profile: %w", err)
	}

	for _, mutation := range save.Secrets {
		if mutation.Delete {
			if _, err := scope.Queries.DeleteAppSecret(ctx, mutation.Name); err != nil {
				return db.SaveAppSettingRow{}, fmt.Errorf("delete application secret %q: %w", mutation.Name, err)
			}
			continue
		}
		if mutation.Value == nil {
			return db.SaveAppSettingRow{}, fmt.Errorf("secret mutation %q has no encrypted value", mutation.Name)
		}
		if _, err := scope.Queries.UpsertAppSecret(ctx, db.UpsertAppSecretParams{
			Name:       mutation.Name,
			Ciphertext: mutation.Value.Ciphertext,
			Nonce:      mutation.Value.Nonce,
			MaskedHint: mutation.Value.MaskedHint,
			UpdatedBy:  UUIDToPG(save.UpdatedBy),
		}); err != nil {
			return db.SaveAppSettingRow{}, fmt.Errorf("save application secret %q: %w", mutation.Name, err)
		}
	}

	eventData, err := json.Marshal(map[string]any{"version": setting.Version})
	if err != nil {
		return db.SaveAppSettingRow{}, fmt.Errorf("encode configuration event: %w", err)
	}
	if _, err := scope.Queries.AppendEvent(ctx, db.AppendEventParams{
		ID:          UUIDToPG(uuid.New()),
		Topic:       "configuration.updated",
		ActorUserID: UUIDToPG(save.UpdatedBy),
		Data:        eventData,
	}); err != nil {
		return db.SaveAppSettingRow{}, fmt.Errorf("append configuration event: %w", err)
	}
	return setting, nil
}

func (repository *Configuration) GetSecret(ctx context.Context, name string) (domain.EncryptedSecret, error) {
	secret, err := repository.queries.GetAppSecret(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EncryptedSecret{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.EncryptedSecret{}, fmt.Errorf("get application secret %q: %w", name, err)
	}
	return domain.EncryptedSecret{
		Name:       secret.Name,
		Ciphertext: secret.Ciphertext,
		Nonce:      secret.Nonce,
		MaskedHint: secret.MaskedHint,
	}, nil
}
