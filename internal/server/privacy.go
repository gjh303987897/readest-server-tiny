package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/readest/readest-tiny-backend/internal/model"
	"gorm.io/gorm"
)

const (
	maxPrivacyEnvelopeBytes = 1024 * 1024
	privacyKDFIterations    = 600000
	privacyMaxFutureSkew    = 5 * time.Minute
	maxSafeJSONInteger      = 1<<53 - 1
)

type privacySyncRequest struct {
	Envelope  json.RawMessage `json:"envelope"`
	UpdatedAt json.RawMessage `json:"updatedAt"`
}

type encryptedPrivacyEnvelope struct {
	Version    int                  `json:"version"`
	Algorithm  string               `json:"algorithm"`
	KDF        privacyEncryptionKDF `json:"kdf"`
	IV         string               `json:"iv"`
	Ciphertext string               `json:"ciphertext"`
}

type privacyEncryptionKDF struct {
	Algorithm  string `json:"algorithm"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
}

func (s *Server) pullPrivacySettings(c *gin.Context) {
	user := authenticatedUser(c)
	var setting model.PrivacySetting
	result := s.db.Where("user_id = ?", user.ID).First(&setting)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusOK, gin.H{"record": nil})
		return
	}
	if result.Error != nil {
		logInternal("read privacy settings", result.Error)
		jsonError(c, http.StatusInternalServerError, "Failed to sync privacy settings")
		return
	}
	c.JSON(http.StatusOK, gin.H{"record": privacySettingResponse(&setting)})
}

func (s *Server) pushPrivacySettings(c *gin.Context) {
	var request privacySyncRequest
	if err := bindJSON(c, s.cfg.MaxJSONBodyBytes, &request); err != nil {
		jsonError(c, http.StatusBadRequest, "Invalid privacy sync payload")
		return
	}
	envelope, err := validatePrivacyEnvelope(request.Envelope)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "Invalid privacy sync payload")
		return
	}
	updatedAtMillis, err := parsePrivacyUpdatedAt(request.UpdatedAt)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "Invalid privacy sync payload")
		return
	}

	user := authenticatedUser(c)
	setting := model.PrivacySetting{
		UserID: user.ID, Envelope: envelope, UpdatedAt: time.UnixMilli(updatedAtMillis).UTC(),
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`INSERT INTO privacy_settings (user_id, envelope, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(user_id) DO UPDATE SET
				envelope = excluded.envelope,
				updated_at = excluded.updated_at
			WHERE excluded.updated_at > privacy_settings.updated_at`,
			setting.UserID, setting.Envelope, setting.UpdatedAt)
		if result.Error != nil {
			return result.Error
		}
		return tx.Where("user_id = ?", user.ID).First(&setting).Error
	})
	if err != nil {
		logInternal("merge privacy settings", err)
		jsonError(c, http.StatusInternalServerError, "Failed to sync privacy settings")
		return
	}
	c.JSON(http.StatusOK, gin.H{"record": privacySettingResponse(&setting)})
}

func validatePrivacyEnvelope(raw json.RawMessage) (*string, error) {
	if len(raw) == 0 {
		return nil, errors.New("missing envelope")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var envelope encryptedPrivacyEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Version != 1 || envelope.Algorithm != "AES-GCM" ||
		envelope.KDF.Algorithm != "PBKDF2-SHA-256" || envelope.KDF.Iterations != privacyKDFIterations ||
		!validBase64Length(envelope.KDF.Salt, 16, 16) ||
		!validBase64Length(envelope.IV, 12, 12) ||
		!validBase64Length(envelope.Ciphertext, 16, math.MaxInt) {
		return nil, errors.New("invalid encrypted privacy envelope")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil || compact.Len() > maxPrivacyEnvelopeBytes {
		return nil, errors.New("privacy envelope is too large")
	}
	value := compact.String()
	return &value, nil
}

func validBase64Length(value string, minimum, maximum int) bool {
	if value == "" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(value)
		if err != nil {
			return false
		}
	}
	canonical := strings.TrimRight(base64.StdEncoding.EncodeToString(decoded), "=")
	return canonical == strings.TrimRight(value, "=") && len(decoded) >= minimum && len(decoded) <= maximum
}

func parsePrivacyUpdatedAt(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing updatedAt")
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) ||
		value <= 0 || value > maxSafeJSONInteger || math.Trunc(value) != value {
		return 0, errors.New("invalid updatedAt")
	}
	millis := int64(value)
	if time.UnixMilli(millis).After(time.Now().UTC().Add(privacyMaxFutureSkew)) {
		return 0, errors.New("updatedAt is too far in the future")
	}
	return millis, nil
}

func privacySettingResponse(setting *model.PrivacySetting) gin.H {
	var envelope any
	if setting.Envelope != nil {
		envelope = json.RawMessage(*setting.Envelope)
	}
	return gin.H{"envelope": envelope, "updatedAt": setting.UpdatedAt.UnixMilli()}
}
