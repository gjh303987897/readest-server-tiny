package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/readest/readest-tiny-backend/internal/model"
	"github.com/readest/readest-tiny-backend/internal/security"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	minimumPasswordBytes = 8
	maximumPasswordBytes = 72
	maximumEmailBytes    = 320
	maximumMetadataBytes = 32 * 1024
)

type authCredentials struct {
	Email        string         `json:"email"`
	Password     string         `json:"password"`
	RefreshToken string         `json:"refresh_token"`
	Data         map[string]any `json:"data"`
}

type userUpdate struct {
	Email    *string        `json:"email"`
	Password *string        `json:"password"`
	Data     map[string]any `json:"data"`
}

func (s *Server) signup(c *gin.Context) {
	var request authCredentials
	if err := bindJSON(c, s.cfg.MaxJSONBodyBytes, &request); err != nil {
		authError(c, http.StatusBadRequest, "validation_failed", "Invalid signup request")
		return
	}
	email, err := normalizeEmail(request.Email)
	if err != nil || validatePassword(request.Password) != nil {
		authError(c, http.StatusUnprocessableEntity, "weak_password", "Use a valid email and a password of at least 8 characters")
		return
	}
	metadata, err := encodeMetadata(request.Data)
	if err != nil {
		authError(c, http.StatusUnprocessableEntity, "validation_failed", "User metadata is too large")
		return
	}
	var existing int64
	if err := s.db.Model(&model.User{}).Where("email = ?", email).Count(&existing).Error; err != nil {
		logInternal("check signup email", err)
		authError(c, http.StatusInternalServerError, "unexpected_failure", "Unable to create account")
		return
	}
	if existing > 0 {
		authError(c, http.StatusUnprocessableEntity, "user_already_exists", "A user with this email address has already been registered")
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(request.Password), s.cfg.PasswordBCryptCost)
	if err != nil {
		logInternal("hash signup password", err)
		authError(c, http.StatusInternalServerError, "unexpected_failure", "Unable to create account")
		return
	}
	now := time.Now().UTC()
	user := model.User{
		ID: uuid.NewString(), Email: email, PasswordHash: string(passwordHash), UserMetadata: metadata,
	}
	if s.cfg.AutoConfirmEmail {
		user.EmailConfirmedAt = &now
	}
	if err := s.db.Create(&user).Error; err != nil {
		logInternal("create user", err)
		authError(c, http.StatusUnprocessableEntity, "user_already_exists", "Unable to create account")
		return
	}
	if !s.cfg.AutoConfirmEmail {
		redirect := s.safeRedirect(c.Query("redirect_to"))
		if err := s.createAndSendAuthLink(&user, "signup", redirect); err != nil {
			logInternal("send signup confirmation", err)
		}
		c.JSON(http.StatusOK, gin.H{"user": publicUser(&user)})
		return
	}
	session, err := s.createSession(&user)
	if err != nil {
		logInternal("create signup session", err)
		authError(c, http.StatusInternalServerError, "unexpected_failure", "Unable to create session")
		return
	}
	c.JSON(http.StatusOK, session)
}

func (s *Server) token(c *gin.Context) {
	grantType := c.Query("grant_type")
	var request authCredentials
	if err := bindJSON(c, s.cfg.MaxJSONBodyBytes, &request); err != nil {
		authError(c, http.StatusBadRequest, "validation_failed", "Invalid token request")
		return
	}
	switch grantType {
	case "password":
		s.passwordGrant(c, request)
	case "refresh_token":
		s.refreshGrant(c, request.RefreshToken)
	default:
		authError(c, http.StatusBadRequest, "unsupported_grant_type", "Unsupported grant type")
	}
}

func (s *Server) passwordGrant(c *gin.Context, request authCredentials) {
	email, err := normalizeEmail(request.Email)
	if err != nil || validatePassword(request.Password) != nil {
		authError(c, http.StatusBadRequest, "invalid_credentials", "Invalid login credentials")
		return
	}
	var user model.User
	if err := s.db.Where("email = ?", email).First(&user).Error; err != nil || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.Password)) != nil {
		// Keep the invalid-user path computationally similar to a wrong password.
		if err != nil {
			_, _ = bcrypt.GenerateFromPassword([]byte("readest-invalid-login-padding"), s.cfg.PasswordBCryptCost)
		}
		authError(c, http.StatusBadRequest, "invalid_credentials", "Invalid login credentials")
		return
	}
	if user.EmailConfirmedAt == nil {
		authError(c, http.StatusBadRequest, "email_not_confirmed", "Email not confirmed")
		return
	}
	now := time.Now().UTC()
	user.LastSignInAt = &now
	if err := s.db.Model(&user).Update("last_sign_in_at", now).Error; err != nil {
		logInternal("update last sign in", err)
	}
	session, err := s.createSession(&user)
	if err != nil {
		logInternal("create login session", err)
		authError(c, http.StatusInternalServerError, "unexpected_failure", "Unable to create session")
		return
	}
	c.JSON(http.StatusOK, session)
}

func (s *Server) refreshGrant(c *gin.Context, refreshToken string) {
	if len(refreshToken) < 32 || len(refreshToken) > 512 {
		authError(c, http.StatusBadRequest, "refresh_token_not_found", "Invalid refresh token")
		return
	}
	oldHash := security.HashToken(refreshToken)
	newToken, err := security.RandomToken(48)
	if err != nil {
		authError(c, http.StatusInternalServerError, "unexpected_failure", "Unable to refresh session")
		return
	}
	newHash := security.HashToken(newToken)
	var session model.Session
	var user model.User
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("refresh_hash = ? AND revoked_at IS NULL AND expires_at > ?", oldHash, time.Now().UTC()).First(&session).Error; err != nil {
			return err
		}
		result := tx.Model(&model.Session{}).
			Where("id = ? AND refresh_hash = ? AND revoked_at IS NULL", session.ID, oldHash).
			Updates(map[string]any{"refresh_hash": newHash, "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.First(&user, "id = ?", session.UserID).Error
	})
	if err != nil {
		authError(c, http.StatusBadRequest, "refresh_token_not_found", "Invalid or expired refresh token")
		return
	}
	response, err := s.sessionResponse(&user, &session, newToken)
	if err != nil {
		logInternal("issue refreshed access token", err)
		authError(c, http.StatusInternalServerError, "unexpected_failure", "Unable to refresh session")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (s *Server) getUser(c *gin.Context) {
	c.JSON(http.StatusOK, publicUser(authenticatedUser(c)))
}

func (s *Server) updateUser(c *gin.Context) {
	user := authenticatedUser(c)
	var request userUpdate
	if err := bindJSON(c, s.cfg.MaxJSONBodyBytes, &request); err != nil {
		authError(c, http.StatusBadRequest, "validation_failed", "Invalid user update")
		return
	}
	updates := make(map[string]any)
	if request.Password != nil {
		if err := validatePassword(*request.Password); err != nil {
			authError(c, http.StatusUnprocessableEntity, "weak_password", "Password must contain between 8 and 72 bytes")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*request.Password), s.cfg.PasswordBCryptCost)
		if err != nil {
			authError(c, http.StatusInternalServerError, "unexpected_failure", "Unable to update user")
			return
		}
		updates["password_hash"] = string(hash)
	}
	if request.Email != nil {
		email, err := normalizeEmail(*request.Email)
		if err != nil {
			authError(c, http.StatusUnprocessableEntity, "validation_failed", "Invalid email address")
			return
		}
		updates["email"] = email
		user.Email = email
	}
	if request.Data != nil {
		metadata, err := encodeMetadata(request.Data)
		if err != nil {
			authError(c, http.StatusUnprocessableEntity, "validation_failed", "User metadata is too large")
			return
		}
		updates["user_metadata"] = metadata
		user.UserMetadata = metadata
	}
	if len(updates) == 0 {
		authError(c, http.StatusBadRequest, "validation_failed", "No supported user attributes supplied")
		return
	}
	var currentClaims *security.AccessClaims
	if request.Password != nil {
		claims, claimsErr := s.accessClaims(c)
		if claimsErr != nil {
			authError(c, http.StatusUnauthorized, "bad_jwt", "Invalid access token")
			return
		}
		currentClaims = claims
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
			return err
		}
		if currentClaims != nil {
			now := time.Now().UTC()
			return tx.Model(&model.Session{}).
				Where("user_id = ? AND id <> ? AND revoked_at IS NULL", user.ID, currentClaims.SessionID).
				Update("revoked_at", now).Error
		}
		return nil
	}); err != nil {
		authError(c, http.StatusUnprocessableEntity, "user_already_exists", "Unable to update user")
		return
	}
	if err := s.db.First(user, "id = ?", user.ID).Error; err != nil {
		authError(c, http.StatusInternalServerError, "unexpected_failure", "Unable to load user")
		return
	}
	c.JSON(http.StatusOK, publicUser(user))
}

func (s *Server) logout(c *gin.Context) {
	claims, err := s.accessClaims(c)
	if err != nil {
		authError(c, http.StatusUnauthorized, "bad_jwt", "Invalid access token")
		return
	}
	now := time.Now().UTC()
	query := s.db.Model(&model.Session{})
	switch c.DefaultQuery("scope", "local") {
	case "global":
		query = query.Where("user_id = ? AND revoked_at IS NULL", claims.Subject)
	case "others":
		query = query.Where("user_id = ? AND id <> ? AND revoked_at IS NULL", claims.Subject, claims.SessionID)
	case "local":
		query = query.Where("id = ? AND user_id = ? AND revoked_at IS NULL", claims.SessionID, claims.Subject)
	default:
		authError(c, http.StatusBadRequest, "validation_failed", "Invalid logout scope")
		return
	}
	if err := query.Update("revoked_at", now).Error; err != nil {
		logInternal("revoke session", err)
		authError(c, http.StatusInternalServerError, "unexpected_failure", "Unable to revoke session")
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) recover(c *gin.Context) {
	var request struct {
		Email string `json:"email"`
	}
	if err := bindJSON(c, s.cfg.MaxJSONBodyBytes, &request); err != nil {
		authError(c, http.StatusBadRequest, "validation_failed", "Invalid recovery request")
		return
	}
	email, err := normalizeEmail(request.Email)
	if err == nil {
		var user model.User
		if findErr := s.db.Where("email = ?", email).First(&user).Error; findErr == nil {
			redirect := s.safeRedirect(c.Query("redirect_to"))
			if sendErr := s.createAndSendAuthLink(&user, "recovery", redirect); sendErr != nil {
				logInternal("send password recovery", sendErr)
			}
		}
	}
	// Never reveal whether the address exists.
	c.JSON(http.StatusOK, gin.H{})
}

func (s *Server) verifyEmailToken(c *gin.Context) {
	rawToken := c.Query("token")
	kind := c.Query("type")
	if (kind != "recovery" && kind != "signup") || len(rawToken) < 32 || len(rawToken) > 512 {
		s.redirectAuthError(c, c.Query("redirect_to"), "invalid_token")
		return
	}
	hash := security.HashToken(rawToken)
	var token model.AuthToken
	var user model.User
	now := time.Now().UTC()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("token_hash = ? AND kind = ? AND used_at IS NULL AND expires_at > ?", hash, kind, now).First(&token).Error; err != nil {
			return err
		}
		result := tx.Model(&model.AuthToken{}).Where("id = ? AND used_at IS NULL", token.ID).Update("used_at", now)
		if result.Error != nil || result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.First(&user, "id = ?", token.UserID).Error; err != nil {
			return err
		}
		if kind == "signup" && user.EmailConfirmedAt == nil {
			user.EmailConfirmedAt = &now
			return tx.Model(&user).Update("email_confirmed_at", now).Error
		}
		return nil
	})
	if err != nil {
		s.redirectAuthError(c, c.Query("redirect_to"), "expired_token")
		return
	}
	session, err := s.createSession(&user)
	if err != nil {
		s.redirectAuthError(c, token.Redirect, "server_error")
		return
	}
	redirect := s.safeRedirect(token.Redirect)
	fragment := url.Values{
		"access_token":  {session["access_token"].(string)},
		"refresh_token": {session["refresh_token"].(string)},
		"expires_in":    {strconv.FormatInt(session["expires_in"].(int64), 10)},
		"token_type":    {"bearer"},
		"type":          {kind},
	}.Encode()
	c.Redirect(http.StatusFound, redirect+"#"+fragment)
}

func (s *Server) createSession(user *model.User) (gin.H, error) {
	refreshToken, err := security.RandomToken(48)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	session := model.Session{
		ID: uuid.NewString(), UserID: user.ID, RefreshHash: security.HashToken(refreshToken),
		ExpiresAt: now.Add(s.cfg.RefreshTokenTTL),
	}
	if err := s.db.Create(&session).Error; err != nil {
		return nil, err
	}
	return s.sessionResponse(user, &session, refreshToken)
}

func (s *Server) sessionResponse(user *model.User, session *model.Session, refreshToken string) (gin.H, error) {
	var usage int64
	if err := s.db.Model(&model.File{}).Select("COALESCE(SUM(file_size), 0)").Where("user_id = ? AND ready = ?", user.ID, true).Scan(&usage).Error; err != nil {
		return nil, err
	}
	accessToken, expiresAt, err := s.tokens.IssueAccess(s.publicURL, user.ID, user.Email, session.ID, usage, s.cfg.StorageQuotaBytes, s.cfg.AccessTokenTTL)
	if err != nil {
		return nil, err
	}
	return gin.H{
		"access_token": accessToken, "token_type": "bearer",
		"expires_in": int64(s.cfg.AccessTokenTTL.Seconds()), "expires_at": expiresAt,
		"refresh_token": refreshToken, "user": publicUser(user),
	}, nil
}

func (s *Server) accessClaims(c *gin.Context) (*security.AccessClaims, error) {
	header := c.GetHeader("Authorization")
	if len(header) < 8 {
		return nil, errors.New("missing token")
	}
	return s.tokens.ParseAccess(strings.TrimSpace(header[7:]), s.publicURL)
}

func publicUser(user *model.User) gin.H {
	metadata := map[string]any{}
	_ = json.Unmarshal([]byte(user.UserMetadata), &metadata)
	created := formatTime(user.CreatedAt)
	updated := formatTime(user.UpdatedAt)
	lastSignIn := nullableTime(user.LastSignInAt)
	emailConfirmed := nullableTime(user.EmailConfirmedAt)
	identity := gin.H{
		"identity_id": user.ID, "id": user.ID, "user_id": user.ID, "provider": "email",
		"identity_data":   gin.H{"email": user.Email, "email_verified": user.EmailConfirmedAt != nil, "phone_verified": false, "sub": user.ID},
		"last_sign_in_at": lastSignIn, "created_at": created, "updated_at": updated, "email": user.Email,
	}
	return gin.H{
		"id": user.ID, "aud": "authenticated", "role": "authenticated", "email": user.Email,
		"email_confirmed_at": emailConfirmed, "confirmed_at": emailConfirmed, "phone": "",
		"last_sign_in_at": lastSignIn, "app_metadata": gin.H{"provider": "email", "providers": []string{"email"}},
		"user_metadata": metadata, "identities": []any{identity}, "created_at": created, "updated_at": updated,
		"is_anonymous": false,
	}
}

func normalizeEmail(input string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(input))
	if email == "" || len(email) > maximumEmailBytes || strings.Count(email, "@") != 1 || strings.ContainsAny(email, "\r\n\x00 ") {
		return "", errors.New("invalid email")
	}
	parts := strings.SplitN(email, "@", 2)
	if parts[0] == "" || parts[1] == "" || !strings.Contains(parts[1], ".") {
		return "", errors.New("invalid email")
	}
	return email, nil
}

func validatePassword(password string) error {
	if !utf8.ValidString(password) || len([]byte(password)) < minimumPasswordBytes || len([]byte(password)) > maximumPasswordBytes {
		return errors.New("invalid password length")
	}
	return nil
}

func encodeMetadata(metadata map[string]any) (string, error) {
	if metadata == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil || len(encoded) > maximumMetadataBytes {
		return "", errors.New("invalid metadata")
	}
	return string(encoded), nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}
