package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/arthur-s/softpractice-cli/internal/localchecks"
	"github.com/arthur-s/softpractice-cli/internal/submission"
)

// All CLI/MCP check entry points use the same input preparation. History is
// never copied into the checked project or its immutable submission archive.
func runChecksWithInputs(ctx context.Context, client *learnercli.Client, projectRoot, checkRoot string, config localchecks.Config, output, errorOutput io.Writer) (result error) {
	// Without declared history the checks need no network, whatever the schema.
	if len(config.PriorRevisions) == 0 {
		return localchecks.Run(ctx, checkRoot, config, output, errorOutput)
	}
	if client == nil {
		return errors.New(text(ctx, "проверкам этого урока нужна история из SoftPractice API: проверьте адрес API и выполните softpractice login", "public checks of this lesson need historical inputs from the SoftPractice API; check the API address and run softpractice login"))
	}
	_, link, workspace, err := linkedWorkspace(ctx, client, projectRoot, false)
	if err != nil {
		return err
	}
	published, err := localchecks.Parse(workspace.Assignment.LocalChecks)
	if err != nil {
		return err
	}
	if pendingTransition(link, workspace) || pendingLessonVersion(link, workspace) || !reflect.DeepEqual(config.PriorRevisions, published.PriorRevisions) || config.SchemaVersion != published.SchemaVersion {
		return errors.New(text(ctx, "конфигурация проверок отличается от опубликованной для урока; выполните softpractice update", "local check inputs differ from the published lesson; apply softpractice update"))
	}
	if workspace.Workspace.BaseRevisionID == nil {
		return errors.New(text(ctx, "проверкам нужна принятая базовая сдача предыдущего урока", "public checks require an accepted base revision"))
	}
	base := *workspace.Workspace.BaseRevisionID
	root, err := os.MkdirTemp("", "softpractice-check-inputs-")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, removeCheckInputs(root)) }()
	for _, id := range config.PriorRevisions {
		if err := preparePriorInput(ctx, client, root, workspace, base, id); err != nil {
			return err
		}
	}
	before, err := inputDigests(root)
	if err != nil {
		return err
	}
	if err := localchecks.RunWithInputs(ctx, checkRoot, config, root, output, errorOutput); err != nil {
		return err
	}
	after, err := inputDigests(root)
	if err != nil || !reflect.DeepEqual(before, after) {
		return errors.New("public checks changed historical inputs; checks stopped")
	}
	current, err := client.GetWorkspaceStatus(ctx, link.WorkspaceID)
	if err != nil {
		return err
	}
	if current.Assignment.ID != workspace.Assignment.ID || current.Assignment.Version != workspace.Assignment.Version || current.Workspace.BaseRevisionID == nil || *current.Workspace.BaseRevisionID != base {
		return errors.New(text(ctx, "проект в SoftPractice изменился во время проверок; повторите их в текущем уроке", "workspace changed during local checks; retry from the current lesson"))
	}
	return ctx.Err()
}

func preparePriorInput(ctx context.Context, client *learnercli.Client, root string, workspace learnercli.WorkspaceStatus, base, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	archive, err := os.CreateTemp(root, "download-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	query := url.Values{"assignment_id": {workspace.Assignment.ID}, "assignment_version": {strconv.Itoa(workspace.Assignment.Version)}, "base_revision_id": {base}}
	path := "/v1/workspaces/" + url.PathEscape(workspace.Workspace.ID) + "/current-assignment/check-inputs/" + url.PathEscape(id) + "/archive?" + query.Encode()
	result, downloadErr := client.DownloadRevisionArchive(ctx, path, archive)
	closeErr := archive.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if downloadErr != nil {
		return downloadErr
	}
	if closeErr != nil {
		return closeErr
	}
	headers := result.Headers
	expected := map[string]string{"X-Softpractice-Assignment-ID": workspace.Assignment.ID, "X-Softpractice-Assignment-Version": strconv.Itoa(workspace.Assignment.Version), "X-Softpractice-Base-Revision-ID": base, "X-Softpractice-Prior-Assignment-ID": id, "Cache-Control": "no-store"}
	for key, value := range expected {
		if headers.Get(key) != value {
			return fmt.Errorf("historical input response has invalid %s", key)
		}
	}
	if headers.Get("X-Softpractice-Revision-ID") == "" {
		return errors.New("historical input response has no revision identity")
	}
	materialized, err := submission.MaterializeZIP(archive.Name(), root, submission.DefaultArchiveLimits())
	if err != nil {
		return err
	}
	defer os.RemoveAll(materialized.Path)
	if materialized.ContentSHA256 != headers.Get("X-Softpractice-Content-SHA256") {
		return errors.New("historical input hash does not match immutable metadata")
	}
	target := filepath.Join(root, id)
	if err := os.Mkdir(target, 0700); err != nil {
		return err
	}
	// Validate the entire ZIP before projecting data. No historical executable,
	// Python module, or configuration capable of loading code enters this tree.
	return filepath.WalkDir(materialized.Path, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(materialized.Path, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !historicalDataPath(relative) {
			return nil
		}
		destination := filepath.Join(target, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0400)
	})
}

func historicalDataPath(path string) bool {
	if path == "loading.json" || path == "sql/ingest.sql" {
		return true
	}
	for _, prefix := range []string{"sql/migrations/", "sql/queries/"} {
		if strings.HasPrefix(path, prefix) && !strings.Contains(strings.TrimPrefix(path, prefix), "/") && strings.HasSuffix(path, ".sql") {
			return true
		}
	}
	return false
}

func inputDigests(root string) (map[string][32]byte, error) {
	result := make(map[string][32]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("historical inputs contain a non-regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = sha256.Sum256(data)
		return nil
	})
	return result, err
}

// Windows also needs writable files for deletion, and a check may leave a
// read-only subdirectory whose entries cannot be unlinked. This applies only to
// the private directory created for this run, never to any project directory.
func removeCheckInputs(root string) error {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0700)
		}
		if entry.Type().IsRegular() {
			return os.Chmod(path, 0600)
		}
		return nil
	})
	return os.RemoveAll(root)
}
