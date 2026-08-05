package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type AccessClaims struct {
	Email                 string `json:"email"`
	Role                  string `json:"role"`
	SessionID             string `json:"session_id"`
	Plan                  string `json:"plan"`
	StorageUsageBytes     int64  `json:"storage_usage_bytes"`
	StoragePurchasedBytes int64  `json:"storage_purchased_bytes"`
	jwt.RegisteredClaims
}

type StorageClaims struct {
	Purpose string `json:"purpose"`
	FileID  string `json:"file_id"`
	FileKey string `json:"file_key"`
	Nonce   string `json:"nonce,omitempty"`
	Size    int64  `json:"size,omitempty"`
	jwt.RegisteredClaims
}

type Manager struct {
	secret []byte
}

func NewManager(secret []byte) *Manager {
	return &Manager{secret: append([]byte(nil), secret...)}
}

func (m *Manager) IssueAccess(issuer, userID, email, sessionID string, usage, baseQuota, effectiveQuota int64, ttl time.Duration) (string, int64, error) {
	now := time.Now().UTC()
	expires := now.Add(ttl)
	claims := AccessClaims{
		Email: email, Role: "authenticated", SessionID: sessionID, Plan: "free",
		StorageUsageBytes: usage, StoragePurchasedBytes: effectiveQuota - baseQuota,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: issuer + "/auth/v1", Subject: userID,
			Audience: jwt.ClaimStrings{"authenticated"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	return token, expires.Unix(), err
}

func (m *Manager) ParseAccess(token, issuer string) (*AccessClaims, error) {
	claims := &AccessClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("unexpected signing algorithm")
		}
		return m.secret, nil
	}, jwt.WithAudience("authenticated"), jwt.WithIssuer(issuer+"/auth/v1"), jwt.WithLeeway(15*time.Second), jwt.WithStrictDecoding())
	if err != nil || !parsed.Valid || claims.Subject == "" || claims.SessionID == "" {
		return nil, errors.New("invalid access token")
	}
	return claims, nil
}

func (m *Manager) IssueStorage(issuer, purpose, userID, fileID, fileKey, nonce string, size int64, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	claims := StorageClaims{
		Purpose: purpose, FileID: fileID, FileKey: fileKey, Nonce: nonce, Size: size,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: issuer, Subject: userID, Audience: jwt.ClaimStrings{"readest-storage"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
}

func (m *Manager) ParseStorage(token, issuer, purpose string) (*StorageClaims, error) {
	claims := &StorageClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("unexpected signing algorithm")
		}
		return m.secret, nil
	}, jwt.WithAudience("readest-storage"), jwt.WithIssuer(issuer), jwt.WithLeeway(5*time.Second), jwt.WithStrictDecoding())
	if err != nil || !parsed.Valid || claims.Purpose != purpose || claims.Subject == "" || claims.FileID == "" {
		return nil, errors.New("invalid storage token")
	}
	return claims, nil
}

func RandomToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func EqualHash(token, encodedHash string) bool {
	expected, err := hex.DecodeString(encodedHash)
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	actual := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(actual[:], expected) == 1
}
