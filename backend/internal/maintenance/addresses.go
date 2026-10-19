package maintenance

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

type AddressChange struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type AddressBackup struct {
	Format  int             `json:"format"`
	Version int32           `json:"version"`
	Changes []AddressChange `json:"changes"`
}

type AddressResult struct {
	Applied bool     `json:"applied"`
	Version int32    `json:"version"`
	Fields  []string `json:"fields"`
}

func PlanAddresses(settings domain.RuntimeSettings, from, to string) (domain.RuntimeSettings, []AddressChange, error) {
	if from == "" || net.ParseIP(to) == nil || !net.ParseIP(to).IsLoopback() {
		return settings, nil, errors.New("目标服务地址必须是回环 IP")
	}
	updated := settings
	changes := make([]AddressChange, 0, 2)
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"qBittorrent.url", &updated.QBittorrent.URL},
		{"emby.url", &updated.Emby.URL},
	} {
		value, changed, err := ReplaceURLHost(*field.value, from, to)
		if err != nil {
			return settings, nil, err
		}
		if changed {
			changes = append(changes, AddressChange{Field: field.name, Before: *field.value, After: value})
			*field.value = value
		}
	}
	return updated, changes, nil
}

func RestoreAddresses(settings domain.RuntimeSettings, backup AddressBackup) (domain.RuntimeSettings, []AddressChange, error) {
	if backup.Format != 1 {
		return settings, nil, errors.New("地址快照版本无效")
	}
	updated := settings
	changes := make([]AddressChange, 0, len(backup.Changes))
	seen := make(map[string]bool)
	for _, change := range backup.Changes {
		var value *string
		switch change.Field {
		case "qBittorrent.url":
			value = &updated.QBittorrent.URL
		case "emby.url":
			value = &updated.Emby.URL
		default:
			return settings, nil, errors.New("地址快照包含范围外字段")
		}
		if seen[change.Field] {
			return settings, nil, errors.New("地址快照包含重复字段")
		}
		seen[change.Field] = true
		if *value == change.Before {
			continue
		}
		if *value != change.After {
			return settings, nil, errors.New("当前服务地址与恢复快照不一致")
		}
		changes = append(changes, AddressChange{Field: change.Field, Before: change.After, After: change.Before})
		*value = change.Before
	}
	return updated, changes, nil
}

func (scope *Scope) MigrateAddresses(ctx context.Context, from, to, backupPath, restorePath string, apply bool, expectedChanges int) (AddressResult, error) {
	var result AddressResult
	err := scope.WithStopped(ctx, func() error {
		current, err := scope.Configuration.Load(ctx)
		if err != nil {
			return err
		}
		var updated domain.RuntimeSettings
		var changes []AddressChange
		if restorePath != "" {
			var backup AddressBackup
			if err := ReadPrivateJSON(restorePath, &backup); err != nil {
				return err
			}
			updated, changes, err = RestoreAddresses(current.Settings, backup)
		} else {
			updated, changes, err = PlanAddresses(current.Settings, from, to)
		}
		if err != nil {
			return err
		}
		result.Version = current.Version
		result.Fields = make([]string, 0, len(changes))
		for _, change := range changes {
			result.Fields = append(result.Fields, change.Field)
		}
		if expectedChanges >= 0 && len(changes) != 0 && len(changes) != expectedChanges {
			return errors.New("服务地址变更数量不符合预期")
		}
		if !apply || len(changes) == 0 {
			return nil
		}
		if restorePath == "" {
			backup := AddressBackup{Format: 1, Version: current.Version, Changes: changes}
			if _, err := os.Lstat(backupPath); err == nil {
				var previous AddressBackup
				if err := ReadPrivateJSON(backupPath, &previous); err != nil {
					return err
				}
				if !sameAddressBackup(backup, previous) {
					return errors.New("已有地址快照与本次迁移不一致")
				}
			} else if err := WritePrivateJSON(backupPath, backup); err != nil {
				return err
			}
		}
		secrets := make(map[string]domain.SecretUpdate)
		for _, name := range []string{domain.SecretQBittorrentPassword, domain.SecretEmbyAPIKey, domain.SecretTMDbAPIToken, domain.SecretAgentAPIKey} {
			secrets[name] = domain.SecretUpdate{Action: domain.SecretKeep}
		}
		saved, err := scope.Configuration.Update(ctx, domain.ConfigurationUpdate{
			ExpectedVersion: current.Version, Settings: updated, Events: &updated.Events, Secrets: secrets,
		}, scope.ActorID)
		if err != nil {
			return err
		}
		result.Applied = true
		result.Version = saved.Version
		return nil
	})
	return result, err
}

func sameAddressBackup(left, right AddressBackup) bool {
	if left.Format != right.Format || left.Version != right.Version || len(left.Changes) != len(right.Changes) {
		return false
	}
	for i := range left.Changes {
		if left.Changes[i] != right.Changes[i] {
			return false
		}
	}
	return true
}
