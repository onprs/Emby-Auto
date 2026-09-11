package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// RSSSubtitleGroupSource 提供 feed lookup 使用的已维护字幕组名称。
type RSSSubtitleGroupSource interface {
	ListRSSSubtitleGroupNames(context.Context) ([]string, error)
}

// RSSSubtitleGroupWorkflow 负责字幕组清单的持久化和管理接口。
type RSSSubtitleGroupWorkflow struct {
	queries    *db.Queries
	transactor *database.Transactor
}

func NewRSSSubtitleGroupWorkflow(queries *db.Queries, transactor *database.Transactor) *RSSSubtitleGroupWorkflow {
	return &RSSSubtitleGroupWorkflow{queries: queries, transactor: transactor}
}

func (workflow *RSSSubtitleGroupWorkflow) List(ctx context.Context) (domain.RSSSubtitleGroupPage, error) {
	rows, err := workflow.queries.ListRSSSubtitleGroups(ctx)
	if err != nil {
		return domain.RSSSubtitleGroupPage{}, fmt.Errorf("list RSS subtitle groups: %w", err)
	}
	items := make([]domain.RSSSubtitleGroup, 0, len(rows))
	for _, row := range rows {
		items = append(items, rssSubtitleGroupFromRow(row))
	}
	return domain.RSSSubtitleGroupPage{Items: items}, nil
}

func (workflow *RSSSubtitleGroupWorkflow) ListRSSSubtitleGroupNames(ctx context.Context) ([]string, error) {
	groups, err := workflow.queries.ListRSSSubtitleGroupNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("list RSS subtitle group names: %w", err)
	}
	return groups, nil
}

func (workflow *RSSSubtitleGroupWorkflow) Create(ctx context.Context, input domain.CreateRSSSubtitleGroup) (domain.RSSSubtitleGroup, error) {
	name, normalized, err := normalizeRSSSubtitleGroupInput(input.Name)
	if err != nil {
		return domain.RSSSubtitleGroup{}, err
	}
	if workflow.transactor == nil {
		return domain.RSSSubtitleGroup{}, errors.New("RSS subtitle group storage is unavailable")
	}
	var result domain.RSSSubtitleGroup
	err = workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		row, createErr := scope.Queries.CreateRSSSubtitleGroup(ctx, db.CreateRSSSubtitleGroupParams{
			ID:             repository.UUIDToPG(uuid.New()),
			Name:           name,
			NormalizedName: normalized,
		})
		if createErr != nil {
			return rssSubtitleGroupMutationError("create RSS subtitle group", createErr)
		}
		result = rssSubtitleGroupFromRow(row)
		return appendRSSSubtitleGroupEvent(ctx, scope.Queries, result, input.ActorUserID, "rss.subtitle_group.created")
	})
	if err != nil {
		return domain.RSSSubtitleGroup{}, err
	}
	return result, nil
}

func (workflow *RSSSubtitleGroupWorkflow) Update(ctx context.Context, input domain.UpdateRSSSubtitleGroup) (domain.RSSSubtitleGroup, error) {
	if input.ID == uuid.Nil {
		return domain.RSSSubtitleGroup{}, domain.ErrNotFound
	}
	name, normalized, err := normalizeRSSSubtitleGroupInput(input.Name)
	if err != nil {
		return domain.RSSSubtitleGroup{}, err
	}
	if workflow.transactor == nil {
		return domain.RSSSubtitleGroup{}, errors.New("RSS subtitle group storage is unavailable")
	}
	var result domain.RSSSubtitleGroup
	err = workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		row, updateErr := scope.Queries.UpdateRSSSubtitleGroup(ctx, db.UpdateRSSSubtitleGroupParams{
			ID:             repository.UUIDToPG(input.ID),
			Name:           name,
			NormalizedName: normalized,
		})
		if errors.Is(updateErr, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if updateErr != nil {
			return rssSubtitleGroupMutationError("update RSS subtitle group", updateErr)
		}
		result = rssSubtitleGroupFromRow(row)
		return appendRSSSubtitleGroupEvent(ctx, scope.Queries, result, input.ActorUserID, "rss.subtitle_group.updated")
	})
	if err != nil {
		return domain.RSSSubtitleGroup{}, err
	}
	return result, nil
}

func (workflow *RSSSubtitleGroupWorkflow) Delete(ctx context.Context, id uuid.UUID, actorUserID uuid.UUID) error {
	if id == uuid.Nil {
		return domain.ErrNotFound
	}
	if workflow.transactor == nil {
		return errors.New("RSS subtitle group storage is unavailable")
	}
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		row, err := scope.Queries.GetRSSSubtitleGroup(ctx, repository.UUIDToPG(id))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load RSS subtitle group for deletion: %w", err)
		}
		deleted, err := scope.Queries.DeleteRSSSubtitleGroup(ctx, repository.UUIDToPG(id))
		if err != nil {
			return fmt.Errorf("delete RSS subtitle group: %w", err)
		}
		if deleted != 1 {
			return domain.ErrNotFound
		}
		return appendRSSSubtitleGroupEvent(ctx, scope.Queries, rssSubtitleGroupFromRow(row), actorUserID, "rss.subtitle_group.deleted")
	})
}

func normalizeRSSSubtitleGroupInput(value string) (string, string, error) {
	name := domain.NormalizeRSSSubtitleGroupName(value)
	if name == "" {
		return "", "", invalidRSSSubtitleGroup("name", "must not be blank")
	}
	if utf8.RuneCountInString(name) > 128 {
		return "", "", invalidRSSSubtitleGroup("name", "must not exceed 128 characters")
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(name), " "))
	return name, normalized, nil
}

func rssSubtitleGroupFromRow(row db.RssSubtitleGroup) domain.RSSSubtitleGroup {
	return domain.RSSSubtitleGroup{
		ID:        repository.UUIDFromPG(row.ID),
		Name:      row.Name,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}
}

func appendRSSSubtitleGroupEvent(ctx context.Context, queries *db.Queries, group domain.RSSSubtitleGroup, actorUserID uuid.UUID, topic string) error {
	resourceType := "rss_subtitle_group"
	data, err := json.Marshal(map[string]any{"name": group.Name})
	if err != nil {
		return fmt.Errorf("encode RSS subtitle group event: %w", err)
	}
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           repository.UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(group.ID),
		ActorUserID:  repository.UUIDToPG(actorUserID),
		Data:         data,
	}); err != nil {
		return fmt.Errorf("append RSS subtitle group event: %w", err)
	}
	return nil
}

func rssSubtitleGroupMutationError(action string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return NewError("rss_subtitle_group_exists", "the RSS subtitle group already exists", ErrStateConflict, map[string]any{})
	}
	return fmt.Errorf("%s: %w", action, err)
}

func invalidRSSSubtitleGroup(field, reason string) *Error {
	return NewError("invalid_rss_subtitle_group", "the RSS subtitle group is invalid", ErrInvalidInput, map[string]any{
		"field":  field,
		"reason": reason,
	})
}
