package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type checkSnapshot struct {
	root  string
	files map[string][32]byte
}

// Extract the already-created submission archive. Never copy ignored files or
// add the working project to the snapshot's module search path.
func prepareCheckSnapshot(repository gitRepository) (_ checkSnapshot, err error) {
	root, err := os.MkdirTemp("", "softpractice-checks-")
	if err != nil {
		return checkSnapshot{}, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(root)
		}
	}()
	snapshot := checkSnapshot{root: root, files: make(map[string][32]byte)}
	archive, err := os.Open(repository.ArchivePath)
	if err != nil {
		return snapshot, err
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return snapshot, err
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	var total int64
	for {
		header, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return snapshot, readErr
		}
		// Git archives include directory entries, unlike starter bundles.
		if header.Typeflag == tar.TypeDir || header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if header.Typeflag != tar.TypeReg || !safeProjectRelativePath(header.Name) || header.Size < 0 || header.Size > 5<<20 || len(snapshot.files) >= 2000 {
			return snapshot, errors.New("submission snapshot contains an unsupported entry")
		}
		total += header.Size
		if total > 50<<20 {
			return snapshot, errors.New("submission snapshot exceeds size limit")
		}
		if _, exists := snapshot.files[header.Name]; exists {
			return snapshot, errors.New("submission snapshot repeats a file")
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return snapshot, err
		}
		path := filepath.Join(root, filepath.FromSlash(header.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return snapshot, err
		}
		if err := os.WriteFile(path, data, os.FileMode(header.Mode)&0777); err != nil {
			return snapshot, err
		}
		snapshot.files[header.Name] = sha256.Sum256(data)
	}
	return snapshot, nil
}

func (snapshot checkSnapshot) verify() error {
	root, err := os.OpenRoot(snapshot.root)
	if err != nil {
		return err
	}
	defer root.Close()
	for path, digest := range snapshot.files {
		info, err := root.Lstat(filepath.FromSlash(path))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("public checks changed submission snapshot file %q; submission stopped", path)
		}
		data, err := root.ReadFile(filepath.FromSlash(path))
		if err != nil || sha256.Sum256(data) != digest {
			return fmt.Errorf("public checks changed submission snapshot file %q; submission stopped", path)
		}
	}
	return nil
}
