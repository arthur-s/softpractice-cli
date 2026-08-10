package submission

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
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultMaxCompressedBytes   = int64(10 << 20)
	defaultMaxUncompressedBytes = int64(50 << 20)
	defaultMaxFileBytes         = int64(5 << 20)
	defaultMaxFiles             = 2_000
	defaultMaxEntries           = 4_000
	defaultMaxPathBytes         = 240
	defaultMaxPathDepth         = 20
)

var canonicalTime = time.Unix(0, 0).UTC()

type ArchiveLimits struct {
	MaxCompressedBytes   int64
	MaxUncompressedBytes int64
	MaxFileBytes         int64
	MaxFiles             int
	MaxEntries           int
	MaxPathBytes         int
	MaxPathDepth         int
}

func DefaultArchiveLimits() ArchiveLimits {
	return ArchiveLimits{
		MaxCompressedBytes:   defaultMaxCompressedBytes,
		MaxUncompressedBytes: defaultMaxUncompressedBytes,
		MaxFileBytes:         defaultMaxFileBytes,
		MaxFiles:             defaultMaxFiles,
		MaxEntries:           defaultMaxEntries,
		MaxPathBytes:         defaultMaxPathBytes,
		MaxPathDepth:         defaultMaxPathDepth,
	}
}

type NormalizedArchive struct {
	Path              string
	ContentSHA256     string
	CompressedBytes   int64
	UncompressedBytes int64
	FileCount         int
}

// MaterializedArchive is a validated submission tree extracted from a tar.gz.
// The caller owns Path and must remove it when the evaluation finishes.
type MaterializedArchive struct {
	Path              string
	ContentSHA256     string
	CompressedBytes   int64
	UncompressedBytes int64
	FileCount         int
}

type Manifest struct {
	SchemaVersion  int    `json:"schema_version"`
	WorkspaceID    string `json:"workspace_id"`
	ProjectID      string `json:"project_id"`
	AssignmentID   string `json:"assignment_id"`
	BaseRevisionID string `json:"base_revision_id,omitempty"`
	CommitSHA      string `json:"commit_sha"`
	CLIVersion     string `json:"cli_version"`
}

// NormalizeTarGz validates an untrusted tar.gz and emits a deterministic archive.
// The caller owns the returned file and must remove it after upload.
func NormalizeTarGz(sourcePath, outputDirectory string, limits ArchiveLimits) (_ NormalizedArchive, err error) {
	return normalize(sourcePath, outputDirectory, limits, extractValidatedTarGz)
}

// NormalizeZIP applies the exact same safety limits and canonical content
// representation as NormalizeTarGz, allowing browser-native ZIP uploads.
func NormalizeZIP(sourcePath, outputDirectory string, limits ArchiveLimits) (_ NormalizedArchive, err error) {
	return normalize(sourcePath, outputDirectory, limits, extractValidatedZIP)
}

type archiveExtractor func(string, string, ArchiveLimits) (archiveStats, error)

func normalize(sourcePath, outputDirectory string, limits ArchiveLimits, extract archiveExtractor) (_ NormalizedArchive, err error) {
	limits = limits.withDefaults()

	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return NormalizedArchive{}, fmt.Errorf("stat source archive: %w", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return NormalizedArchive{}, errors.New("source archive must be a regular file")
	}
	if sourceInfo.Size() > limits.MaxCompressedBytes {
		return NormalizedArchive{}, fmt.Errorf("compressed archive exceeds %d bytes", limits.MaxCompressedBytes)
	}

	staging, err := os.MkdirTemp(outputDirectory, "submission-unpacked-*")
	if err != nil {
		return NormalizedArchive{}, fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	stats, err := extract(sourcePath, staging, limits)
	if err != nil {
		return NormalizedArchive{}, err
	}

	output, err := os.CreateTemp(outputDirectory, "submission-normalized-*.tar.gz")
	if err != nil {
		return NormalizedArchive{}, fmt.Errorf("create normalized archive: %w", err)
	}
	outputPath := output.Name()
	defer func() {
		_ = output.Close()
		if err != nil {
			_ = os.Remove(outputPath)
		}
	}()

	contentHash := sha256.New()
	gzipWriter, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		return NormalizedArchive{}, fmt.Errorf("create gzip writer: %w", err)
	}
	gzipWriter.Header.ModTime = canonicalTime
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(io.MultiWriter(gzipWriter, contentHash))

	if err = writeCanonicalTree(staging, tarWriter); err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		return NormalizedArchive{}, err
	}
	if err = tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return NormalizedArchive{}, fmt.Errorf("finish canonical tar: %w", err)
	}
	if err = gzipWriter.Close(); err != nil {
		return NormalizedArchive{}, fmt.Errorf("finish canonical gzip: %w", err)
	}
	if err = output.Close(); err != nil {
		return NormalizedArchive{}, fmt.Errorf("close normalized archive: %w", err)
	}

	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		return NormalizedArchive{}, fmt.Errorf("stat normalized archive: %w", err)
	}

	return NormalizedArchive{
		Path:              outputPath,
		ContentSHA256:     hex.EncodeToString(contentHash.Sum(nil)),
		CompressedBytes:   outputInfo.Size(),
		UncompressedBytes: stats.uncompressedBytes,
		FileCount:         stats.fileCount,
	}, nil
}

// MaterializeTarGz validates an untrusted archive with the same rules used by
// intake, extracts it into a new private temporary directory, and recomputes
// the canonical content hash. It never trusts paths or entry types from the
// archive directly. The caller owns the returned directory.
func MaterializeTarGz(sourcePath, outputDirectory string, limits ArchiveLimits) (_ MaterializedArchive, err error) {
	return materialize(sourcePath, outputDirectory, limits, extractValidatedTarGz)
}

// MaterializeZIP is the ZIP counterpart for evaluation workers.
func MaterializeZIP(sourcePath, outputDirectory string, limits ArchiveLimits) (_ MaterializedArchive, err error) {
	return materialize(sourcePath, outputDirectory, limits, extractValidatedZIP)
}

func materialize(sourcePath, outputDirectory string, limits ArchiveLimits, extract archiveExtractor) (_ MaterializedArchive, err error) {
	limits = limits.withDefaults()

	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return MaterializedArchive{}, fmt.Errorf("stat source archive: %w", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return MaterializedArchive{}, errors.New("source archive must be a regular file")
	}
	if sourceInfo.Size() > limits.MaxCompressedBytes {
		return MaterializedArchive{}, fmt.Errorf("compressed archive exceeds %d bytes", limits.MaxCompressedBytes)
	}

	staging, err := os.MkdirTemp(outputDirectory, "submission-materialized-*")
	if err != nil {
		return MaterializedArchive{}, fmt.Errorf("create materialization directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(staging)
		}
	}()

	stats, err := extract(sourcePath, staging, limits)
	if err != nil {
		return MaterializedArchive{}, err
	}
	contentHash := sha256.New()
	tarWriter := tar.NewWriter(contentHash)
	if err = writeCanonicalTree(staging, tarWriter); err != nil {
		_ = tarWriter.Close()
		return MaterializedArchive{}, err
	}
	if err = tarWriter.Close(); err != nil {
		return MaterializedArchive{}, fmt.Errorf("finish canonical tar hash: %w", err)
	}

	return MaterializedArchive{
		Path:              staging,
		ContentSHA256:     hex.EncodeToString(contentHash.Sum(nil)),
		CompressedBytes:   sourceInfo.Size(),
		UncompressedBytes: stats.uncompressedBytes,
		FileCount:         stats.fileCount,
	}, nil
}

type archiveStats struct {
	uncompressedBytes int64
	fileCount         int
	entryCount        int
}

func extractValidatedTarGz(sourcePath, staging string, limits ArchiveLimits) (archiveStats, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return archiveStats{}, fmt.Errorf("open source archive: %w", err)
	}
	defer source.Close()

	gzipReader, err := gzip.NewReader(io.LimitReader(source, limits.MaxCompressedBytes+1))
	if err != nil {
		return archiveStats{}, fmt.Errorf("open gzip stream: %w", err)
	}
	defer gzipReader.Close()

	reader := tar.NewReader(gzipReader)
	seen := make(map[string]struct{})
	var stats archiveStats

	for {
		header, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return archiveStats{}, fmt.Errorf("read tar entry: %w", readErr)
		}
		stats.entryCount++
		if stats.entryCount > limits.MaxEntries {
			return archiveStats{}, fmt.Errorf("archive exceeds %d entries", limits.MaxEntries)
		}
		// `git archive --format=tar.gz` on macOS writes this PAX global header.
		// It contains metadata, not a learner-visible path; archive/tar applies
		// any relevant attributes to subsequent headers, which are still fully
		// validated below.
		if header.Typeflag == tar.TypeXGlobalHeader {
			if header.Size < 0 || header.Size > limits.MaxFileBytes {
				return archiveStats{}, fmt.Errorf("PAX global header exceeds %d bytes", limits.MaxFileBytes)
			}
			stats.uncompressedBytes += header.Size
			if stats.uncompressedBytes > limits.MaxUncompressedBytes {
				return archiveStats{}, fmt.Errorf("archive exceeds %d uncompressed bytes", limits.MaxUncompressedBytes)
			}
			continue
		}

		name, err := validateArchivePath(header.Name, limits)
		if err != nil {
			return archiveStats{}, err
		}
		if _, duplicate := seen[name]; duplicate {
			return archiveStats{}, fmt.Errorf("duplicate archive path %q", name)
		}
		seen[name] = struct{}{}

		target := filepath.Join(staging, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return archiveStats{}, fmt.Errorf("create directory %q: %w", name, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > limits.MaxFileBytes {
				return archiveStats{}, fmt.Errorf("file %q exceeds %d bytes", name, limits.MaxFileBytes)
			}
			stats.fileCount++
			stats.uncompressedBytes += header.Size
			if stats.fileCount > limits.MaxFiles {
				return archiveStats{}, fmt.Errorf("archive exceeds %d files", limits.MaxFiles)
			}
			if stats.uncompressedBytes > limits.MaxUncompressedBytes {
				return archiveStats{}, fmt.Errorf("archive exceeds %d uncompressed bytes", limits.MaxUncompressedBytes)
			}
			if err := writeExtractedFile(target, reader, header.Size, header.FileInfo().Mode().Perm()); err != nil {
				return archiveStats{}, fmt.Errorf("extract %q: %w", name, err)
			}
		default:
			return archiveStats{}, fmt.Errorf("archive entry %q has forbidden type %d", name, header.Typeflag)
		}
	}

	if stats.fileCount == 0 {
		return archiveStats{}, errors.New("archive contains no files")
	}
	return stats, nil
}

func extractValidatedZIP(sourcePath, staging string, limits ArchiveLimits) (archiveStats, error) {
	reader, err := zip.OpenReader(sourcePath)
	if err != nil {
		return archiveStats{}, fmt.Errorf("open ZIP archive: %w", err)
	}
	defer reader.Close()
	if len(reader.File) > limits.MaxEntries {
		return archiveStats{}, fmt.Errorf("archive exceeds %d entries", limits.MaxEntries)
	}
	seen := make(map[string]struct{}, len(reader.File))
	var stats archiveStats
	for _, entry := range reader.File {
		stats.entryCount++
		if entry.Flags&0x1 != 0 {
			return archiveStats{}, fmt.Errorf("ZIP entry %q is encrypted", entry.Name)
		}
		if entry.Method != zip.Store && entry.Method != zip.Deflate {
			return archiveStats{}, fmt.Errorf("ZIP entry %q uses unsupported compression", entry.Name)
		}
		name, err := validateArchivePath(entry.Name, limits)
		if entry.FileInfo().IsDir() {
			name, err = validateArchivePath(strings.TrimSuffix(entry.Name, "/"), limits)
		}
		if err != nil {
			return archiveStats{}, err
		}
		if _, duplicate := seen[name]; duplicate {
			return archiveStats{}, fmt.Errorf("duplicate archive path %q", name)
		}
		seen[name] = struct{}{}
		target := filepath.Join(staging, filepath.FromSlash(name))
		if entry.FileInfo().IsDir() {
			if entry.UncompressedSize64 != 0 {
				return archiveStats{}, fmt.Errorf("ZIP directory %q is invalid", name)
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return archiveStats{}, fmt.Errorf("create directory %q: %w", name, err)
			}
			continue
		}
		if entry.Mode()&os.ModeType != 0 || entry.UncompressedSize64 > uint64(limits.MaxFileBytes) {
			return archiveStats{}, fmt.Errorf("ZIP entry %q is not a supported regular file", name)
		}
		stats.fileCount++
		stats.uncompressedBytes += int64(entry.UncompressedSize64)
		if stats.fileCount > limits.MaxFiles {
			return archiveStats{}, fmt.Errorf("archive exceeds %d files", limits.MaxFiles)
		}
		if stats.uncompressedBytes > limits.MaxUncompressedBytes {
			return archiveStats{}, fmt.Errorf("archive exceeds %d uncompressed bytes", limits.MaxUncompressedBytes)
		}
		body, err := entry.Open()
		if err != nil {
			return archiveStats{}, fmt.Errorf("open ZIP entry %q: %w", name, err)
		}
		writeErr := writeExtractedFile(target, body, int64(entry.UncompressedSize64), entry.Mode().Perm())
		closeErr := body.Close()
		if writeErr != nil || closeErr != nil {
			return archiveStats{}, fmt.Errorf("extract ZIP entry %q: %w", name, errors.Join(writeErr, closeErr))
		}
	}
	if stats.fileCount == 0 {
		return archiveStats{}, errors.New("archive contains no files")
	}
	return stats, nil
}

func validateArchivePath(name string, limits ArchiveLimits) (string, error) {
	if !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
		return "", errors.New("archive path must be valid UTF-8 without NUL")
	}
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", fmt.Errorf("invalid archive path %q", name)
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != strings.TrimSuffix(name, "/") {
		return "", fmt.Errorf("non-canonical archive path %q", name)
	}
	if len([]byte(cleaned)) > limits.MaxPathBytes {
		return "", fmt.Errorf("archive path %q exceeds %d UTF-8 bytes", cleaned, limits.MaxPathBytes)
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) > limits.MaxPathDepth {
		return "", fmt.Errorf("archive path %q exceeds depth %d", cleaned, limits.MaxPathDepth)
	}
	for _, part := range parts {
		if part == ".git" {
			return "", fmt.Errorf("archive path %q contains forbidden .git component", cleaned)
		}
	}
	return cleaned, nil
}

func writeExtractedFile(target string, reader io.Reader, size int64, inputMode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if inputMode&0o111 != 0 {
		mode = 0o755
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(file, reader, size)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func writeCanonicalTree(root string, writer *tar.Writer) error {
	var paths []string
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk normalized tree: %w", err)
	}
	sort.Strings(paths)

	for _, name := range paths {
		fullPath := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Stat(fullPath)
		if err != nil {
			return fmt.Errorf("stat %q: %w", name, err)
		}
		mode := int64(0o644)
		typeFlag := byte(tar.TypeReg)
		size := info.Size()
		canonicalName := name
		if info.IsDir() {
			mode = 0o755
			typeFlag = tar.TypeDir
			size = 0
			canonicalName += "/"
		} else if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		header := &tar.Header{
			Name:       canonicalName,
			Mode:       mode,
			Size:       size,
			ModTime:    canonicalTime,
			AccessTime: canonicalTime,
			ChangeTime: canonicalTime,
			Typeflag:   typeFlag,
			Format:     tar.FormatPAX,
		}
		if err := writer.WriteHeader(header); err != nil {
			return fmt.Errorf("write header %q: %w", name, err)
		}
		if info.IsDir() {
			continue
		}
		file, err := os.Open(fullPath)
		if err != nil {
			return fmt.Errorf("open %q: %w", name, err)
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("write %q: %w", name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %q: %w", name, closeErr)
		}
	}
	return nil
}

func (limits ArchiveLimits) withDefaults() ArchiveLimits {
	defaults := DefaultArchiveLimits()
	if limits.MaxCompressedBytes <= 0 {
		limits.MaxCompressedBytes = defaults.MaxCompressedBytes
	}
	if limits.MaxUncompressedBytes <= 0 {
		limits.MaxUncompressedBytes = defaults.MaxUncompressedBytes
	}
	if limits.MaxFileBytes <= 0 {
		limits.MaxFileBytes = defaults.MaxFileBytes
	}
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = defaults.MaxFiles
	}
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = defaults.MaxEntries
	}
	if limits.MaxPathBytes <= 0 {
		limits.MaxPathBytes = defaults.MaxPathBytes
	}
	if limits.MaxPathDepth <= 0 {
		limits.MaxPathDepth = defaults.MaxPathDepth
	}
	return limits
}
