package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

type StoredObject struct {
	ObjectKey string
	Name      string
	MimeType  string
	Size      int64
	SHA256    string
}

type StorageService interface {
	Root() string
	Save(ctx context.Context, taskID, kind, filename string, reader io.Reader) (*StoredObject, error)
	CopyFile(ctx context.Context, taskID, kind, objectName, sourcePath string) (*StoredObject, error)
	CopyFileToObject(ctx context.Context, objectKey, sourcePath string) (*StoredObject, error)
	Open(objectKey string) (*os.File, error)
	Path(objectKey string) string
}

type LocalStorage struct {
	root string
}

func NewLocalStorage(root string) *LocalStorage {
	if root == "" {
		root = "storage"
	}
	return &LocalStorage{root: root}
}

func (s *LocalStorage) Root() string {
	return s.root
}

func (s *LocalStorage) Save(ctx context.Context, taskID, kind, filename string, reader io.Reader) (*StoredObject, error) {
	name := sanitizeFilename(filename)
	objectKey := filepath.ToSlash(filepath.Join("tasks", taskID, kind, name))
	return s.saveObject(ctx, objectKey, name, reader)
}

func (s *LocalStorage) saveObject(ctx context.Context, objectKey, name string, reader io.Reader) (*StoredObject, error) {
	cleanKey, err := cleanObjectKey(objectKey)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = filepath.Base(cleanKey)
	}
	targetPath := s.Path(cleanKey)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return nil, err
	}
	target, err := os.Create(targetPath)
	if err != nil {
		return nil, err
	}
	defer target.Close()

	hash := sha256.New()
	written, err := io.Copy(target, io.TeeReader(reader, hash))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &StoredObject{
		ObjectKey: cleanKey,
		Name:      name,
		MimeType:  mime.TypeByExtension(filepath.Ext(name)),
		Size:      written,
		SHA256:    hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func (s *LocalStorage) CopyFile(ctx context.Context, taskID, kind, objectName, sourcePath string) (*StoredObject, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	return s.Save(ctx, taskID, kind, objectName, source)
}

func (s *LocalStorage) CopyFileToObject(ctx context.Context, objectKey, sourcePath string) (*StoredObject, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	return s.saveObject(ctx, objectKey, filepath.Base(objectKey), source)
}

func (s *LocalStorage) Open(objectKey string) (*os.File, error) {
	cleanKey, err := cleanObjectKey(objectKey)
	if err != nil {
		return nil, fmt.Errorf("invalid object key")
	}
	return os.Open(s.Path(cleanKey))
}

func (s *LocalStorage) Path(objectKey string) string {
	return filepath.Join(s.root, filepath.FromSlash(objectKey))
}

func sanitizeFilename(filename string) string {
	name := filepath.Base(filename)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "file"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", "\x00", "_")
	return replacer.Replace(name)
}

func cleanObjectKey(objectKey string) (string, error) {
	key := strings.TrimSpace(filepath.ToSlash(objectKey))
	if key == "" {
		return "", fmt.Errorf("object key is empty")
	}
	if strings.HasPrefix(key, "/") || strings.Contains(key, "\x00") {
		return "", fmt.Errorf("invalid object key")
	}
	parts := strings.Split(key, "/")
	cleanParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", fmt.Errorf("invalid object key")
		}
		cleanParts = append(cleanParts, part)
	}
	if len(cleanParts) == 0 {
		return "", fmt.Errorf("object key is empty")
	}
	return strings.Join(cleanParts, "/"), nil
}
