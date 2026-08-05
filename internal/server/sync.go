package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/readest/readest-tiny-backend/internal/model"
	"gorm.io/gorm"
)

const (
	maxSyncRecords = 1000
	maxSyncText    = 16 * 1024
	maxNoteID      = 512
	maxNotePage    = 1<<31 - 1
	maxMetadata    = 1024 * 1024
	maxTags        = 100
	maxFutureSkew  = 24 * time.Hour
)

type optionalString struct {
	Set   bool
	Value *string
}

func (o *optionalString) UnmarshalJSON(data []byte) error {
	o.Set = true
	if bytes.Equal(data, []byte("null")) {
		o.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

type optionalTime struct {
	Set   bool
	Value *time.Time
}

func (o *optionalTime) UnmarshalJSON(data []byte) error {
	o.Set = true
	if bytes.Equal(data, []byte("null")) {
		o.Value = nil
		return nil
	}
	parsed, err := parseWireTime(data)
	if err != nil {
		return err
	}
	o.Value = parsed
	return nil
}

type optionalJSON struct {
	Set   bool
	Value json.RawMessage
}

func (o *optionalJSON) UnmarshalJSON(data []byte) error {
	o.Set = true
	o.Value = append(o.Value[:0], data...)
	return nil
}

type bookWire struct {
	Hash                     string         `json:"hash"`
	BookHash                 string         `json:"bookHash"`
	DBBookHash               string         `json:"book_hash"`
	MetaHash                 optionalString `json:"metaHash"`
	DBMetaHash               optionalString `json:"meta_hash"`
	Format                   optionalString `json:"format"`
	Title                    optionalString `json:"title"`
	SourceTitle              optionalString `json:"sourceTitle"`
	DBSourceTitle            optionalString `json:"source_title"`
	Author                   optionalString `json:"author"`
	Group                    optionalString `json:"group"`
	Tags                     optionalJSON   `json:"tags"`
	Progress                 optionalJSON   `json:"progress"`
	ReadingStatus            optionalString `json:"readingStatus"`
	DBReadingStatus          optionalString `json:"reading_status"`
	ReadingStatusUpdatedAt   optionalTime   `json:"readingStatusUpdatedAt"`
	DBReadingStatusUpdatedAt optionalTime   `json:"reading_status_updated_at"`
	CoverHash                optionalString `json:"coverHash"`
	DBCoverHash              optionalString `json:"cover_hash"`
	CoverUpdatedAt           optionalTime   `json:"coverUpdatedAt"`
	DBCoverUpdatedAt         optionalTime   `json:"cover_updated_at"`
	MetadataUpdatedAt        optionalTime   `json:"metadataUpdatedAt"`
	DBMetadataUpdatedAt      optionalTime   `json:"metadata_updated_at"`
	GroupID                  optionalString `json:"groupId"`
	DBGroupID                optionalString `json:"group_id"`
	GroupName                optionalString `json:"groupName"`
	DBGroupName              optionalString `json:"group_name"`
	Metadata                 optionalJSON   `json:"metadata"`
	CreatedAt                optionalTime   `json:"createdAt"`
	DBCreatedAt              optionalTime   `json:"created_at"`
	UpdatedAt                optionalTime   `json:"updatedAt"`
	DBUpdatedAt              optionalTime   `json:"updated_at"`
	DeletedAt                optionalTime   `json:"deletedAt"`
	DBDeletedAt              optionalTime   `json:"deleted_at"`
	UploadedAt               optionalTime   `json:"uploadedAt"`
	DBUploadedAt             optionalTime   `json:"uploaded_at"`
}

type configWire struct {
	BookHash       string         `json:"bookHash"`
	DBBookHash     string         `json:"book_hash"`
	MetaHash       optionalString `json:"metaHash"`
	DBMetaHash     optionalString `json:"meta_hash"`
	Location       optionalString `json:"location"`
	XPointer       optionalString `json:"xpointer"`
	Progress       optionalJSON   `json:"progress"`
	RSVPPosition   optionalString `json:"rsvpPosition"`
	DBRSVPPosition optionalString `json:"rsvp_position"`
	SearchConfig   optionalJSON   `json:"searchConfig"`
	DBSearchConfig optionalJSON   `json:"search_config"`
	ViewSettings   optionalJSON   `json:"viewSettings"`
	DBViewSettings optionalJSON   `json:"view_settings"`
	CreatedAt      optionalTime   `json:"createdAt"`
	DBCreatedAt    optionalTime   `json:"created_at"`
	UpdatedAt      optionalTime   `json:"updatedAt"`
	DBUpdatedAt    optionalTime   `json:"updated_at"`
	DeletedAt      optionalTime   `json:"deletedAt"`
	DBDeletedAt    optionalTime   `json:"deleted_at"`
}

type noteWire struct {
	BookHash    string         `json:"bookHash"`
	DBBookHash  string         `json:"book_hash"`
	MetaHash    optionalString `json:"metaHash"`
	DBMetaHash  optionalString `json:"meta_hash"`
	ID          string         `json:"id"`
	Type        optionalString `json:"type"`
	CFI         optionalString `json:"cfi"`
	XPointer0   optionalString `json:"xpointer0"`
	XPointer1   optionalString `json:"xpointer1"`
	Text        optionalString `json:"text"`
	Style       optionalString `json:"style"`
	Color       optionalString `json:"color"`
	Note        optionalString `json:"note"`
	Page        *int64         `json:"page"`
	Global      *bool          `json:"global"`
	CreatedAt   optionalTime   `json:"createdAt"`
	DBCreatedAt optionalTime   `json:"created_at"`
	UpdatedAt   optionalTime   `json:"updatedAt"`
	DBUpdatedAt optionalTime   `json:"updated_at"`
	DeletedAt   optionalTime   `json:"deletedAt"`
	DBDeletedAt optionalTime   `json:"deleted_at"`
}

type syncPayload struct {
	Books     []json.RawMessage `json:"books"`
	Configs   []json.RawMessage `json:"configs"`
	Notes     []json.RawMessage `json:"notes"`
	StatBooks []json.RawMessage `json:"statBooks"`
	StatPages []json.RawMessage `json:"statPages"`
}

func (s *Server) pullSync(c *gin.Context) {
	user := authenticatedUser(c)
	sinceRaw := c.Query("since")
	if sinceRaw == "" {
		jsonError(c, http.StatusBadRequest, `"since" query parameter is required`)
		return
	}
	sinceFloat, err := strconv.ParseFloat(sinceRaw, 64)
	if err != nil || math.IsNaN(sinceFloat) || math.IsInf(sinceFloat, 0) || sinceFloat < 0 || sinceFloat > float64(time.Now().UTC().Add(maxFutureSkew).UnixMilli()) {
		jsonError(c, http.StatusBadRequest, `Invalid "since" timestamp`)
		return
	}
	since := time.UnixMilli(int64(sinceFloat)).UTC()
	syncType := c.Query("type")
	if syncType != "" && syncType != "books" && syncType != "configs" && syncType != "notes" {
		jsonError(c, http.StatusBadRequest, "Unsupported sync type")
		return
	}
	bookFilter := c.Query("book")
	metaFilter := c.Query("meta_hash")
	if len(bookFilter) > 128 || len(metaFilter) > 128 {
		jsonError(c, http.StatusBadRequest, "Invalid sync filter")
		return
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 {
			jsonError(c, http.StatusBadRequest, "Invalid limit")
			return
		}
		limit = min(parsed, maxSyncRecords)
	}

	response := gin.H{"books": []any{}, "configs": []any{}, "notes": []any{}}
	if syncType == "" || syncType == "books" {
		books, queryErr := s.queryBooks(user.ID, since, bookFilter, metaFilter, limit, syncType == "books")
		if queryErr != nil {
			logInternal("pull books", queryErr)
			jsonError(c, http.StatusInternalServerError, "Unable to pull books")
			return
		}
		if len(books) == 0 && since.UnixMilli() < 1000 {
			now := time.Now().UTC()
			books = append(books, gin.H{
				"user_id": user.ID, "book_hash": strings.Repeat("0", 32), "hash": strings.Repeat("0", 32),
				"title": "Dummy Book", "format": "EPUB", "author": "",
				"created_at": formatTime(now), "updated_at": formatTime(now), "deleted_at": formatTime(now), "synced_at": formatTime(now),
			})
		}
		response["books"] = books
	}
	if syncType == "" || syncType == "configs" {
		configs, queryErr := s.queryConfigs(user.ID, since, bookFilter, metaFilter)
		if queryErr != nil {
			logInternal("pull configs", queryErr)
			jsonError(c, http.StatusInternalServerError, "Unable to pull configs")
			return
		}
		response["configs"] = configs
	}
	if syncType == "" || syncType == "notes" {
		notes, queryErr := s.queryNotes(user.ID, since, bookFilter, metaFilter)
		if queryErr != nil {
			logInternal("pull notes", queryErr)
			jsonError(c, http.StatusInternalServerError, "Unable to pull notes")
			return
		}
		response["notes"] = notes
	}
	c.JSON(http.StatusOK, response)
}

func (s *Server) pushSync(c *gin.Context) {
	user := authenticatedUser(c)
	var payload syncPayload
	if err := bindJSON(c, s.cfg.MaxJSONBodyBytes, &payload); err != nil {
		jsonError(c, http.StatusBadRequest, "Invalid sync data")
		return
	}
	if len(payload.Books) > maxSyncRecords || len(payload.Configs) > maxSyncRecords || len(payload.Notes) > maxSyncRecords || len(payload.StatBooks) > 0 || len(payload.StatPages) > 0 {
		jsonError(c, http.StatusBadRequest, "Unsupported or excessive sync data")
		return
	}
	bookRows := make([]model.Book, 0, len(payload.Books))
	for _, raw := range payload.Books {
		row, err := decodeBook(raw, user.ID)
		if err != nil {
			jsonError(c, http.StatusBadRequest, err.Error())
			return
		}
		bookRows = append(bookRows, row)
	}
	configRows := make([]model.BookConfig, 0, len(payload.Configs))
	for _, raw := range payload.Configs {
		row, err := decodeConfig(raw, user.ID)
		if err != nil {
			jsonError(c, http.StatusBadRequest, err.Error())
			return
		}
		configRows = append(configRows, row)
	}
	noteRows := make([]model.BookNote, 0, len(payload.Notes))
	for _, raw := range payload.Notes {
		row, err := decodeNote(raw, user.ID)
		if err != nil {
			jsonError(c, http.StatusBadRequest, err.Error())
			return
		}
		noteRows = append(noteRows, row)
	}

	var authoritativeBooks []model.Book
	var authoritativeConfigs []model.BookConfig
	var authoritativeNotes []model.BookNote
	err := s.db.Transaction(func(tx *gorm.DB) error {
		batchTime := time.Now().UTC()
		var err error
		authoritativeBooks, err = mergeBooks(tx, bookRows, batchTime)
		if err != nil {
			return err
		}
		authoritativeConfigs, err = mergeConfigs(tx, configRows, batchTime)
		if err != nil {
			return err
		}
		authoritativeNotes, err = mergeNotes(tx, noteRows, batchTime)
		if err != nil {
			return err
		}
		return piggybackProgress(tx, user.ID, authoritativeConfigs, batchTime)
	})
	if err != nil {
		logInternal("push sync transaction", err)
		jsonError(c, http.StatusInternalServerError, "Unable to save sync data")
		return
	}
	books := make([]any, 0, len(authoritativeBooks))
	for i := range authoritativeBooks {
		books = append(books, bookResponse(&authoritativeBooks[i]))
	}
	configs := make([]any, 0, len(authoritativeConfigs))
	for i := range authoritativeConfigs {
		configs = append(configs, configResponse(&authoritativeConfigs[i]))
	}
	notes := make([]any, 0, len(authoritativeNotes))
	for i := range authoritativeNotes {
		notes = append(notes, noteResponse(&authoritativeNotes[i]))
	}
	c.JSON(http.StatusOK, gin.H{"books": books, "configs": configs, "notes": notes})
}

func (s *Server) queryBooks(userID string, since time.Time, bookFilter, metaFilter string, limit int, paged bool) ([]any, error) {
	query := s.db.Where("user_id = ? AND synced_at > ?", userID, since)
	query = applyBookFilters(query, bookFilter, metaFilter)
	if limit > 0 && paged {
		var page []model.Book
		if err := query.Order("synced_at ASC").Limit(limit).Find(&page).Error; err != nil {
			return nil, err
		}
		if len(page) == limit {
			last := page[len(page)-1].SyncedAt
			var ties []model.Book
			tieQuery := s.db.Where("user_id = ? AND synced_at = ?", userID, last)
			tieQuery = applyBookFilters(tieQuery, bookFilter, metaFilter)
			if err := tieQuery.Find(&ties).Error; err != nil {
				return nil, err
			}
			seen := make(map[string]struct{}, len(page))
			for _, row := range page {
				seen[row.BookHash] = struct{}{}
			}
			for _, row := range ties {
				if _, ok := seen[row.BookHash]; !ok {
					page = append(page, row)
				}
			}
		}
		return booksResponse(page), nil
	}
	var rows []model.Book
	if err := query.Order("synced_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return booksResponse(rows), nil
}

func (s *Server) queryConfigs(userID string, since time.Time, bookFilter, metaFilter string) ([]any, error) {
	query := s.db.Where("user_id = ? AND (updated_at > ? OR deleted_at > ?)", userID, since, since)
	query = applyBookFilters(query, bookFilter, metaFilter)
	var rows []model.BookConfig
	if err := query.Order("updated_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]any, 0, len(rows))
	for i := range rows {
		result = append(result, configResponse(&rows[i]))
	}
	return result, nil
}

func (s *Server) queryNotes(userID string, since time.Time, bookFilter, metaFilter string) ([]any, error) {
	query := s.db.Where("user_id = ? AND (updated_at > ? OR deleted_at > ?)", userID, since, since)
	query = applyBookFilters(query, bookFilter, metaFilter)
	var rows []model.BookNote
	if err := query.Order("updated_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]any, 0, len(rows))
	for i := range rows {
		result = append(result, noteResponse(&rows[i]))
	}
	return result, nil
}

func applyBookFilters(query *gorm.DB, bookFilter, metaFilter string) *gorm.DB {
	if bookFilter != "" && metaFilter != "" {
		return query.Where("book_hash = ? OR meta_hash = ?", bookFilter, metaFilter)
	}
	if bookFilter != "" {
		return query.Where("book_hash = ?", bookFilter)
	}
	if metaFilter != "" {
		return query.Where("meta_hash = ?", metaFilter)
	}
	return query
}

func mergeBooks(tx *gorm.DB, incoming []model.Book, batchTime time.Time) ([]model.Book, error) {
	result := make([]model.Book, 0, len(incoming))
	for _, client := range incoming {
		var server model.Book
		err := tx.Where("user_id = ? AND book_hash = ?", client.UserID, client.BookHash).First(&server).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			client.SyncedAt = batchTime
			client.UpdatedAt = timePointer(batchTime)
			if client.CreatedAt == nil {
				client.CreatedAt = timePointer(batchTime)
			}
			if err := tx.Omit("User").Create(&client).Error; err != nil {
				return nil, err
			}
			result = append(result, client)
			continue
		}
		if err != nil {
			return nil, err
		}
		preserveMissingBookFields(&client, &server)
		clientNewer := millis(client.DeletedAt) > millis(server.DeletedAt) || millis(client.UpdatedAt) > millis(server.UpdatedAt)
		merged := server
		changed := false
		if clientNewer {
			merged = client
			changed = true
		}
		if millis(client.CoverUpdatedAt) >= millis(server.CoverUpdatedAt) {
			if !stringPointersEqual(merged.CoverHash, client.CoverHash) || millis(merged.CoverUpdatedAt) != millis(client.CoverUpdatedAt) {
				changed = true
			}
			merged.CoverHash = client.CoverHash
			merged.CoverUpdatedAt = client.CoverUpdatedAt
		} else {
			merged.CoverHash = server.CoverHash
			merged.CoverUpdatedAt = server.CoverUpdatedAt
		}
		if client.Presence["reading_status"] || client.Presence["reading_status_updated_at"] {
			if millis(client.ReadingStatusUpdatedAt) >= millis(server.ReadingStatusUpdatedAt) {
				if !stringPointersEqual(merged.ReadingStatus, client.ReadingStatus) || millis(merged.ReadingStatusUpdatedAt) != millis(client.ReadingStatusUpdatedAt) {
					changed = true
				}
				merged.ReadingStatus = client.ReadingStatus
				merged.ReadingStatusUpdatedAt = client.ReadingStatusUpdatedAt
			} else {
				merged.ReadingStatus = server.ReadingStatus
				merged.ReadingStatusUpdatedAt = server.ReadingStatusUpdatedAt
			}
		} else {
			merged.ReadingStatus = server.ReadingStatus
			merged.ReadingStatusUpdatedAt = server.ReadingStatusUpdatedAt
		}
		if millis(client.MetadataUpdatedAt) > 0 || millis(server.MetadataUpdatedAt) > 0 {
			if millis(client.MetadataUpdatedAt) >= millis(server.MetadataUpdatedAt) {
				if !metadataFieldsEqual(&merged, &client) {
					changed = true
				}
				copyMetadataFields(&merged, &client)
			} else {
				copyMetadataFields(&merged, &server)
			}
		}
		if changed {
			merged.UserID = server.UserID
			merged.BookHash = server.BookHash
			merged.SyncedAt = batchTime
			if err := tx.Omit("User").Save(&merged).Error; err != nil {
				return nil, err
			}
		}
		result = append(result, merged)
	}
	return result, nil
}

func mergeConfigs(tx *gorm.DB, incoming []model.BookConfig, batchTime time.Time) ([]model.BookConfig, error) {
	result := make([]model.BookConfig, 0, len(incoming))
	for _, client := range incoming {
		var server model.BookConfig
		err := tx.Where("user_id = ? AND book_hash = ?", client.UserID, client.BookHash).First(&server).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			client.UpdatedAt = timePointer(batchTime)
			if client.CreatedAt == nil {
				client.CreatedAt = timePointer(batchTime)
			}
			if err := tx.Omit("User").Create(&client).Error; err != nil {
				return nil, err
			}
			result = append(result, client)
			continue
		}
		if err != nil {
			return nil, err
		}
		clientNewer := millis(client.DeletedAt) > millis(server.DeletedAt) || millis(client.UpdatedAt) > millis(server.UpdatedAt)
		if clientNewer {
			preserveMissingConfigFields(&client, &server)
			client.UserID = server.UserID
			client.BookHash = server.BookHash
			if err := tx.Omit("User").Save(&client).Error; err != nil {
				return nil, err
			}
			result = append(result, client)
		} else {
			result = append(result, server)
		}
	}
	return result, nil
}

func mergeNotes(tx *gorm.DB, incoming []model.BookNote, batchTime time.Time) ([]model.BookNote, error) {
	result := make([]model.BookNote, 0, len(incoming))
	for _, client := range incoming {
		var server model.BookNote
		err := tx.Where("user_id = ? AND book_hash = ? AND id = ?", client.UserID, client.BookHash, client.ID).First(&server).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			client.UpdatedAt = batchTime
			if client.CreatedAt == nil {
				client.CreatedAt = timePointer(batchTime)
			}
			if err := tx.Create(&client).Error; err != nil {
				return nil, err
			}
			result = append(result, client)
			continue
		}
		if err != nil {
			return nil, err
		}
		if noteVersionMillis(&client) > noteVersionMillis(&server) {
			client.UserID = server.UserID
			client.BookHash = server.BookHash
			client.ID = server.ID
			client.Payload = server.Payload
			if client.Payload == "" {
				client.Payload = "{}"
			}
			if client.CreatedAt == nil {
				client.CreatedAt = server.CreatedAt
			}
			if client.UpdatedAt.IsZero() {
				client.UpdatedAt = server.UpdatedAt
			}
			if err := tx.Save(&client).Error; err != nil {
				return nil, err
			}
			result = append(result, client)
		} else {
			result = append(result, server)
		}
	}
	return result, nil
}

func piggybackProgress(tx *gorm.DB, userID string, configs []model.BookConfig, batchTime time.Time) error {
	for _, config := range configs {
		if config.Progress == nil || config.UpdatedAt == nil {
			continue
		}
		var progress []float64
		if err := json.Unmarshal([]byte(*config.Progress), &progress); err != nil || len(progress) != 2 {
			continue
		}
		encoded := *config.Progress
		result := tx.Model(&model.Book{}).
			Where("user_id = ? AND book_hash = ? AND (updated_at IS NULL OR updated_at < ?)", userID, config.BookHash, config.UpdatedAt).
			Updates(map[string]any{"progress": encoded, "updated_at": config.UpdatedAt, "synced_at": batchTime})
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

func decodeBook(raw json.RawMessage, userID string) (model.Book, error) {
	var wire bookWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return model.Book{}, errors.New("Invalid book record")
	}
	hash := firstNonEmpty(wire.Hash, wire.BookHash, wire.DBBookHash)
	if !isBookHash(hash) {
		return model.Book{}, errors.New("Invalid book hash")
	}
	metaHash := chooseString(wire.MetaHash, wire.DBMetaHash)
	sourceTitle := chooseString(wire.SourceTitle, wire.DBSourceTitle)
	readingStatus := chooseString(wire.ReadingStatus, wire.DBReadingStatus)
	readingUpdated := chooseTime(wire.ReadingStatusUpdatedAt, wire.DBReadingStatusUpdatedAt)
	coverHash := chooseString(wire.CoverHash, wire.DBCoverHash)
	coverUpdated := chooseTime(wire.CoverUpdatedAt, wire.DBCoverUpdatedAt)
	metadataUpdated := chooseTime(wire.MetadataUpdatedAt, wire.DBMetadataUpdatedAt)
	groupID := chooseString(wire.GroupID, wire.DBGroupID)
	groupName := chooseString(wire.GroupName, wire.DBGroupName)
	created := chooseTime(wire.CreatedAt, wire.DBCreatedAt)
	updated := chooseTime(wire.UpdatedAt, wire.DBUpdatedAt)
	deleted := chooseTime(wire.DeletedAt, wire.DBDeletedAt)
	uploaded := chooseTime(wire.UploadedAt, wire.DBUploadedAt)
	for _, value := range []*string{metaHash, wire.Format.Value, wire.Title.Value, sourceTitle, wire.Author.Value, wire.Group.Value, readingStatus, coverHash, groupID, groupName} {
		if err := validateSyncString(value); err != nil {
			return model.Book{}, err
		}
	}
	for _, value := range []*time.Time{created, updated, deleted, uploaded, readingUpdated, coverUpdated, metadataUpdated} {
		if err := validateClientTime(value); err != nil {
			return model.Book{}, err
		}
	}
	tags, err := encodeTags(wire.Tags)
	if err != nil {
		return model.Book{}, err
	}
	progress, err := encodeProgress(wire.Progress)
	if err != nil {
		return model.Book{}, err
	}
	metadata, err := encodeBookMetadata(wire.Metadata)
	if err != nil {
		return model.Book{}, err
	}
	return model.Book{
		UserID: userID, BookHash: hash, MetaHash: cleanPointer(metaHash), Format: cleanPointer(wire.Format.Value),
		Title: cleanPointer(wire.Title.Value), SourceTitle: cleanPointer(sourceTitle), Author: cleanPointer(wire.Author.Value),
		Group: cleanPointer(wire.Group.Value), Tags: tags, Progress: progress,
		ReadingStatus: cleanPointer(readingStatus), ReadingStatusUpdatedAt: readingUpdated,
		CoverHash: cleanPointer(coverHash), CoverUpdatedAt: coverUpdated, MetadataUpdatedAt: metadataUpdated,
		GroupID: cleanPointer(groupID), GroupName: cleanPointer(groupName), Metadata: metadata,
		CreatedAt: created, UpdatedAt: updated, DeletedAt: deleted, UploadedAt: uploaded,
		Presence: map[string]bool{
			"meta_hash": wire.MetaHash.Set || wire.DBMetaHash.Set,
			"format":    wire.Format.Set, "title": wire.Title.Set,
			"source_title": wire.SourceTitle.Set || wire.DBSourceTitle.Set,
			"author":       wire.Author.Set, "group": wire.Group.Set, "tags": wire.Tags.Set,
			"progress":                  wire.Progress.Set,
			"reading_status":            wire.ReadingStatus.Set || wire.DBReadingStatus.Set,
			"reading_status_updated_at": wire.ReadingStatusUpdatedAt.Set || wire.DBReadingStatusUpdatedAt.Set,
			"group_id":                  wire.GroupID.Set || wire.DBGroupID.Set,
			"group_name":                wire.GroupName.Set || wire.DBGroupName.Set,
		},
	}, nil
}

func decodeConfig(raw json.RawMessage, userID string) (model.BookConfig, error) {
	var wire configWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return model.BookConfig{}, errors.New("Invalid config record")
	}
	hash := firstNonEmpty(wire.BookHash, wire.DBBookHash)
	if !isBookHash(hash) {
		return model.BookConfig{}, errors.New("Invalid book hash")
	}
	metaHash := chooseString(wire.MetaHash, wire.DBMetaHash)
	rsvp := chooseString(wire.RSVPPosition, wire.DBRSVPPosition)
	created := chooseTime(wire.CreatedAt, wire.DBCreatedAt)
	updated := chooseTime(wire.UpdatedAt, wire.DBUpdatedAt)
	deleted := chooseTime(wire.DeletedAt, wire.DBDeletedAt)
	for _, value := range []*string{metaHash, wire.Location.Value, wire.XPointer.Value, rsvp} {
		if err := validateSyncString(value); err != nil {
			return model.BookConfig{}, err
		}
	}
	for _, value := range []*time.Time{created, updated, deleted} {
		if err := validateClientTime(value); err != nil {
			return model.BookConfig{}, err
		}
	}
	progress, err := encodeProgress(wire.Progress)
	if err != nil {
		return model.BookConfig{}, err
	}
	searchConfig, err := encodeArbitraryJSON(chooseJSON(wire.SearchConfig, wire.DBSearchConfig), 256*1024)
	if err != nil {
		return model.BookConfig{}, errors.New("Invalid search config")
	}
	viewSettings, err := encodeArbitraryJSON(chooseJSON(wire.ViewSettings, wire.DBViewSettings), 256*1024)
	if err != nil {
		return model.BookConfig{}, errors.New("Invalid view settings")
	}
	return model.BookConfig{
		UserID: userID, BookHash: hash, MetaHash: cleanPointer(metaHash), Location: cleanPointer(wire.Location.Value),
		XPointer: cleanPointer(wire.XPointer.Value), Progress: progress, RSVPPosition: cleanPointer(rsvp),
		SearchConfig: searchConfig, ViewSettings: viewSettings, CreatedAt: created, UpdatedAt: updated, DeletedAt: deleted,
		Presence: map[string]bool{
			"meta_hash": wire.MetaHash.Set || wire.DBMetaHash.Set,
			"location":  wire.Location.Set, "xpointer": wire.XPointer.Set, "progress": wire.Progress.Set,
			"rsvp_position": wire.RSVPPosition.Set || wire.DBRSVPPosition.Set,
			"search_config": wire.SearchConfig.Set || wire.DBSearchConfig.Set,
			"view_settings": wire.ViewSettings.Set || wire.DBViewSettings.Set,
			"created_at":    wire.CreatedAt.Set || wire.DBCreatedAt.Set,
			"deleted_at":    wire.DeletedAt.Set || wire.DBDeletedAt.Set,
		},
	}, nil
}

func decodeNote(raw json.RawMessage, userID string) (model.BookNote, error) {
	var wire noteWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return model.BookNote{}, errors.New("Invalid note record")
	}
	hash := firstNonEmpty(wire.BookHash, wire.DBBookHash)
	if !isBookHash(hash) {
		return model.BookNote{}, errors.New("Invalid book hash")
	}
	id := wire.ID
	if id == "" || len(id) > maxNoteID || strings.ContainsRune(id, '\x00') {
		return model.BookNote{}, errors.New("Invalid note ID")
	}
	noteType := chooseString(wire.Type)
	if noteType == nil || (*noteType != "bookmark" && *noteType != "annotation") {
		return model.BookNote{}, errors.New("Invalid note type")
	}
	metaHash := chooseString(wire.MetaHash, wire.DBMetaHash)
	created := chooseTime(wire.CreatedAt, wire.DBCreatedAt)
	updated := chooseTime(wire.UpdatedAt, wire.DBUpdatedAt)
	deleted := chooseTime(wire.DeletedAt, wire.DBDeletedAt)
	for _, value := range []*string{metaHash, wire.CFI.Value, wire.XPointer0.Value, wire.XPointer1.Value, wire.Text.Value, wire.Style.Value, wire.Color.Value, wire.Note.Value} {
		if err := validateSyncString(value); err != nil {
			return model.BookNote{}, err
		}
	}
	for _, value := range []*time.Time{created, updated, deleted} {
		if err := validateClientTime(value); err != nil {
			return model.BookNote{}, err
		}
	}
	if wire.Page != nil && (*wire.Page < 0 || *wire.Page > maxNotePage) {
		return model.BookNote{}, errors.New("Invalid note page")
	}
	var updatedAt time.Time
	if updated != nil {
		updatedAt = *updated
	}
	return model.BookNote{
		UserID: userID, BookHash: hash, ID: id, MetaHash: cleanPointer(metaHash), Type: *noteType,
		CFI: cleanPointer(wire.CFI.Value), XPointer0: cleanPointer(wire.XPointer0.Value), XPointer1: cleanPointer(wire.XPointer1.Value),
		Text: cleanPointer(wire.Text.Value), Style: cleanPointer(wire.Style.Value), Color: cleanPointer(wire.Color.Value), Note: cleanPointer(wire.Note.Value),
		Page: wire.Page, Global: wire.Global, CreatedAt: created, UpdatedAt: updatedAt, DeletedAt: deleted, Payload: "{}",
	}, nil
}

func parseWireTime(raw []byte) (*time.Time, error) {
	var millisValue float64
	if err := json.Unmarshal(raw, &millisValue); err == nil {
		if math.IsNaN(millisValue) || math.IsInf(millisValue, 0) || millisValue < 0 || millisValue > math.MaxInt64 {
			return nil, errors.New("invalid timestamp")
		}
		parsed := time.UnixMilli(int64(millisValue)).UTC()
		return &parsed, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, errors.New("invalid timestamp")
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return nil, errors.New("invalid timestamp")
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func validateClientTime(value *time.Time) error {
	if value != nil && value.After(time.Now().UTC().Add(maxFutureSkew)) {
		return errors.New("Timestamp is too far in the future")
	}
	return nil
}

func validateSyncString(value *string) error {
	if value != nil && len(*value) > maxSyncText {
		return errors.New("Sync field is too large")
	}
	return nil
}

func encodeTags(value optionalJSON) (*string, error) {
	if !value.Set || bytes.Equal(value.Value, []byte("null")) {
		return nil, nil
	}
	var tags []string
	if err := json.Unmarshal(value.Value, &tags); err != nil || len(tags) > maxTags {
		return nil, errors.New("Invalid book tags")
	}
	for i := range tags {
		if len(tags[i]) > 512 {
			return nil, errors.New("Invalid book tags")
		}
		tags[i] = strings.ReplaceAll(tags[i], "\x00", "")
	}
	encoded, _ := json.Marshal(tags)
	text := string(encoded)
	return &text, nil
}

func encodeProgress(value optionalJSON) (*string, error) {
	if !value.Set || bytes.Equal(value.Value, []byte("null")) {
		return nil, nil
	}
	var progress []float64
	if err := json.Unmarshal(value.Value, &progress); err != nil || len(progress) != 2 {
		return nil, errors.New("Invalid book progress")
	}
	for _, part := range progress {
		if math.IsNaN(part) || math.IsInf(part, 0) || part < 0 {
			return nil, errors.New("Invalid book progress")
		}
	}
	encoded, _ := json.Marshal(progress)
	text := string(encoded)
	return &text, nil
}

func encodeBookMetadata(value optionalJSON) (*string, error) {
	if !value.Set || bytes.Equal(value.Value, []byte("null")) {
		return nil, nil
	}
	if len(value.Value) > maxMetadata || !json.Valid(value.Value) {
		return nil, errors.New("Invalid book metadata")
	}
	if value.Value[0] == '"' {
		var text string
		if err := json.Unmarshal(value.Value, &text); err != nil || len(text) > maxMetadata || !json.Valid([]byte(text)) {
			return nil, errors.New("Invalid book metadata")
		}
		text = strings.ReplaceAll(text, "\x00", "")
		return &text, nil
	}
	compact := bytes.Buffer{}
	if err := json.Compact(&compact, value.Value); err != nil {
		return nil, errors.New("Invalid book metadata")
	}
	text := strings.ReplaceAll(compact.String(), "\x00", "")
	return &text, nil
}

func encodeArbitraryJSON(value optionalJSON, limit int) (*string, error) {
	if !value.Set || bytes.Equal(value.Value, []byte("null")) {
		return nil, nil
	}
	if len(value.Value) > limit || !json.Valid(value.Value) {
		return nil, errors.New("invalid JSON")
	}
	compact := bytes.Buffer{}
	_ = json.Compact(&compact, value.Value)
	text := compact.String()
	return &text, nil
}

func booksResponse(rows []model.Book) []any {
	result := make([]any, 0, len(rows))
	for i := range rows {
		result = append(result, bookResponse(&rows[i]))
	}
	return result
}

func bookResponse(book *model.Book) gin.H {
	return gin.H{
		"user_id": book.UserID, "book_hash": book.BookHash, "meta_hash": valueOrNil(book.MetaHash),
		"format": valueOrNil(book.Format), "title": valueOrNil(book.Title), "source_title": valueOrNil(book.SourceTitle),
		"author": valueOrNil(book.Author), "group": valueOrNil(book.Group), "tags": decodedOrNil(book.Tags),
		"progress": decodedOrNil(book.Progress), "reading_status": valueOrNil(book.ReadingStatus),
		"reading_status_updated_at": nullableTime(book.ReadingStatusUpdatedAt), "cover_hash": valueOrNil(book.CoverHash),
		"cover_updated_at": nullableTime(book.CoverUpdatedAt), "metadata_updated_at": nullableTime(book.MetadataUpdatedAt),
		"group_id": valueOrNil(book.GroupID), "group_name": valueOrNil(book.GroupName), "metadata": valueOrNil(book.Metadata),
		"created_at": nullableTime(book.CreatedAt), "updated_at": nullableTime(book.UpdatedAt), "deleted_at": nullableTime(book.DeletedAt),
		"uploaded_at": nullableTime(book.UploadedAt), "synced_at": formatTime(book.SyncedAt),
	}
}

func configResponse(config *model.BookConfig) gin.H {
	return gin.H{
		"user_id": config.UserID, "book_hash": config.BookHash, "meta_hash": valueOrNil(config.MetaHash),
		"location": valueOrNil(config.Location), "xpointer": valueOrNil(config.XPointer), "progress": valueOrNil(config.Progress),
		"rsvp_position": valueOrNil(config.RSVPPosition), "search_config": valueOrNil(config.SearchConfig),
		"view_settings": valueOrNil(config.ViewSettings), "created_at": nullableTime(config.CreatedAt),
		"updated_at": nullableTime(config.UpdatedAt), "deleted_at": nullableTime(config.DeletedAt),
	}
}

func noteResponse(note *model.BookNote) gin.H {
	return gin.H{
		"user_id": note.UserID, "book_hash": note.BookHash, "meta_hash": valueOrNil(note.MetaHash),
		"id": note.ID, "type": note.Type, "cfi": valueOrNil(note.CFI),
		"xpointer0": valueOrNil(note.XPointer0), "xpointer1": valueOrNil(note.XPointer1),
		"text": valueOrNil(note.Text), "style": valueOrNil(note.Style), "color": valueOrNil(note.Color),
		"note": valueOrNil(note.Note), "page": int64OrNil(note.Page), "global": boolOrNil(note.Global),
		"created_at": nullableTime(note.CreatedAt), "updated_at": formatTime(note.UpdatedAt), "deleted_at": nullableTime(note.DeletedAt),
	}
}

func decodedOrNil(value *string) any {
	if value == nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(*value), &decoded); err != nil {
		return nil
	}
	return decoded
}

func valueOrNil(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64OrNil(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func boolOrNil(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func chooseString(values ...optionalString) *string {
	for _, value := range values {
		if value.Set {
			return value.Value
		}
	}
	return nil
}

func chooseTime(values ...optionalTime) *time.Time {
	for _, value := range values {
		if value.Set {
			return value.Value
		}
	}
	return nil
}

func chooseJSON(values ...optionalJSON) optionalJSON {
	for _, value := range values {
		if value.Set {
			return value
		}
	}
	return optionalJSON{}
}

func cleanPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cleaned := strings.ReplaceAll(*value, "\x00", "")
	return &cleaned
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isBookHash(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func timePointer(value time.Time) *time.Time { return &value }

func millis(value *time.Time) int64 {
	if value == nil {
		return 0
	}
	return value.UnixMilli()
}

func noteVersionMillis(note *model.BookNote) int64 {
	updated := int64(0)
	if !note.UpdatedAt.IsZero() {
		updated = note.UpdatedAt.UnixMilli()
	}
	return max(updated, millis(note.DeletedAt))
}

func stringPointersEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func metadataFieldsEqual(left, right *model.Book) bool {
	return stringPointersEqual(left.Title, right.Title) && stringPointersEqual(left.SourceTitle, right.SourceTitle) &&
		stringPointersEqual(left.Author, right.Author) && stringPointersEqual(left.Tags, right.Tags) &&
		stringPointersEqual(left.Metadata, right.Metadata) && millis(left.MetadataUpdatedAt) == millis(right.MetadataUpdatedAt)
}

func copyMetadataFields(destination, source *model.Book) {
	destination.Title = source.Title
	destination.SourceTitle = source.SourceTitle
	destination.Author = source.Author
	destination.Tags = source.Tags
	destination.Metadata = source.Metadata
	destination.MetadataUpdatedAt = source.MetadataUpdatedAt
}

func preserveMissingBookFields(client, server *model.Book) {
	if !client.Presence["meta_hash"] {
		client.MetaHash = server.MetaHash
	}
	if !client.Presence["format"] {
		client.Format = server.Format
	}
	if !client.Presence["title"] {
		client.Title = server.Title
	}
	if !client.Presence["source_title"] {
		client.SourceTitle = server.SourceTitle
	}
	if !client.Presence["author"] {
		client.Author = server.Author
	}
	if !client.Presence["group"] {
		client.Group = server.Group
	}
	if !client.Presence["tags"] {
		client.Tags = server.Tags
	}
	if !client.Presence["progress"] {
		client.Progress = server.Progress
	}
	if !client.Presence["reading_status"] {
		client.ReadingStatus = server.ReadingStatus
	}
	if !client.Presence["reading_status_updated_at"] {
		client.ReadingStatusUpdatedAt = server.ReadingStatusUpdatedAt
	}
	if !client.Presence["group_id"] {
		client.GroupID = server.GroupID
	}
	if !client.Presence["group_name"] {
		client.GroupName = server.GroupName
	}
}

func preserveMissingConfigFields(client, server *model.BookConfig) {
	if !client.Presence["meta_hash"] {
		client.MetaHash = server.MetaHash
	}
	if !client.Presence["location"] {
		client.Location = server.Location
	}
	if !client.Presence["xpointer"] {
		client.XPointer = server.XPointer
	}
	if !client.Presence["progress"] {
		client.Progress = server.Progress
	}
	if !client.Presence["rsvp_position"] {
		client.RSVPPosition = server.RSVPPosition
	}
	if !client.Presence["search_config"] {
		client.SearchConfig = server.SearchConfig
	}
	if !client.Presence["view_settings"] {
		client.ViewSettings = server.ViewSettings
	}
	if !client.Presence["created_at"] {
		client.CreatedAt = server.CreatedAt
	}
	if !client.Presence["deleted_at"] {
		client.DeletedAt = server.DeletedAt
	}
}
