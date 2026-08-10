package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type gitRepository struct {
	Root            string
	CommitSHA       string
	Clean           bool
	Files           []gitFile
	ArchivePath     string
	CompressedBytes int64
}

type gitFile struct {
	Path string
	Mode string
	Size int64
}

func inspectGitRepository(
	ctx context.Context,
	startDirectory string,
	createArchive bool,
) (gitRepository, error) {
	rootArguments := []string{"rev-parse", "--show-toplevel"}
	if startDirectory != "" {
		rootArguments = append([]string{"-C", startDirectory}, rootArguments...)
	}
	rootOutput, err := exec.CommandContext(ctx, "git", rootArguments...).Output()
	if err != nil {
		return gitRepository{}, errors.New("current directory is not a Git repository")
	}
	repository := gitRepository{Root: filepath.Clean(strings.TrimSpace(string(rootOutput)))}
	headOutput, err := gitOutput(ctx, repository.Root, "rev-parse", "HEAD")
	if err != nil {
		return gitRepository{}, errors.New("repository has no commit; create the initial commit first")
	}
	repository.CommitSHA = strings.TrimSpace(string(headOutput))
	clean, err := gitWorktreeIsClean(ctx, repository.Root)
	if err != nil {
		return gitRepository{}, err
	}
	repository.Clean = clean
	if !createArchive {
		return repository, nil
	}
	filesOutput, err := gitOutput(ctx, repository.Root, "ls-tree", "-r", "-l", "-z", "HEAD")
	if err != nil {
		return gitRepository{}, err
	}
	if err := readGitTree(&repository, filesOutput); err != nil {
		return gitRepository{}, err
	}
	if err := createGitArchive(ctx, &repository); err != nil {
		return gitRepository{}, err
	}
	return repository, nil
}

func gitWorktreeIsClean(ctx context.Context, root string) (bool, error) {
	statusOutput, err := gitOutput(ctx, root, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return false, err
	}
	return len(statusOutput) == 0, nil
}

func readGitTree(repository *gitRepository, output []byte) error {
	var totalBytes int64
	for _, record := range strings.Split(string(output), "\x00") {
		if record == "" {
			continue
		}
		file, err := parseGitTreeRecord(record)
		if err != nil {
			return err
		}
		totalBytes += file.Size
		repository.Files = append(repository.Files, file)
	}
	if len(repository.Files) == 0 {
		return errors.New("HEAD contains no files")
	}
	if len(repository.Files) > 2000 {
		return errors.New("HEAD exceeds the 2,000-file submission limit")
	}
	if totalBytes > 50<<20 {
		return errors.New("HEAD exceeds the 50 MiB uncompressed submission limit")
	}
	return nil
}

func parseGitTreeRecord(record string) (gitFile, error) {
	header, path, ok := strings.Cut(record, "\t")
	fields := strings.Fields(header)
	if !ok || len(fields) != 4 {
		return gitFile{}, errors.New("Git returned an invalid tree entry")
	}
	if fields[0] == "120000" {
		return gitFile{}, fmt.Errorf("symbolic link %q cannot be submitted", path)
	}
	if fields[0] == "160000" || fields[1] == "commit" {
		return gitFile{}, fmt.Errorf("Git submodule %q cannot be submitted", path)
	}
	if fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return gitFile{}, fmt.Errorf("unsupported Git entry %q with mode %s", path, fields[0])
	}
	size, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || size < 0 {
		return gitFile{}, fmt.Errorf("Git returned an invalid size for %q", path)
	}
	if !utf8.ValidString(path) || len([]byte(path)) > 240 ||
		strings.Count(path, "/")+1 > 20 {
		return gitFile{}, fmt.Errorf("Git path %q exceeds submission limits", path)
	}
	if size > 5<<20 {
		return gitFile{}, fmt.Errorf("file %q exceeds the 5 MiB limit", path)
	}
	return gitFile{Path: path, Mode: fields[0], Size: size}, nil
}

func createGitArchive(ctx context.Context, repository *gitRepository) error {
	archive, err := os.CreateTemp("", "softpractice-git-*.tar.gz")
	if err != nil {
		return err
	}
	repository.ArchivePath = archive.Name()
	if err := archive.Close(); err != nil {
		_ = os.Remove(repository.ArchivePath)
		return err
	}
	command := exec.CommandContext(
		ctx,
		"git",
		"-C",
		repository.Root,
		"archive",
		"--format=tar.gz",
		"--output",
		repository.ArchivePath,
		"HEAD",
	)
	if output, err := command.CombinedOutput(); err != nil {
		_ = os.Remove(repository.ArchivePath)
		return fmt.Errorf("create Git archive: %v: %s", err, output)
	}
	info, err := os.Stat(repository.ArchivePath)
	if err != nil {
		_ = os.Remove(repository.ArchivePath)
		return err
	}
	repository.CompressedBytes = info.Size()
	if repository.CompressedBytes > 10<<20 {
		_ = os.Remove(repository.ArchivePath)
		return errors.New("archive exceeds the 10 MiB compressed submission limit")
	}
	return nil
}

func gitOutput(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(arguments, " "), err)
	}
	return output, nil
}

func suspiciousPaths(files []gitFile) []string {
	var suspicious []string
	for _, file := range files {
		path := file.Path
		lower := strings.ToLower(path)
		base := filepath.Base(lower)
		if base == ".env" ||
			base == "id_rsa" ||
			base == "id_ed25519" ||
			strings.HasSuffix(lower, ".pem") ||
			strings.HasSuffix(lower, ".key") ||
			strings.HasSuffix(lower, ".p12") {
			suspicious = append(suspicious, path)
		}
	}
	return suspicious
}

func formatBytes(size int64) string {
	const (
		kib = 1024
		mib = 1024 * kib
	)
	switch {
	case size >= mib:
		return fmt.Sprintf("%.1f MiB", float64(size)/mib)
	case size >= kib:
		return fmt.Sprintf("%.1f KiB", float64(size)/kib)
	default:
		return fmt.Sprintf("%d B", size)
	}
}
