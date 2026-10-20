package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mattn/go-sqlite3"
	"github.com/readest/readest-tiny-backend/internal/backup"
	"github.com/readest/readest-tiny-backend/internal/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openBackupDB(filename string) (*sql.DB, error) {
	uriPath := filepath.ToSlash(filename)
	if filepath.VolumeName(filename) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	u.RawQuery = "_foreign_keys=on&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func copySQLite(ctx context.Context, dst, src *sql.DB) error {
	source, err := src.Conn(ctx)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := dst.Conn(ctx)
	if err != nil {
		return err
	}
	defer target.Close()
	return target.Raw(func(targetDriver any) error {
		return source.Raw(func(sourceDriver any) error {
			d, ok := targetDriver.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("unsupported backup database driver")
			}
			s, ok := sourceDriver.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("unsupported source database driver")
			}
			operation, err := d.Backup("main", s, "main")
			if err != nil {
				return err
			}
			for {
				if err := ctx.Err(); err != nil {
					return errors.Join(err, operation.Finish())
				}
				remaining := operation.Remaining()
				done, stepErr := operation.Step(256)
				if stepErr != nil || done {
					return errors.Join(stepErr, operation.Finish())
				}
				if operation.Remaining() == remaining {
					timer := time.NewTimer(10 * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
					case <-timer.C:
					}
				}
			}
		})
	})
}

func snapshotFiles(ctx context.Context, db *sql.DB) ([]model.File, error) {
	orm, err := gorm.Open(sqlite.Dialector{Conn: db}, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	var files []model.File
	if err := orm.WithContext(ctx).Where("ready = ?", true).Limit(100000).Find(&files).Error; err != nil {
		return nil, err
	}
	if len(files) >= 100000 {
		return nil, errors.New("备份文件数量超过限制")
	}
	for _, file := range files {
		if _, err := objectRelativePath(&file); err != nil {
			return nil, errors.New("备份数据库含有无效的文件路径")
		}
		if file.FileSize <= 0 || !isUserBookObjectKey(file.FileKey, file.UserID) || file.FileKey != file.UserID+"/"+file.FileName {
			return nil, errors.New("备份数据库含有无效的文件记录")
		}
	}
	return files, nil
}

func cleanBackupSessions(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DELETE FROM sessions; DELETE FROM auth_tokens;
		DELETE FROM files WHERE ready = 0;
		UPDATE files SET pending_size = 0, upload_nonce_hash = '', reservation_expires_at = NULL;`)
	return err
}

func (s *Server) writeBackup(ctx context.Context, workspace string, outputRoot *os.Root, outputName, passphrase string) error {
	workRoot, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer workRoot.Close()
	snapshot, err := openBackupDB(filepath.Join(workspace, "database.sqlite"))
	if err != nil {
		return err
	}
	defer snapshot.Close()
	live, err := s.db.DB()
	if err != nil {
		return err
	}
	if err := copySQLite(ctx, snapshot, live); err != nil {
		return err
	}
	if err := cleanBackupSessions(ctx, snapshot); err != nil {
		return err
	}
	if _, err := snapshot.ExecContext(ctx, "PRAGMA journal_mode = DELETE"); err != nil {
		return err
	}
	files, err := snapshotFiles(ctx, snapshot)
	if err != nil {
		return err
	}
	stat, err := workRoot.Stat("database.sqlite")
	if err != nil {
		return err
	}
	sources := []backup.Source{{Name: "database.sqlite", Size: stat.Size(), Open: func() (io.ReadCloser, error) { return workRoot.Open("database.sqlite") }}}
	for _, file := range files {
		relative, _ := objectRelativePath(&file)
		sources = append(sources, backup.Source{Name: "objects/" + filepath.ToSlash(relative), Size: file.FileSize,
			Open: func() (io.ReadCloser, error) { return s.storage.Open(relative) },
		})
	}
	output, err := outputRoot.OpenFile(outputName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writeErr := backup.WriteArchive(ctx, output, passphrase, sources, s.backupMaxBytes())
	return errors.Join(writeErr, output.Sync(), output.Close())
}

func databaseSchema(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%' AND type IN ('table', 'trigger', 'view') ORDER BY type, name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var schema strings.Builder
	for rows.Next() {
		var kind, name, definition string
		if err := rows.Scan(&kind, &name, &definition); err != nil {
			return "", err
		}
		fmt.Fprintf(&schema, "%s\x00%s\x00%s\n", kind, name, definition)
	}
	return schema.String(), rows.Err()
}

func (s *Server) validateBackupDB(ctx context.Context, restored *sql.DB, manifest *backup.Manifest) ([]model.File, error) {
	var integrity string
	if err := restored.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return nil, errors.New("备份数据库完整性检查失败")
	}
	live, err := s.db.DB()
	if err != nil {
		return nil, err
	}
	expected, err := databaseSchema(ctx, live)
	if err != nil {
		return nil, err
	}
	actual, err := databaseSchema(ctx, restored)
	if err != nil || expected != actual {
		return nil, errors.New("备份数据库结构与当前服务器版本不匹配")
	}
	rows, err := restored.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, err
	}
	invalid := rows.Next()
	rowsErr := errors.Join(rows.Err(), rows.Close())
	if invalid || rowsErr != nil {
		return nil, errors.New("备份数据库关联数据损坏")
	}
	var admins int
	if err := restored.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE is_admin = 1").Scan(&admins); err != nil || admins == 0 {
		return nil, errors.New("备份中没有可用于恢复后登录的管理员账号")
	}
	files, err := snapshotFiles(ctx, restored)
	if err != nil {
		return nil, err
	}
	if len(manifest.Entries) != len(files)+1 {
		return nil, errors.New("备份清单与数据库中的文件数量不一致")
	}
	entries := make(map[string]int64, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		entries[entry.Name] = entry.Size
	}
	for _, file := range files {
		relative, _ := objectRelativePath(&file)
		if size, ok := entries["objects/"+filepath.ToSlash(relative)]; !ok || size != file.FileSize {
			return nil, errors.New("备份缺少数据库引用的书籍文件")
		}
	}
	return files, nil
}

// Install each restored object under a new UUID before atomically replacing the
// database. The old database and objects remain usable if any preparation fails.
func (s *Server) applyBackup(ctx context.Context, restored *sql.DB, workspace string, files []model.File) error {
	var oldFiles []model.File
	if err := s.db.Where("ready = ?", true).Find(&oldFiles).Error; err != nil {
		return err
	}
	installed := make([]string, 0, len(files))
	committed := false
	defer func() {
		if !committed {
			for _, name := range installed {
				_ = s.storage.Remove(name)
			}
		}
	}()
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		oldPath, _ := objectRelativePath(&file)
		file.ObjectName = uuid.NewString() + strings.ToLower(filepath.Ext(file.FileName))
		newPath, _ := objectRelativePath(&file)
		if err := s.storage.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
			return err
		}
		if err := s.storage.Rename(filepath.Join(workspace, "objects", oldPath), newPath); err != nil {
			return err
		}
		installed = append(installed, newPath)
		if _, err := restored.ExecContext(ctx, "UPDATE files SET id = ?, object_name = ? WHERE id = ?", uuid.NewString(), file.ObjectName, file.ID); err != nil {
			return err
		}
	}
	if err := cleanBackupSessions(ctx, restored); err != nil {
		return err
	}
	live, err := s.db.DB()
	if err != nil {
		return err
	}
	if err := copySQLite(ctx, live, restored); err != nil {
		return err
	}
	committed = true
	for _, file := range oldFiles {
		if name, err := objectRelativePath(&file); err == nil {
			if err := s.storage.Remove(name); err != nil && !os.IsNotExist(err) {
				logInternal("remove replaced backup object", err)
			}
		}
	}
	return nil
}
