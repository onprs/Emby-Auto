package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/google/uuid"
)

const (
	bootstrapFileVersion = 1
	maxBootstrapBytes    = 16 << 10
)

type BootstrapData struct {
	Version           int       `json:"version"`
	DatabaseURL       string    `json:"databaseUrl,omitempty"`
	EncryptionKey     string    `json:"encryptionKey,omitempty"`
	AdminID           uuid.UUID `json:"adminId"`
	AdminUsername     string    `json:"adminUsername"`
	AdminPasswordHash string    `json:"adminPasswordHash"`
}

type bootstrapCompletion struct {
	Version    int       `json:"version"`
	AdminID    uuid.UUID `json:"adminId"`
	ConfigHash string    `json:"configSha256"`
}

type BootstrapStore struct {
	path string
}

func NewBootstrapStore(path string) *BootstrapStore {
	return &BootstrapStore{path: filepath.Clean(path)}
}

func (store *BootstrapStore) Path() string {
	return store.path
}

func (store *BootstrapStore) Load() (BootstrapData, bool, error) {
	data, encoded, err := store.loadData()
	if err != nil {
		return BootstrapData{}, false, err
	}
	completionPath := store.path + ".complete"
	completionEncoded, err := readRestrictedFile(completionPath)
	if errors.Is(err, os.ErrNotExist) {
		return data, false, nil
	}
	if err != nil {
		return BootstrapData{}, false, fmt.Errorf("read bootstrap completion marker: %w", err)
	}
	var completion bootstrapCompletion
	if err := decodeSingleJSON(completionEncoded, &completion); err != nil {
		return BootstrapData{}, false, fmt.Errorf("decode bootstrap completion marker: %w", err)
	}
	hash := sha256.Sum256(encoded)
	if completion.Version != bootstrapFileVersion || completion.AdminID != data.AdminID || completion.ConfigHash != fmt.Sprintf("%x", hash[:]) {
		return BootstrapData{}, false, fmt.Errorf("bootstrap completion marker does not match the configuration")
	}
	return data, true, nil
}

func (store *BootstrapStore) WriteConfigured(data BootstrapData) error {
	data.Version = bootstrapFileVersion
	if data.AdminID == uuid.Nil || data.AdminUsername == "" || data.AdminPasswordHash == "" {
		return fmt.Errorf("bootstrap administrator identity is required")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode bootstrap configuration: %w", err)
	}
	encoded = append(encoded, '\n')
	if existing, _, err := store.loadData(); err == nil {
		existing.Version = bootstrapFileVersion
		existingEncoded, encodeErr := json.Marshal(existing)
		if encodeErr != nil {
			return fmt.Errorf("encode existing bootstrap configuration: %w", encodeErr)
		}
		existingEncoded = append(existingEncoded, '\n')
		if !bytes.Equal(existingEncoded, encoded) {
			return fmt.Errorf("bootstrap configuration already exists with different settings")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeNewRestrictedFile(store.path, encoded); err != nil {
		return fmt.Errorf("write bootstrap configuration: %w", err)
	}
	return nil
}

func (store *BootstrapStore) MarkCompleted() error {
	data, encoded, err := store.loadData()
	if err != nil {
		return err
	}
	hash := sha256.Sum256(encoded)
	completion := bootstrapCompletion{
		Version:    bootstrapFileVersion,
		AdminID:    data.AdminID,
		ConfigHash: fmt.Sprintf("%x", hash[:]),
	}
	completionEncoded, err := json.Marshal(completion)
	if err != nil {
		return fmt.Errorf("encode bootstrap completion marker: %w", err)
	}
	completionEncoded = append(completionEncoded, '\n')
	path := store.path + ".complete"
	if existing, err := readRestrictedFile(path); err == nil {
		if !bytes.Equal(existing, completionEncoded) {
			return fmt.Errorf("bootstrap completion marker already exists with different settings")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeNewRestrictedFile(path, completionEncoded); err != nil {
		return fmt.Errorf("write bootstrap completion marker: %w", err)
	}
	return nil
}

func (store *BootstrapStore) loadData() (BootstrapData, []byte, error) {
	encoded, err := readRestrictedFile(store.path)
	if err != nil {
		return BootstrapData{}, nil, err
	}
	var data BootstrapData
	if err := decodeSingleJSON(encoded, &data); err != nil {
		return BootstrapData{}, nil, fmt.Errorf("decode bootstrap configuration: %w", err)
	}
	if data.Version != bootstrapFileVersion || data.AdminID == uuid.Nil || data.AdminUsername == "" || data.AdminPasswordHash == "" {
		return BootstrapData{}, nil, fmt.Errorf("bootstrap configuration has an unsupported version or administrator ID")
	}
	return data, encoded, nil
}

func readRestrictedFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("bootstrap path must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("bootstrap file permissions must not allow group or other access")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	encoded, err := io.ReadAll(io.LimitReader(file, maxBootstrapBytes+1))
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxBootstrapBytes {
		return nil, fmt.Errorf("bootstrap file exceeds %d bytes", maxBootstrapBytes)
	}
	return encoded, nil
}

func writeNewRestrictedFile(path string, encoded []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil && runtime.GOOS != "windows" {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".bootstrap-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func decodeSingleJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
