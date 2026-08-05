package model

import "time"

type User struct {
	ID                string `gorm:"type:text;primaryKey"`
	Email             string `gorm:"type:text;uniqueIndex;not null"`
	PasswordHash      string `gorm:"type:text;not null"`
	UserMetadata      string `gorm:"type:text;not null;default:'{}'"`
	IsAdmin           bool   `gorm:"not null;default:false;index"`
	StorageQuotaBytes *int64
	EmailConfirmedAt  *time.Time
	LastSignInAt      *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Session struct {
	ID          string `gorm:"type:text;primaryKey"`
	UserID      string `gorm:"type:text;not null;index"`
	RefreshHash string `gorm:"type:text;not null;uniqueIndex"`
	ExpiresAt   time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	User        User `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
}

type AuthToken struct {
	ID        string `gorm:"type:text;primaryKey"`
	UserID    string `gorm:"type:text;not null;index"`
	Kind      string `gorm:"type:text;not null;index"`
	TokenHash string `gorm:"type:text;not null;uniqueIndex"`
	Redirect  string `gorm:"type:text;not null"`
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
	User      User `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
}

type Book struct {
	UserID                 string `gorm:"type:text;primaryKey"`
	BookHash               string `gorm:"type:text;primaryKey"`
	MetaHash               *string
	Format                 *string
	Title                  *string
	SourceTitle            *string
	Author                 *string
	Group                  *string
	Tags                   *string `gorm:"type:text"`
	Progress               *string `gorm:"type:text"`
	ReadingStatus          *string
	ReadingStatusUpdatedAt *time.Time
	CoverHash              *string
	CoverUpdatedAt         *time.Time
	MetadataUpdatedAt      *time.Time
	GroupID                *string
	GroupName              *string
	Metadata               *string    `gorm:"type:text"`
	CreatedAt              *time.Time `gorm:"autoCreateTime:false"`
	UpdatedAt              *time.Time `gorm:"autoUpdateTime:false;index"`
	DeletedAt              *time.Time `gorm:"index"`
	UploadedAt             *time.Time
	SyncedAt               time.Time       `gorm:"autoUpdateTime:false;index"`
	User                   User            `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Presence               map[string]bool `gorm:"-"`
}

type BookConfig struct {
	UserID       string `gorm:"type:text;primaryKey"`
	BookHash     string `gorm:"type:text;primaryKey"`
	MetaHash     *string
	Location     *string
	XPointer     *string
	Progress     *string `gorm:"type:text"`
	RSVPPosition *string
	SearchConfig *string         `gorm:"type:text"`
	ViewSettings *string         `gorm:"type:text"`
	CreatedAt    *time.Time      `gorm:"autoCreateTime:false"`
	UpdatedAt    *time.Time      `gorm:"autoUpdateTime:false;index"`
	DeletedAt    *time.Time      `gorm:"index"`
	User         User            `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Presence     map[string]bool `gorm:"-"`
}

type BookNote struct {
	UserID    string     `gorm:"type:text;primaryKey"`
	BookHash  string     `gorm:"type:text;primaryKey"`
	ID        string     `gorm:"type:text;primaryKey"`
	Payload   string     `gorm:"type:text;not null"`
	UpdatedAt time.Time  `gorm:"autoUpdateTime:false;index"`
	DeletedAt *time.Time `gorm:"index"`
}

type File struct {
	ID                   string `gorm:"type:text;primaryKey"`
	UserID               string `gorm:"type:text;not null;index"`
	BookHash             string `gorm:"type:text;not null;index"`
	FileKey              string `gorm:"type:text;not null;uniqueIndex"`
	FileName             string `gorm:"type:text;not null"`
	ObjectName           string `gorm:"type:text"`
	FileSize             int64
	Ready                bool
	PendingSize          int64
	UploadNonceHash      string `gorm:"type:text"`
	ReservationExpiresAt *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	User                 User `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
}
