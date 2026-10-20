package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"testing"

	"filippo.io/age"
)

func extractForTest(t *testing.T, data []byte, password string, limit int64) (*Manifest, error) {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	return ExtractArchive(context.Background(), bytes.NewReader(data), root, password, limit)
}

func TestArchiveEncryptionAndIntegrity(t *testing.T) {
	password := "test-backup-passphrase"
	payload := []byte("sensitive database payload")
	var archive bytes.Buffer
	err := WriteArchive(context.Background(), &archive, password, []Source{{Name: "database.sqlite", Size: int64(len(payload)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }}}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive.Bytes(), payload) {
		t.Fatal("plaintext present in encrypted backup")
	}
	manifest, err := extractForTest(t, archive.Bytes(), password, 1024)
	if err != nil || len(manifest.Entries) != 1 {
		t.Fatal("round trip", manifest, err)
	}
	for _, test := range []struct {
		name     string
		data     []byte
		password string
		limit    int64
	}{
		{"wrong password", archive.Bytes(), "incorrect password", 1024},
		{"truncated authenticated stream", archive.Bytes()[:archive.Len()-1], password, 1024},
		{"extraction limit", archive.Bytes(), password, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := extractForTest(t, test.data, test.password, test.limit); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
	tampered := bytes.Clone(archive.Bytes())
	tampered[len(tampered)-20] ^= 0x01
	if _, err := extractForTest(t, tampered, password, 1024); err == nil {
		t.Fatal("tampered archive accepted")
	}
}

func maliciousArchive(t *testing.T, headers []tar.Header, entries []Entry, extra bool) []byte {
	t.Helper()
	recipient, err := age.NewScryptRecipient("test-backup-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	recipient.SetWorkFactor(10)
	var output bytes.Buffer
	encrypted, err := age.Encrypt(&output, recipient)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(encrypted)
	archive := tar.NewWriter(compressed)
	for _, header := range headers {
		if err := archive.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := archive.Write(bytes.Repeat([]byte("x"), int(header.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	manifest, _ := json.Marshal(Manifest{Version: 1, Entries: entries})
	if err := archive.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	_, _ = archive.Write(manifest)
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if extra {
		_, _ = compressed.Write([]byte("extra content"))
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encrypted.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestArchiveRejectsUnsafeContents(t *testing.T) {
	hash := sha256.Sum256([]byte("x"))
	valid := Entry{Name: "database.sqlite", Size: 1, SHA256: hex.EncodeToString(hash[:])}
	for _, test := range []struct {
		name    string
		headers []tar.Header
		entries []Entry
		extra   bool
	}{
		{"traversal", []tar.Header{{Name: "objects/../../outside", Typeflag: tar.TypeReg}}, nil, false},
		{"windows separator", []tar.Header{{Name: `objects/..\outside`, Typeflag: tar.TypeReg}}, nil, false},
		{"symlink", []tar.Header{{Name: "objects/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}}, nil, false},
		{"duplicate path", []tar.Header{{Name: "database.sqlite", Size: 1}, {Name: "database.sqlite", Size: 1}}, []Entry{valid}, false},
		{"incorrect checksum", []tar.Header{{Name: "database.sqlite", Size: 1}}, []Entry{{Name: "database.sqlite", Size: 1, SHA256: "wrong"}}, false},
		{"missing manifest entry", []tar.Header{{Name: "database.sqlite", Size: 1}}, nil, false},
		{"unauthorized trailing content", []tar.Header{{Name: "database.sqlite", Size: 1}}, []Entry{valid}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := maliciousArchive(t, test.headers, test.entries, test.extra)
			if _, err := extractForTest(t, data, "test-backup-passphrase", 1024); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
