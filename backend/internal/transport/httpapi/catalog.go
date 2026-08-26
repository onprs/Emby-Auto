package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (server *Server) SyncTMDbSeries(
	ctx context.Context,
	request SyncTMDbSeriesRequestObject,
) (SyncTMDbSeriesResponseObject, error) {
	if server.catalog == nil {
		return SyncTMDbSeries503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "catalog")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return SyncTMDbSeries401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return SyncTMDbSeries400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	result, err := server.catalog.ScheduleTMDbSync(ctx, domain.SyncTMDbSeries{
		TMDbSeriesID:   request.TmdbSeriesId,
		SeriesTitle:    request.Body.SeriesTitle,
		IdempotencyKey: request.Params.IdempotencyKey,
		ActorUserID:    authenticated.session.User.ID,
	})
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return SyncTMDbSeries400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return SyncTMDbSeries409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return SyncTMDbSeries503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return SyncTMDbSeries202JSONResponse(CatalogCommandAccepted{
			SeriesId:    result.SeriesID,
			OperationId: result.Operation.ID,
			Status:      CatalogCommandAcceptedStatus(result.Operation.Status),
		}), nil
	}
}

func (server *Server) PreviewAcquisitionEpisodeMapping(
	ctx context.Context,
	request PreviewAcquisitionEpisodeMappingRequestObject,
) (PreviewAcquisitionEpisodeMappingResponseObject, error) {
	if server.catalog == nil {
		return PreviewAcquisitionEpisodeMapping503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "catalog")}, nil
	}
	if request.Body == nil {
		return PreviewAcquisitionEpisodeMapping400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	input, decodeErr := mappingPlanInput(uuid.UUID(request.AcquisitionId), *request.Body, "", uuid.Nil)
	if decodeErr != nil {
		return PreviewAcquisitionEpisodeMapping400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "episode mapping request is invalid")}, nil
	}
	preview, err := server.catalog.PreviewEpisodeMapping(ctx, input)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return PreviewAcquisitionEpisodeMapping400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return PreviewAcquisitionEpisodeMapping404JSONResponse{NotFoundJSONResponse: catalogNotFoundError(ctx, "the acquisition was not found")}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return PreviewAcquisitionEpisodeMapping409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return PreviewAcquisitionEpisodeMapping503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return PreviewAcquisitionEpisodeMapping200JSONResponse(mappingPreviewResponse(preview)), nil
	}
}

func (server *Server) SaveAcquisitionEpisodeMapping(
	ctx context.Context,
	request SaveAcquisitionEpisodeMappingRequestObject,
) (SaveAcquisitionEpisodeMappingResponseObject, error) {
	if server.catalog == nil {
		return SaveAcquisitionEpisodeMapping503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "catalog")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return SaveAcquisitionEpisodeMapping401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if request.Body == nil {
		return SaveAcquisitionEpisodeMapping400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}
	input, decodeErr := mappingPlanInput(
		uuid.UUID(request.AcquisitionId),
		*request.Body,
		request.Params.IdempotencyKey,
		authenticated.session.User.ID,
	)
	if decodeErr != nil {
		return SaveAcquisitionEpisodeMapping400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "episode mapping request is invalid")}, nil
	}
	saved, err := server.catalog.SaveEpisodeMapping(ctx, input)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return SaveAcquisitionEpisodeMapping400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return SaveAcquisitionEpisodeMapping404JSONResponse{NotFoundJSONResponse: catalogNotFoundError(ctx, "the acquisition was not found")}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return SaveAcquisitionEpisodeMapping409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return SaveAcquisitionEpisodeMapping503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return SaveAcquisitionEpisodeMapping200JSONResponse(SavedEpisodeMapping{
			ProfileId: saved.ProfileID,
			Version:   int32(saved.Version),
			Preview:   mappingPreviewResponse(saved.Preview),
		}), nil
	}
}

func mappingPlanInput(
	acquisitionID uuid.UUID,
	plan EpisodeMappingPlanRequest,
	idempotencyKey string,
	actorUserID uuid.UUID,
) (domain.EpisodeMappingPlanInput, error) {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return domain.EpisodeMappingPlanInput{}, err
	}
	var envelope struct {
		Mode        json.RawMessage `json:"mode"`
		Anchor      json.RawMessage `json:"anchor"`
		Assignments json.RawMessage `json:"assignments"`
	}
	if err := decodeStrictJSON(encoded, &envelope); err != nil {
		return domain.EpisodeMappingPlanInput{}, err
	}
	mode := domain.EpisodeMappingModeAnchor
	if len(envelope.Mode) != 0 {
		if jsonValueMissing(envelope.Mode) {
			return domain.EpisodeMappingPlanInput{}, errors.New("episode mapping mode must not be null")
		}
		var encodedMode string
		if err := decodeStrictJSON(envelope.Mode, &encodedMode); err != nil {
			return domain.EpisodeMappingPlanInput{}, err
		}
		mode = domain.EpisodeMappingMode(encodedMode)
	}

	input := domain.EpisodeMappingPlanInput{
		AcquisitionID:  acquisitionID,
		Mode:           mode,
		IdempotencyKey: idempotencyKey,
		ActorUserID:    actorUserID,
	}
	switch mode {
	case domain.EpisodeMappingModeAnchor:
		if len(envelope.Anchor) == 0 || jsonValueMissing(envelope.Anchor) || len(envelope.Assignments) != 0 {
			return domain.EpisodeMappingPlanInput{}, errors.New("anchor mapping requires a non-null anchor and must omit assignments")
		}
		anchor, err := decodeEpisodeMappingAnchor(envelope.Anchor)
		if err != nil {
			return domain.EpisodeMappingPlanInput{}, err
		}
		input.Anchor = anchor
		return input, nil
	case domain.EpisodeMappingModeExplicit:
		if len(envelope.Anchor) != 0 || len(envelope.Assignments) == 0 || jsonValueMissing(envelope.Assignments) {
			return domain.EpisodeMappingPlanInput{}, errors.New("explicit mapping requires non-null assignments and must omit anchor")
		}
		assignments, err := decodeEpisodeMappingExplicitAssignments(envelope.Assignments)
		if err != nil {
			return domain.EpisodeMappingPlanInput{}, err
		}
		input.Assignments = assignments
		return input, nil
	default:
		return domain.EpisodeMappingPlanInput{}, errors.New("episode mapping mode is invalid")
	}
}

func decodeEpisodeMappingAnchor(raw json.RawMessage) (domain.EpisodeMappingAnchorInput, error) {
	var encoded struct {
		SourceFileID  json.RawMessage `json:"sourceFileId"`
		TargetSeason  json.RawMessage `json:"targetSeason"`
		TargetEpisode json.RawMessage `json:"targetEpisode"`
	}
	if err := decodeStrictJSON(raw, &encoded); err != nil {
		return domain.EpisodeMappingAnchorInput{}, err
	}
	if jsonValueMissing(encoded.SourceFileID) || jsonValueMissing(encoded.TargetSeason) || jsonValueMissing(encoded.TargetEpisode) {
		return domain.EpisodeMappingAnchorInput{}, errors.New("anchor fields must be present and non-null")
	}
	var sourceFileID uuid.UUID
	var targetSeason, targetEpisode int32
	if err := decodeStrictJSON(encoded.SourceFileID, &sourceFileID); err != nil {
		return domain.EpisodeMappingAnchorInput{}, err
	}
	if err := decodeStrictJSON(encoded.TargetSeason, &targetSeason); err != nil {
		return domain.EpisodeMappingAnchorInput{}, err
	}
	if err := decodeStrictJSON(encoded.TargetEpisode, &targetEpisode); err != nil {
		return domain.EpisodeMappingAnchorInput{}, err
	}
	if targetSeason < 1 || targetEpisode < 1 {
		return domain.EpisodeMappingAnchorInput{}, errors.New("anchor target must be a positive regular episode coordinate")
	}
	return domain.EpisodeMappingAnchorInput{
		SourceFileID: sourceFileID,
		Target:       domain.EpisodeCoordinate{Season: int(targetSeason), Episode: int(targetEpisode)},
	}, nil
}

func decodeEpisodeMappingExplicitAssignments(raw json.RawMessage) ([]domain.EpisodeMappingExplicitInput, error) {
	var encoded []json.RawMessage
	if err := decodeStrictJSON(raw, &encoded); err != nil {
		return nil, err
	}
	if len(encoded) == 0 || len(encoded) > 128 {
		return nil, errors.New("explicit mapping assignments must contain between 1 and 128 items")
	}
	assignments := make([]domain.EpisodeMappingExplicitInput, 0, len(encoded))
	for _, item := range encoded {
		assignment, err := decodeEpisodeMappingExplicitAssignment(item)
		if err != nil {
			return nil, err
		}
		assignments = append(assignments, assignment)
	}
	return assignments, nil
}

func decodeEpisodeMappingExplicitAssignment(raw json.RawMessage) (domain.EpisodeMappingExplicitInput, error) {
	var encoded struct {
		SourceFileID  json.RawMessage `json:"sourceFileId"`
		Action        json.RawMessage `json:"action"`
		TargetSeason  json.RawMessage `json:"targetSeason"`
		TargetEpisode json.RawMessage `json:"targetEpisode"`
	}
	if err := decodeStrictJSON(raw, &encoded); err != nil {
		return domain.EpisodeMappingExplicitInput{}, err
	}
	if jsonValueMissing(encoded.SourceFileID) || jsonValueMissing(encoded.Action) {
		return domain.EpisodeMappingExplicitInput{}, errors.New("explicit mapping disposition source and action must be present and non-null")
	}
	var sourceFileID uuid.UUID
	var encodedAction string
	if err := decodeStrictJSON(encoded.SourceFileID, &sourceFileID); err != nil {
		return domain.EpisodeMappingExplicitInput{}, err
	}
	if err := decodeStrictJSON(encoded.Action, &encodedAction); err != nil {
		return domain.EpisodeMappingExplicitInput{}, err
	}
	action := domain.EpisodeMappingExplicitAction(encodedAction)
	assignment := domain.EpisodeMappingExplicitInput{SourceFileID: sourceFileID, Action: action}
	switch action {
	case domain.EpisodeMappingExplicitMap:
		if jsonValueMissing(encoded.TargetSeason) || jsonValueMissing(encoded.TargetEpisode) {
			return domain.EpisodeMappingExplicitInput{}, errors.New("map disposition targets must be present and non-null")
		}
		var targetSeason, targetEpisode int32
		if err := decodeStrictJSON(encoded.TargetSeason, &targetSeason); err != nil {
			return domain.EpisodeMappingExplicitInput{}, err
		}
		if err := decodeStrictJSON(encoded.TargetEpisode, &targetEpisode); err != nil {
			return domain.EpisodeMappingExplicitInput{}, err
		}
		if targetSeason < 0 || targetEpisode < 1 {
			return domain.EpisodeMappingExplicitInput{}, errors.New("map disposition target is invalid")
		}
		assignment.Target = domain.EpisodeCoordinate{Season: int(targetSeason), Episode: int(targetEpisode)}
		return assignment, nil
	case domain.EpisodeMappingExplicitExclude:
		if len(encoded.TargetSeason) != 0 || len(encoded.TargetEpisode) != 0 {
			return domain.EpisodeMappingExplicitInput{}, errors.New("exclude disposition must omit target fields")
		}
		return assignment, nil
	default:
		return domain.EpisodeMappingExplicitInput{}, errors.New("explicit mapping disposition action is invalid")
	}
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func jsonValueMissing(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func mappingPreviewResponse(preview domain.EpisodeMappingPreview) EpisodeMappingPreview {
	mode := preview.Mode
	if mode == "" {
		mode = domain.EpisodeMappingModeAnchor
	}
	response := EpisodeMappingPreview{
		AcquisitionId: preview.AcquisitionID,
		SeriesId:      preview.SeriesID,
		Mode:          EpisodeMappingMode(mode),
		Rows:          make([]EpisodeMappingRow, 0, len(preview.Rows)),
	}
	if mode == domain.EpisodeMappingModeAnchor {
		response.Anchor = &EpisodeMappingAnchor{
			SourceFileId:  preview.Anchor.SourceFileID,
			TargetSeason:  int32(preview.Anchor.Target.Season),
			TargetEpisode: int32(preview.Anchor.Target.Episode),
		}
	}
	for _, row := range preview.Rows {
		mapped := EpisodeMappingRow{
			SourceFileId:  row.SourceFileID,
			RelativePath:  row.RelativePath,
			SourceSeason:  int32(row.SourceSeason),
			SourceEpisode: int32(row.SourceEpisode),
			Status:        EpisodeMappingRowStatus(row.Status),
			MatchSource:   EpisodeMappingRowMatchSource(row.MatchSource),
		}
		if row.SourceEpisodeFractionHundredths > 0 {
			fraction := int32(row.SourceEpisodeFractionHundredths)
			mapped.SourceEpisodeFractionHundredths = &fraction
		}
		if row.AbsoluteEpisode > 0 {
			value := int32(row.AbsoluteEpisode)
			mapped.AbsoluteEpisode = &value
		}
		if row.TargetEpisode > 0 {
			season := int32(row.TargetSeason)
			episode := int32(row.TargetEpisode)
			mapped.TargetSeason = &season
			mapped.TargetEpisode = &episode
		}
		if row.TargetTitle != "" {
			mapped.TargetTitle = &row.TargetTitle
		}
		if row.ErrorCode != "" {
			mapped.ErrorCode = &row.ErrorCode
		}
		response.Rows = append(response.Rows, mapped)
	}
	return response
}

func (server *Server) RefreshEmbyLibrary(
	ctx context.Context,
	request RefreshEmbyLibraryRequestObject,
) (RefreshEmbyLibraryResponseObject, error) {
	if server.embyCatalog == nil {
		return RefreshEmbyLibrary503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "emby")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return RefreshEmbyLibrary401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	operation, err := server.embyCatalog.ScheduleRefresh(ctx, domain.CreateEmbyRefresh{
		IdempotencyKey: request.Params.IdempotencyKey,
		ActorUserID:    authenticated.session.User.ID,
	})
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return RefreshEmbyLibrary400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return RefreshEmbyLibrary409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return RefreshEmbyLibrary503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return RefreshEmbyLibrary202JSONResponse(CommandAccepted{
			OperationId: operation.ID, Status: CommandAcceptedStatus(operation.Status),
		}), nil
	}
}

func (server *Server) CreateEmbyScan(
	ctx context.Context,
	request CreateEmbyScanRequestObject,
) (CreateEmbyScanResponseObject, error) {
	if server.embyCatalog == nil {
		return CreateEmbyScan503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "emby")}, nil
	}
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return CreateEmbyScan401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	result, err := server.embyCatalog.ScheduleScan(ctx, domain.CreateEmbyScan{
		IdempotencyKey: request.Params.IdempotencyKey,
		ActorUserID:    authenticated.session.User.ID,
	})
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return CreateEmbyScan400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrStateConflict):
		return CreateEmbyScan409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return CreateEmbyScan503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return CreateEmbyScan202JSONResponse(EmbyScanCommandAccepted{
			Scan:        embyScanResponse(result.Scan),
			OperationId: result.Operation.ID,
			Status:      EmbyScanCommandAcceptedStatus(result.Operation.Status),
		}), nil
	}
}

func (server *Server) GetEmbyScan(
	ctx context.Context,
	request GetEmbyScanRequestObject,
) (GetEmbyScanResponseObject, error) {
	if server.embyCatalog == nil {
		return GetEmbyScan503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "emby")}, nil
	}
	scan, err := server.embyCatalog.GetScan(ctx, uuid.UUID(request.ScanId))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetEmbyScan404JSONResponse{NotFoundJSONResponse: catalogNotFoundError(ctx, "the Emby scan was not found")}, nil
	case err != nil:
		return GetEmbyScan503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		return GetEmbyScan200JSONResponse(embyScanResponse(scan)), nil
	}
}

func (server *Server) ListEmbyScans(
	ctx context.Context,
	request ListEmbyScansRequestObject,
) (ListEmbyScansResponseObject, error) {
	if server.embyCatalog == nil {
		return ListEmbyScans503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "emby")}, nil
	}
	limit := 50
	if request.Params.Limit != nil {
		limit = int(*request.Params.Limit)
	}
	var cursor *uuid.UUID
	if request.Params.Cursor != nil {
		value := uuid.UUID(*request.Params.Cursor)
		cursor = &value
	}
	page, err := server.embyCatalog.ListScans(ctx, cursor, limit)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return ListEmbyScans400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return ListEmbyScans400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "the Emby scan cursor was not found")}, nil
	case err != nil:
		return ListEmbyScans503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		response := EmbyScanPage{Items: make([]EmbyScan, 0, len(page.Items))}
		for _, scan := range page.Items {
			response.Items = append(response.Items, embyScanResponse(scan))
		}
		if page.NextCursor != nil {
			value := *page.NextCursor
			response.NextCursor = &value
		}
		return ListEmbyScans200JSONResponse(response), nil
	}
}

func (server *Server) ListEmbyLibraries(
	ctx context.Context,
	_ ListEmbyLibrariesRequestObject,
) (ListEmbyLibrariesResponseObject, error) {
	if server.embyCatalog == nil {
		return ListEmbyLibraries503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "emby")}, nil
	}
	libraries, err := server.embyCatalog.ListLibraries(ctx)
	if err != nil {
		return ListEmbyLibraries503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	response := make([]EmbyLibrary, 0, len(libraries))
	for _, library := range libraries {
		mapped := EmbyLibrary{
			Id:         library.ID,
			EmbyId:     library.EmbyID,
			Name:       library.Name,
			Locations:  library.Locations,
			Present:    library.Present,
			LastSeenAt: library.LastSeenAt,
		}
		if library.CollectionType != "" {
			mapped.CollectionType = &library.CollectionType
		}
		response = append(response, mapped)
	}
	return ListEmbyLibraries200JSONResponse(response), nil
}

func (server *Server) ListEmbyLibraryItems(
	ctx context.Context,
	request ListEmbyLibraryItemsRequestObject,
) (ListEmbyLibraryItemsResponseObject, error) {
	if server.embyCatalog == nil {
		return ListEmbyLibraryItems503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "emby")}, nil
	}
	limit := 100
	if request.Params.Limit != nil {
		limit = int(*request.Params.Limit)
	}
	itemType := ""
	if request.Params.ItemType != nil {
		itemType = string(*request.Params.ItemType)
	}
	var cursor *uuid.UUID
	if request.Params.Cursor != nil {
		value := uuid.UUID(*request.Params.Cursor)
		cursor = &value
	}
	filter := domain.EmbyLibraryItemFilter{ItemType: itemType, Present: request.Params.Present}
	if request.Params.Name != nil {
		filter.Name = *request.Params.Name
	}
	if request.Params.ProviderId != nil {
		filter.ProviderID = *request.Params.ProviderId
	}
	page, err := server.embyCatalog.ListLibraryItems(ctx, uuid.UUID(request.LibraryId), filter, cursor, limit)
	var serviceErr *service.Error
	switch {
	case errors.As(err, &serviceErr) && errors.Is(err, service.ErrInvalidInput):
		return ListEmbyLibraryItems400JSONResponse{BadRequestJSONResponse: BadRequestJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case errors.Is(err, domain.ErrNotFound):
		return ListEmbyLibraryItems404JSONResponse{NotFoundJSONResponse: catalogNotFoundError(ctx, "the Emby library was not found")}, nil
	case err != nil:
		return ListEmbyLibraryItems503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	default:
		response := EmbyLibraryItemPage{Items: make([]EmbyLibraryItem, 0, len(page.Items))}
		for _, item := range page.Items {
			mapped := EmbyLibraryItem{
				Id:            item.ID,
				EmbyId:        item.EmbyID,
				LibraryId:     item.LibraryID,
				ItemType:      EmbyLibraryItemItemType(item.ItemType),
				Name:          item.Name,
				ProviderIds:   item.ProviderIDs,
				Present:       item.Present,
				LastSeenAt:    item.LastSeenAt,
				SeasonNumber:  int32Pointer(item.SeasonNumber),
				EpisodeNumber: int32Pointer(item.EpisodeNumber),
			}
			if item.ParentEmbyID != "" {
				mapped.ParentEmbyId = &item.ParentEmbyID
			}
			if item.Path != "" {
				mapped.Path = &item.Path
			}
			mapped.ImportedTaskId = item.ImportedTaskID
			response.Items = append(response.Items, mapped)
		}
		if page.NextCursor != nil {
			value := *page.NextCursor
			response.NextCursor = &value
		}
		return ListEmbyLibraryItems200JSONResponse(response), nil
	}
}

func embyScanResponse(scan domain.EmbyScan) EmbyScan {
	response := EmbyScan{
		Id:           scan.ID,
		OperationId:  scan.OperationID,
		Status:       EmbyScanStatus(scan.Status),
		LibraryCount: int32(scan.LibraryCount),
		ItemCount:    int32(scan.ItemCount),
		StartedAt:    scan.StartedAt,
		CompletedAt:  scan.CompletedAt,
		CreatedAt:    scan.CreatedAt,
		UpdatedAt:    scan.UpdatedAt,
	}
	if scan.ErrorCode != "" {
		response.ErrorCode = &scan.ErrorCode
	}
	if scan.ErrorMessage != "" {
		response.ErrorMessage = &scan.ErrorMessage
	}
	return response
}

func int32Pointer(value *int) *int32 {
	if value == nil {
		return nil
	}
	result := int32(*value)
	return &result
}

func catalogNotFoundError(ctx context.Context, message string) NotFoundJSONResponse {
	return NotFoundJSONResponse(ApiError{
		Code:      "not_found",
		Message:   message,
		Details:   map[string]any{},
		RequestId: middleware.GetReqID(ctx),
	})
}
