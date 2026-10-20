package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"filippo.io/age"
)

type Source struct {
	Name string
	Size int64
	Open func() (io.ReadCloser, error)
}

type Entry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	Entries   []Entry   `json:"entries"`
}

const maxManifestBytes = 32 << 20
const maxArchiveEntries = 100000

func validEntry(name string) bool {
	return fs.ValidPath(name) && len(name) <= 1024 && !strings.ContainsAny(name, "\\:\x00") &&
		(name == "database.sqlite" || strings.HasPrefix(name, "objects/"))
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func WriteArchive(ctx context.Context, dst io.Writer, passphrase string, sources []Source, maxBytes int64) error {
	if len(sources) == 0 || len(sources) > maxArchiveEntries {
		return errors.New("备份文件数量无效")
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return err
	}
	encrypted, err := age.Encrypt(dst, recipient)
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(encrypted)
	archive := tar.NewWriter(compressed)
	manifest := Manifest{Version: 1, CreatedAt: time.Now().UTC(), Entries: make([]Entry, 0, len(sources))}
	seen := make(map[string]bool)
	var total int64
	for _, source := range sources {
		if !validEntry(source.Name) || source.Size < 0 || source.Size > maxBytes-total || seen[source.Name] {
			return errors.New("备份路径或数据大小无效")
		}
		seen[source.Name] = true
		total += source.Size
		if err := archive.WriteHeader(&tar.Header{Name: source.Name, Mode: 0o600, Size: source.Size, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		input, err := source.Open()
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(archive, hash), contextReader{ctx, io.LimitReader(input, source.Size+1)})
		closeErr := input.Close()
		if copyErr != nil || n != source.Size || closeErr != nil {
			return errors.New("备份文件读取失败或大小发生变化")
		}
		manifest.Entries = append(manifest.Entries, Entry{Name: source.Name, Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(data) > maxManifestBytes {
		return errors.New("备份文件清单过大")
	}
	if err := archive.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := archive.Write(data); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	return encrypted.Close()
}

// Extraction never writes outside this newly created staging root, and never
// follows an archive-supplied symlink. Callers apply data only after full validation.
func ExtractArchive(ctx context.Context, src io.Reader, dst *os.Root, passphrase string, maxBytes int64) (*Manifest, error) {
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	identity.SetMaxWorkFactor(18)
	decrypted, err := age.Decrypt(contextReader{ctx, src}, identity)
	if err != nil {
		return nil, errors.New("备份密码错误或文件不是有效的加密备份")
	}
	compressed, err := gzip.NewReader(decrypted)
	if err != nil {
		return nil, errors.New("备份压缩数据无效")
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	seen := make(map[string]Entry)
	var manifest *Manifest
	var total int64
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("备份归档损坏")
		}
		if header.Typeflag != tar.TypeReg || manifest != nil {
			return nil, errors.New("备份包含不支持的文件类型或额外内容")
		}
		if header.Name == "manifest.json" {
			if header.Size <= 0 || header.Size > maxManifestBytes {
				return nil, errors.New("备份清单大小无效")
			}
			data, err := io.ReadAll(io.LimitReader(archive, maxManifestBytes+1))
			if err != nil {
				return nil, err
			}
			manifest = &Manifest{}
			if err := json.Unmarshal(data, manifest); err != nil || manifest.Version != 1 || len(manifest.Entries) > maxArchiveEntries {
				return nil, errors.New("不支持的备份格式")
			}
			continue
		}
		if !validEntry(header.Name) || header.Size < 0 || header.Size > maxBytes-total || len(seen) >= maxArchiveEntries {
			return nil, errors.New("备份路径或解压大小超出限制")
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return nil, errors.New("备份存在重复路径")
		}
		total += header.Size
		if err := dst.MkdirAll(path.Dir(header.Name), 0o700); err != nil {
			return nil, err
		}
		output, err := dst.OpenFile(header.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(output, hash), contextReader{ctx, archive})
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || n != header.Size {
			return nil, errors.New("解压备份失败")
		}
		seen[header.Name] = Entry{Name: header.Name, Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	// Tar EOF does not imply authenticated age EOF. Read through both trailers.
	n, err := io.Copy(io.Discard, io.LimitReader(compressed, 1))
	if err != nil || n != 0 {
		return nil, errors.New("备份尾部损坏或存在额外数据")
	}
	if n, err := io.Copy(io.Discard, io.LimitReader(decrypted, 1)); err != nil || n != 0 {
		return nil, errors.New("备份加密完整性验证失败")
	}
	if manifest == nil || len(manifest.Entries) != len(seen) {
		return nil, errors.New("备份缺少完整文件清单")
	}
	if _, ok := seen["database.sqlite"]; !ok {
		return nil, errors.New("备份缺少数据库")
	}
	checked := make(map[string]bool)
	for _, expected := range manifest.Entries {
		actual, ok := seen[expected.Name]
		if !ok || actual != expected || checked[expected.Name] {
			return nil, fmt.Errorf("备份文件校验失败：%s", expected.Name)
		}
		checked[expected.Name] = true
	}
	return manifest, nil
}
