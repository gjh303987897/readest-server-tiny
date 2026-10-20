package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/readest/readest-tiny-backend/internal/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNextBookSyncTimeAdvancesWithinOneMillisecondAndAcrossLegacyTimestamps(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cursor.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&model.Book{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, latest := range []time.Time{now, now.Add(500 * time.Microsecond)} {
		row := model.Book{UserID: "user", BookHash: "book", SyncedAt: latest}
		if err := db.Omit("User").Save(&row).Error; err != nil {
			t.Fatal(err)
		}
		stamp, err := nextBookSyncTime(db, "user", now)
		if err != nil || !stamp.After(bookCursorTime(latest)) || stamp.Nanosecond()%int(time.Millisecond) != 0 {
			t.Fatalf("next cursor %v after %v: %v", stamp, latest, err)
		}
		other, err := nextBookSyncTime(db, "another-user", now)
		if err != nil || !other.Equal(now) {
			t.Fatalf("another user's clock changed: %v %v", other, err)
		}
	}
}
