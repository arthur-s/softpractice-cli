// Package starterbundle builds and safely extracts immutable learner starter archives.
package starterbundle

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxFiles     = 2_000
	maxFileBytes = 5 << 20
	maxTotal     = 50 << 20
)

type File struct {
	Path   string
	SHA256 string
}

type Artifact struct {
	SHA256 string
	Size   int64
}

// Build creates a reproducible tar.gz containing exactly manifestFiles from root.
func Build(root string, manifestFiles []File, destination io.Writer) (Artifact, error) {
	if len(manifestFiles) == 0 || len(manifestFiles) > maxFiles {
		return Artifact{}, errors.New("starter manifest has an invalid file count")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Artifact{}, fmt.Errorf("resolve starter root: %w", err)
	}
	hash := sha256.New()
	counting := &countingWriter{writer: io.MultiWriter(destination, hash)}
	gzipWriter, err := gzip.NewWriterLevel(counting, gzip.BestCompression)
	if err != nil {
		return Artifact{}, err
	}
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)

	var total int64
	previous := ""
	for index, file := range manifestFiles {
		if err := validatePath(file.Path); err != nil || (index > 0 && previous >= file.Path) {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return Artifact{}, errors.New("starter manifest paths must be unique, safe, and sorted")
		}
		previous = file.Path
		fullPath := filepath.Join(root, filepath.FromSlash(file.Path))
		if !isWithin(root, fullPath) {
			return Artifact{}, errors.New("starter file escapes its root")
		}
		info, err := os.Lstat(fullPath)
		if err != nil {
			return Artifact{}, fmt.Errorf("inspect starter file %q: %w", file.Path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxFileBytes {
			return Artifact{}, fmt.Errorf("starter file %q is not a supported regular file", file.Path)
		}
		total += info.Size()
		if total > maxTotal {
			return Artifact{}, errors.New("starter archive exceeds the uncompressed size limit")
		}
		body, err := os.Open(fullPath)
		if err != nil {
			return Artifact{}, fmt.Errorf("open starter file %q: %w", file.Path, err)
		}
		fileHash := sha256.New()
		written, copyErr := io.Copy(fileHash, io.LimitReader(body, maxFileBytes+1))
		closeErr := body.Close()
		if copyErr != nil || closeErr != nil || written != info.Size() {
			return Artifact{}, fmt.Errorf("read starter file %q", file.Path)
		}
		if hex.EncodeToString(fileHash.Sum(nil)) != file.SHA256 {
			return Artifact{}, fmt.Errorf("starter file %q SHA-256 does not match its manifest", file.Path)
		}
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: file.Path, Mode: 0o644, Size: info.Size(), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR,
		}); err != nil {
			return Artifact{}, err
		}
		// Re-open after integrity verification so the archive is written only once.
		body, err = os.Open(fullPath)
		if err != nil {
			return Artifact{}, err
		}
		_, copyErr = io.Copy(tarWriter, body)
		closeErr = body.Close()
		if copyErr != nil || closeErr != nil {
			return Artifact{}, fmt.Errorf("write starter file %q", file.Path)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return Artifact{}, err
	}
	if err := gzipWriter.Close(); err != nil {
		return Artifact{}, err
	}
	return Artifact{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: counting.size}, nil
}

// BuildZIP creates the browser-friendly counterpart of the deterministic tar.gz.
func BuildZIP(root string, manifestFiles []File, destination io.Writer) (Artifact, error) {
	if len(manifestFiles) == 0 || len(manifestFiles) > maxFiles {
		return Artifact{}, errors.New("starter manifest has an invalid file count")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Artifact{}, fmt.Errorf("resolve starter root: %w", err)
	}
	hash := sha256.New()
	counting := &countingWriter{writer: io.MultiWriter(destination, hash)}
	zipWriter := zip.NewWriter(counting)
	var total int64
	previous := ""
	for index, file := range manifestFiles {
		if err := validatePath(file.Path); err != nil || (index > 0 && previous >= file.Path) {
			_ = zipWriter.Close()
			return Artifact{}, errors.New("starter manifest paths must be unique, safe, and sorted")
		}
		previous = file.Path
		fullPath := filepath.Join(root, filepath.FromSlash(file.Path))
		if !isWithin(root, fullPath) {
			return Artifact{}, errors.New("starter file escapes its root")
		}
		info, err := os.Lstat(fullPath)
		if err != nil {
			return Artifact{}, fmt.Errorf("inspect starter file %q: %w", file.Path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxFileBytes {
			return Artifact{}, fmt.Errorf("starter file %q is not a supported regular file", file.Path)
		}
		total += info.Size()
		if total > maxTotal {
			return Artifact{}, errors.New("starter archive exceeds the uncompressed size limit")
		}
		body, err := os.Open(fullPath)
		if err != nil {
			return Artifact{}, fmt.Errorf("open starter file %q: %w", file.Path, err)
		}
		fileHash := sha256.New()
		written, copyErr := io.Copy(fileHash, io.LimitReader(body, maxFileBytes+1))
		closeErr := body.Close()
		if copyErr != nil || closeErr != nil || written != info.Size() {
			return Artifact{}, fmt.Errorf("read starter file %q", file.Path)
		}
		if hex.EncodeToString(fileHash.Sum(nil)) != file.SHA256 {
			return Artifact{}, fmt.Errorf("starter file %q SHA-256 does not match its manifest", file.Path)
		}
		header := &zip.FileHeader{Name: file.Path, Method: zip.Deflate}
		header.SetMode(0o644)
		header.SetModTime(time.Unix(0, 0))
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return Artifact{}, err
		}
		body, err = os.Open(fullPath)
		if err != nil {
			return Artifact{}, err
		}
		_, copyErr = io.Copy(writer, body)
		closeErr = body.Close()
		if copyErr != nil || closeErr != nil {
			return Artifact{}, fmt.Errorf("write starter file %q", file.Path)
		}
	}
	if err := zipWriter.Close(); err != nil {
		return Artifact{}, err
	}
	return Artifact{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: counting.size}, nil
}

// Extract verifies a starter archive before materializing it into an empty directory.
func Extract(archive io.Reader, destination string, expectedSHA256 string, expectedSize int64) error {
	if expectedSize < 1 || len(expectedSHA256) != 64 {
		return errors.New("starter archive metadata is invalid")
	}
	if entries, err := os.ReadDir(destination); err != nil {
		return fmt.Errorf("inspect destination: %w", err)
	} else if len(entries) != 0 {
		return errors.New("starter destination must be empty")
	}
	hash := sha256.New()
	counted := &countingReader{reader: io.TeeReader(io.LimitReader(archive, expectedSize+1), hash)}
	gzipReader, err := gzip.NewReader(counted)
	if err != nil {
		return errors.New("starter archive is not a valid gzip stream")
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	var total int64
	files := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("starter archive is not a valid tar stream")
		}
		files++
		if files > maxFiles || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxFileBytes || validatePath(header.Name) != nil {
			return errors.New("starter archive contains an unsupported entry")
		}
		total += header.Size
		if total > maxTotal {
			return errors.New("starter archive exceeds the uncompressed size limit")
		}
		path := filepath.Join(destination, filepath.FromSlash(header.Name))
		if !isWithin(destination, path) {
			return errors.New("starter archive path escapes destination")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create starter file %q: %w", header.Name, err)
		}
		written, copyErr := io.Copy(file, io.LimitReader(tarReader, header.Size+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || written != header.Size {
			return fmt.Errorf("extract starter file %q", header.Name)
		}
	}
	if counted.size != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return errors.New("starter archive checksum or size does not match the published release")
	}
	return nil
}

type countingWriter struct {
	writer io.Writer
	size   int64
}

func (w *countingWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.size += int64(n)
	return n, err
}

type countingReader struct {
	reader io.Reader
	size   int64
}

func (r *countingReader) Read(data []byte) (int, error) {
	n, err := r.reader.Read(data)
	r.size += int64(n)
	return n, err
}

func validatePath(value string) error {
	if value == "" || len(value) > 240 || !filepath.IsLocal(filepath.FromSlash(value)) || strings.Contains(value, "\\") || strings.HasPrefix(value, ".git/") || value == ".git" {
		return errors.New("unsafe path")
	}
	return nil
}

func isWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
