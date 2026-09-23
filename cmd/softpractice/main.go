package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/arthur-s/softpractice-cli/internal/localchecks"
	"github.com/arthur-s/softpractice-cli/internal/starterbundle"
	"github.com/arthur-s/softpractice-cli/internal/submission"
)

// developmentVersion is what a build carries when GoReleaser did not stamp it.
// The server accepts only a semantic version, so a binary holding this value
// can read the course but cannot submit to it.
const developmentVersion = "dev"

// cliVersion is replaced by GoReleaser for tagged releases.
var cliVersion = developmentVersion

// This namespace is part of retry identity and must remain stable across CLI releases.
var submissionNamespace = uuid.MustParse("8e57862c-98cf-4c24-af14-1541172e9a5f")

type currentUser struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	DisplayName      string     `json:"display_name"`
	EmailVerified    bool       `json:"email_verified"`
	CLILastSessionAt *time.Time `json:"cli_last_session_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

type submissionReceipt struct {
	LessonUpdate    *lessonUpdateNotice `json:"lesson_update"`
	SubmissionID    string              `json:"submission_id"`
	JobState        string              `json:"job_state"`
	Replayed        bool                `json:"replayed"`
	SubmissionURL   string              `json:"submission_url"`
	EvaluationURL   string              `json:"evaluation_url"`
	RevisionID      string              `json:"revision_id"`
	EvaluationJobID string              `json:"evaluation_job_id"`
	SubmittedAt     time.Time           `json:"submitted_at"`
}

type lessonUpdateNotice struct {
	AssignmentID     string `json:"assignment_id"`
	SubmittedVersion int    `json:"submitted_version"`
	CurrentVersion   int    `json:"current_version"`
}

// printVersion reports the version this binary was built with. A build made
// outside a release carries the placeholder, and the server refuses its
// submissions, so the placeholder says that here rather than letting the
// learner meet it for the first time as a rejected submit.
func printVersion(ctx context.Context, output io.Writer) {
	fmt.Fprintf(output, "softpractice %s\n", cliVersion)
	if cliVersion == developmentVersion {
		fmt.Fprintln(output, text(ctx,
			"Это сборка из исходников без версии: сервер отклонит отправку решения.\n"+
				"Соберите её с версией, например:\n"+
				`  go build -ldflags "-X main.cliVersion=0.1.5-dev" ./cmd/softpractice`,
			"This is a source build with no version: the server will refuse its submissions.\n"+
				"Build it with one, for example:\n"+
				`  go build -ldflags "-X main.cliVersion=0.1.5-dev" ./cmd/softpractice`))
	}
}

func commandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func main() {
	ctx, stop := commandContext()
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "softpractice:", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	input io.Reader,
	output, errorOutput io.Writer,
) error {
	config, err := defaultConfigStore()
	if err != nil {
		return err
	}
	// Help and `set` must remain available when a configuration or environment
	// override is invalid, otherwise a learner cannot discover or repair it.
	// A valid saved language is still used for their output.
	settings := displaySettings(config)
	root := flag.NewFlagSet("softpractice", flag.ContinueOnError)
	root.SetOutput(errorOutput)
	root.Usage = func() { printHelp(withSettings(ctx, settings), errorOutput) }
	apiURL := root.String("api", "", "API base URL")
	languageValue := root.String("lang", "", "CLI language: ru or en")
	showVersion := root.Bool("version", false, "print the CLI version and exit")
	if err := root.Parse(args); err != nil {
		return err
	}
	var languageOverride language
	if *languageValue != "" {
		languageOverride, err = parseLanguage(*languageValue)
		if err != nil {
			return err
		}
		settings.Language = languageOverride
	}
	ctx = withSettings(ctx, settings)
	remaining := root.Args()
	// The version answers before anything else, including the missing-command
	// help: it is what a bug report and a refused submission both ask for, and
	// it must work when nothing else does.
	if *showVersion || (len(remaining) == 1 && remaining[0] == "version") {
		printVersion(ctx, output)
		return nil
	}
	if len(remaining) == 0 {
		printHelp(ctx, output)
		return nil
	}
	if remaining[0] == "help" || remaining[0] == "--help" || remaining[0] == "-h" {
		if len(remaining) > 2 {
			return errors.New(text(ctx, "использование: softpractice help [команда]", "usage: softpractice help [command]"))
		}
		if len(remaining) == 2 {
			return printCommandHelp(ctx, output, remaining[1])
		}
		printHelp(ctx, output)
		return nil
	}
	if remaining[0] == "config" {
		if len(remaining) != 1 {
			return errors.New(text(ctx, "использование: softpractice config", "usage: softpractice config"))
		}
		settings, err = resolveSettings(config)
		if err != nil {
			return err
		}
		if *apiURL != "" {
			settings.APIURL = *apiURL
		}
		if *languageValue != "" {
			settings.Language = languageOverride
		}
		ctx = withSettings(ctx, settings)
		return showConfig(ctx, output)
	}
	if remaining[0] == "set" {
		return setConfig(ctx, remaining[1:], output)
	}
	settings, err = resolveSettings(config)
	if err != nil {
		return err
	}
	if *apiURL != "" {
		settings.APIURL = *apiURL
	}
	if *languageValue != "" {
		settings.Language = languageOverride
	}
	if err := validateRuntimeLanguage(settings.Language); err != nil {
		return err
	}
	if remaining[0] == "check" {
		return checkProject(ctx, "", remaining[1:], output, errorOutput)
	}
	if _, err := learnercli.NewClient(settings.APIURL, learnercli.CredentialStore{}); err != nil {
		return err
	}
	ctx = withSettings(ctx, settings)
	store, err := learnercli.DefaultCredentialStore()
	if err != nil {
		return err
	}
	client, err := learnercli.NewClient(settings.APIURL, store)
	if err != nil {
		return err
	}
	switch remaining[0] {
	case "login":
		return login(ctx, client, remaining[1:], output, errorOutput)
	case "logout":
		if len(remaining) != 1 {
			return errors.New("usage: softpractice logout")
		}
		if err := client.Logout(ctx); err != nil {
			return err
		}
		fmt.Fprintln(output, text(ctx, "Вход CLI завершён.", "CLI login revoked."))
		return nil
	case "status":
		return status(ctx, client, "", remaining[1:], output, errorOutput)
	case "submission":
		return submissionCommand(ctx, client, "", remaining[1:], output, errorOutput)
	case "project":
		return projectCommand(ctx, client, remaining[1:], output, errorOutput)
	case "starter":
		return downloadStarter(ctx, client, remaining[1:], output, errorOutput)
	case "update":
		return updateProject(ctx, client, remaining[1:], output, errorOutput)
	case "submit":
		return submit(ctx, client, "", remaining[1:], input, output, errorOutput)
	case "open":
		return openCurrent(ctx, client, "", remaining[1:], output, errorOutput)
	default:
		return fmt.Errorf(text(ctx, "неизвестная команда %q", "unknown command %q"), remaining[0])
	}
}

func projectCommand(
	ctx context.Context,
	client *learnercli.Client,
	args []string,
	output, errorOutput io.Writer,
) error {
	if len(args) == 0 || args[0] != "restore" {
		return errors.New(text(ctx,
			"использование: softpractice project restore [--practicum ID] [--directory PATH]",
			"usage: softpractice project restore [--practicum ID] [--directory PATH]"))
	}
	flags := flag.NewFlagSet("project restore", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	practicumID := flags.String("practicum", "", "started practicum ID")
	directory := flags.String("directory", "", "new destination directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(text(ctx,
			"использование: softpractice project restore [--practicum ID] [--directory PATH]",
			"usage: softpractice project restore [--practicum ID] [--directory PATH]"))
	}
	source, err := client.ResolveProjectRestoreSource(ctx, strings.TrimSpace(*practicumID))
	if err != nil {
		return fmt.Errorf(text(ctx, "подготовить восстановление проекта: %w", "prepare project restore: %w"), err)
	}
	target := strings.TrimSpace(*directory)
	if target == "" {
		target = source.ProjectID
	}
	if source.Kind == "starter" {
		if err := downloadStarterInto(
			ctx,
			client,
			source.WorkspaceID,
			source.ProjectID,
			source.AssignmentID,
			source.AssignmentVersion,
			target,
		); err != nil {
			return err
		}
	} else if source.Kind == "revision" {
		if err := restoreRevisionProject(ctx, client, source, target); err != nil {
			return err
		}
	} else {
		return errors.New("project restore source is invalid")
	}
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, text(ctx,
		"Связанный проект восстановлен: %s\n",
		"Linked project restored: %s\n"), absoluteTarget)
	fmt.Fprintln(output, text(ctx,
		"Проверьте `softpractice status` и продолжайте работу в этой папке.",
		"Run `softpractice status`, then continue working in this directory."))
	return nil
}

func restoreRevisionProject(
	ctx context.Context,
	client *learnercli.Client,
	source learnercli.ProjectRestoreSource,
	target string,
) error {
	target, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("destination %s already exists; choose a new directory", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination: %w", err)
	}
	parent := filepath.Dir(target)
	archive, err := os.CreateTemp("", "softpractice-project-restore-*.zip")
	if err != nil {
		return fmt.Errorf("create revision download: %w", err)
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	_, downloadErr := client.DownloadRevisionArchive(
		ctx,
		"/v1/submissions/"+source.SubmissionID+"/revision/archive",
		archive,
	)
	closeErr := archive.Close()
	if downloadErr != nil || closeErr != nil {
		return fmt.Errorf("download project revision: %w", errors.Join(downloadErr, closeErr))
	}
	materialized, err := submission.MaterializeZIP(
		archivePath,
		parent,
		submission.DefaultArchiveLimits(),
	)
	if err != nil {
		return fmt.Errorf("verify project revision: %w", err)
	}
	stage := materialized.Path
	defer os.RemoveAll(stage)
	workspace, err := client.GetWorkspaceStatus(ctx, source.WorkspaceID)
	if err != nil {
		return fmt.Errorf("load local checks for restored project: %w", err)
	}
	if err := writeLocalChecks(stage, workspace.Assignment.LocalChecks); err != nil {
		return err
	}
	link := learnercli.ProjectLink{
		SchemaVersion:     2,
		WorkspaceID:       source.WorkspaceID,
		ProjectID:         source.ProjectID,
		AssignmentID:      source.AssignmentID,
		AssignmentVersion: source.AssignmentVersion,
	}
	if err := initializeLinkedGit(ctx, stage, link, "Restore Softpractice submission"); err != nil {
		return err
	}
	if source.NeedsCourseUpdate {
		if err := updateLinkedProject(
			ctx,
			client,
			stage,
			source.ExpectedBaseRevisionID,
			io.Discard,
		); err != nil {
			return fmt.Errorf("update restored accepted revision: %w", err)
		}
	}
	repository, finalLink, workspace, err := linkedWorkspace(ctx, client, stage, false)
	if err != nil {
		return fmt.Errorf("verify restored project: %w", err)
	}
	if !repository.Clean || finalLink.AssignmentID != workspace.Assignment.ID ||
		finalLink.AssignmentVersion != workspace.Assignment.Version {
		return errors.New("restored project does not match the current workspace assignment")
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("place restored project: %w", err)
	}
	return nil
}

func showConfig(ctx context.Context, output io.Writer) error {
	settings := settingsFromContext(ctx)
	config, err := settings.Config.Load()
	if err != nil {
		return err
	}
	autoChecks := text(ctx,
		"недоступны (запустите команду в связанном проекте)",
		"not available (run this command inside a linked project)")
	if repository, repositoryErr := inspectGitRepository(ctx, "", false); repositoryErr == nil {
		if _, linkErr := learnercli.LoadProjectLink(repository.Root); linkErr == nil {
			enabled, checksErr := projectAutoChecks(ctx, repository.Root)
			if checksErr != nil {
				return checksErr
			}
			autoChecks = fmt.Sprintf("%t", enabled)
		}
	}
	fmt.Fprintf(output, text(ctx,
		"Конфигурация: %s\nAPI: %s%s\nWeb: %s%s\nЯзык: %s\nАвтопроверки: %s\n",
		"Configuration: %s\nAPI: %s%s\nWeb: %s%s\nLanguage: %s\nAuto-checks: %s\n"),
		settings.Config.Path,
		settings.APIURL, settingOverrideNotice(ctx, "api-url", config.APIURL),
		settings.WebURL, settingOverrideNotice(ctx, "web-url", config.WebURL),
		settings.Language,
		autoChecks)
	return nil
}

func setConfig(ctx context.Context, args []string, output io.Writer) error {
	if len(args) != 2 {
		return errors.New(text(ctx,
			"использование: softpractice set api-url|web-url|lang|auto-checks VALUE",
			"usage: softpractice set api-url|web-url|lang|auto-checks VALUE"))
	}
	if args[0] == "auto-checks" {
		return setProjectAutoChecks(ctx, args[1], output)
	}
	settings := settingsFromContext(ctx)
	config, replacedInvalidConfig, err := settings.Config.LoadForUpdate()
	if err != nil {
		return err
	}
	switch args[0] {
	case "api-url":
		if _, err := learnercli.NewClient(args[1], learnercli.CredentialStore{}); err != nil {
			return err
		}
		config.APIURL = args[1]
	case "web-url":
		if err := validateWebURL(args[1]); err != nil {
			return err
		}
		config.WebURL = args[1]
	case "lang":
		value, err := parseLanguage(args[1])
		if err != nil {
			return err
		}
		config.Language = string(value)
	default:
		return fmt.Errorf(text(ctx, "неизвестная настройка %q", "unknown setting %q"), args[0])
	}
	if err := settings.Config.Save(config); err != nil {
		return err
	}
	if replacedInvalidConfig {
		fmt.Fprintln(output, text(ctx,
			"Некорректная конфигурация заменена новой.",
			"The invalid configuration was replaced with a new one."))
	}
	fmt.Fprintf(output, text(ctx, "Настройка %s сохранена в %s\n", "Saved %s in %s\n"), args[0], settings.Config.Path)
	if variable := environmentVariableForSetting(args[0]); variable != "" {
		fmt.Fprintln(output, text(ctx,
			"Сохранённое значение сейчас перекрыто переменной окружения "+variable+". Удалите её из окружения, чтобы CLI использовал эту настройку.",
			"The saved value is currently overridden by "+variable+". Remove it from the environment for the CLI to use this setting."))
	}
	return nil
}

func environmentVariableForSetting(setting string) string {
	var name string
	switch setting {
	case "api-url":
		name = "SOFTPRACTICE_API_URL"
	case "web-url":
		name = "SOFTPRACTICE_WEB_URL"
	default:
		return ""
	}
	if strings.TrimSpace(os.Getenv(name)) == "" {
		return ""
	}
	return name
}

func downloadStarter(ctx context.Context, client *learnercli.Client, args []string, output, errorOutput io.Writer) error {
	flags := flag.NewFlagSet("starter", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	directory := flags.String("directory", "", "new destination directory")
	practicumID := flags.String("practicum", "", "started practicum ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(text(ctx, "использование: softpractice starter [--practicum PRACTICUM_ID] [--directory DIRECTORY]", "usage: softpractice starter [--practicum PRACTICUM_ID] [--directory DIRECTORY]"))
	}
	practicum, err := client.StartedPracticum(ctx, *practicumID)
	if err != nil {
		return err
	}
	if practicum.Workspace == nil {
		return errors.New(text(ctx, "сначала начните практикум в web-приложении, затем скачайте проект", "start a practicum in the web app before downloading its project"))
	}
	if practicum.CurrentAssignment == nil {
		return errors.New(text(ctx, "у начатого практикума нет текущего урока", "started practicum has no current assignment"))
	}
	target := *directory
	if target == "" {
		// The practicum's entry lesson begins the repository which the learner
		// carries through the course, so it is named after the project alone in
		// every practicum. Later independent starters retain their lesson suffix
		// so a fallback download cannot overwrite that ongoing project.
		target = practicum.Workspace.ProjectID
		if practicum.CurrentAssignment.ID != practicum.FirstAssignment.ID {
			target += "-" + practicum.CurrentAssignment.ID
		}
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("destination %s already exists; choose a new directory", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination: %w", err)
	}
	parent := filepath.Dir(target)
	stage, err := os.MkdirTemp(parent, ".softpractice-starter-")
	if err != nil {
		return fmt.Errorf("create starter staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	archive, err := os.CreateTemp("", "softpractice-starter-download-*.tar.gz")
	if err != nil {
		return err
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	metadata, downloadErr := client.DownloadStarter(
		ctx,
		"/v1/workspaces/"+practicum.Workspace.ID+"/current-assignment/starter?format=tar.gz",
		archive,
	)
	closeErr := archive.Close()
	if downloadErr != nil || closeErr != nil {
		return fmt.Errorf("download starter project: %w", errors.Join(downloadErr, closeErr))
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	extractErr := starterbundle.Extract(file, stage, metadata.SHA256, metadata.Size)
	closeErr = file.Close()
	if extractErr != nil || closeErr != nil {
		return fmt.Errorf("verify starter project: %w", errors.Join(extractErr, closeErr))
	}
	if err := writeLocalChecks(stage, practicum.CurrentAssignment.LocalChecks); err != nil {
		return err
	}
	if err := initializeStarterGit(ctx, stage, learnercli.ProjectLink{
		SchemaVersion:     2,
		WorkspaceID:       practicum.Workspace.ID,
		ProjectID:         practicum.Workspace.ProjectID,
		AssignmentID:      practicum.CurrentAssignment.ID,
		AssignmentVersion: practicum.CurrentAssignment.Version,
	}); err != nil {
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("place starter project: %w", err)
	}
	fmt.Fprintf(output, text(ctx, "Проект starter создан: %s\n", "Starter project created: %s\n"), target)
	fmt.Fprintln(output, text(ctx,
		"Локальный базовый Git commit готов. Это новый проект: не изменяйте прежнюю папку урока. Внесите изменения, проверьте `git diff`, сделайте commit и выполните `softpractice submit`.",
		"A local Git baseline commit is ready. This starter is a new local project; keep any previous lesson folder unchanged. Edit, inspect `git diff`, commit, then run `softpractice submit`."))
	return nil
}

func initializeStarterGit(ctx context.Context, root string, link learnercli.ProjectLink) error {
	return initializeLinkedGit(ctx, root, link, "Initial Softpractice starter")
}

func initializeLinkedGit(
	ctx context.Context,
	root string,
	link learnercli.ProjectLink,
	commitMessage string,
) error {
	if output, err := exec.CommandContext(ctx, "git", "-C", root, "init", "--initial-branch=main").CombinedOutput(); err != nil {
		return fmt.Errorf("initialize local Git repository: %v: %s", err, output)
	}
	gitDirectory, err := gitOutput(ctx, root, "rev-parse", "--git-dir")
	if err != nil {
		return err
	}
	excludePath := filepath.Join(root, strings.TrimSpace(string(gitDirectory)), "info", "exclude")
	if err := os.WriteFile(excludePath, []byte(".softpractice/\n"), 0o600); err != nil {
		return fmt.Errorf("write Git local exclude: %w", err)
	}
	linkDirectory := filepath.Join(root, ".softpractice")
	if err := os.MkdirAll(linkDirectory, 0o700); err != nil {
		return fmt.Errorf("create project link directory: %w", err)
	}
	linkBytes := []byte(fmt.Sprintf(
		"{\n  \"schema_version\": %d,\n  \"workspace_id\": %q,\n  \"project_id\": %q,\n  \"assignment_id\": %q,\n  \"assignment_version\": %d\n}\n",
		link.SchemaVersion,
		link.WorkspaceID,
		link.ProjectID,
		link.AssignmentID,
		link.AssignmentVersion,
	))
	if err := os.WriteFile(filepath.Join(linkDirectory, "project.json"), linkBytes, 0o600); err != nil {
		return fmt.Errorf("write project link: %w", err)
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", root, "add", "--all").CombinedOutput(); err != nil {
		return fmt.Errorf("stage starter baseline: %v: %s", err, output)
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", root, "add", "--force", "--", localchecks.RelativePath).CombinedOutput(); err != nil {
		return fmt.Errorf("stage local checks: %v: %s", err, output)
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", root,
		"-c", "user.name=Softpractice", "-c", "user.email=starter@softpractice.invalid",
		"commit", "--no-verify", "-m", commitMessage).CombinedOutput(); err != nil {
		return fmt.Errorf("commit starter baseline: %v: %s", err, output)
	}
	return nil
}

// updateProject applies the server-published, manifest-verified transition to
// the current repository. It never downloads the next starter tree: that
// would overwrite learner work from the previous lesson.
func updateProject(ctx context.Context, client *learnercli.Client, args []string, output, errorOutput io.Writer) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(text(ctx, "использование: softpractice update", "usage: softpractice update"))
	}
	return updateLinkedProject(ctx, client, "", "", output)
}

func updateLinkedProject(
	ctx context.Context,
	client *learnercli.Client,
	startDirectory string,
	expectedBaseRevisionID string,
	output io.Writer,
) error {
	repository, link, workspace, err := linkedWorkspace(ctx, client, startDirectory, true)
	if err != nil {
		return err
	}
	defer os.Remove(repository.ArchivePath)
	if !repository.Clean {
		return errors.New(text(ctx, "рабочее дерево не чистое; сделайте commit или stash перед обновлением проекта", "working tree is not clean; commit or stash changes before updating the course project"))
	}
	if workspace.Workspace.State != "active" {
		return fmt.Errorf(text(ctx, "workspace находится в состоянии %s и не имеет обновления курса", "workspace is %s and has no course update"), workspace.Workspace.State)
	}
	if pendingLessonVersion(link, workspace) {
		return updateLessonVersion(ctx, client, repository.Root, link, workspace, output)
	}
	if link.SchemaVersion == 2 && workspace.Assignment.ID == link.AssignmentID && workspace.Assignment.Version == link.AssignmentVersion {
		checksPath := filepath.Join(repository.Root, filepath.FromSlash(localchecks.RelativePath))
		if _, statErr := os.Stat(checksPath); errors.Is(statErr, os.ErrNotExist) {
			if err := writeLocalChecks(repository.Root, workspace.Assignment.LocalChecks); err != nil {
				return err
			}
			if err := commitLocalChecks(ctx, repository.Root, "Add Softpractice public checks"); err != nil {
				return err
			}
			fmt.Fprintln(output, text(ctx,
				"Конфигурация публичных проверок добавлена в проект. Теперь повторите `softpractice submit`.",
				"Public checks configuration was added to the project. Now run `softpractice submit` again."))
			return nil
		} else if statErr != nil {
			return fmt.Errorf("inspect local checks: %w", statErr)
		}
		return fmt.Errorf(text(ctx, "урок %s v%d всё ещё текущий; отправьте решение и дождитесь принятого результата перед обновлением", "lesson %s v%d is still current; submit and receive an accepted result before updating"), link.AssignmentID, link.AssignmentVersion)
	}
	update, err := client.PrepareCourseUpdate(ctx, link.WorkspaceID)
	if err != nil {
		return fmt.Errorf("prepare course update: %w", err)
	}
	// The transition may start from a newer version of the lesson this folder
	// holds: the lesson was republished after this tree was made, and the tree
	// was accepted on its own version. The server offers that only when the
	// transition itself replaces every lesson file the republication changed;
	// the base content check below still pins the exact accepted tree.
	if (link.SchemaVersion == 2 && (update.FromAssignmentID != link.AssignmentID || update.FromAssignmentVersion < link.AssignmentVersion)) ||
		update.ToAssignmentID != workspace.Assignment.ID || update.ToAssignmentVersion != workspace.Assignment.Version {
		return errors.New(text(ctx, "сервер вернул обновление для другого урока; проект не изменён", "server returned a course update for a different lesson; project was left unchanged"))
	}
	if expectedBaseRevisionID != "" && update.BaseRevisionID != expectedBaseRevisionID {
		return errors.New("workspace base revision changed while restoring the project; retry")
	}
	normalized, err := submission.NormalizeTarGz(repository.ArchivePath, os.TempDir(), submission.DefaultArchiveLimits())
	if err != nil {
		return fmt.Errorf("verify local project content: %w", err)
	}
	defer os.Remove(normalized.Path)
	if normalized.ContentSHA256 != update.BaseContentSHA256 {
		// A non-empty startDirectory means this is the staging copy driven by
		// `project restore`, not a folder the learner is working in.
		return courseUpdateMismatchError(ctx, repository, update, startDirectory != "")
	}
	archivePath := "/v1/workspaces/" + link.WorkspaceID + "/current-assignment/course-update/archive?format=tar.gz"
	if err := downloadAndApplyCourseUpdate(ctx, client, repository.Root, link, update, workspace.Assignment.LocalChecks, archivePath); err != nil {
		return err
	}
	fmt.Fprintf(output, text(ctx, "Проект курса обновлён в этой папке: %s\n", "Course project updated in place: %s\n"), repository.Root)
	fmt.Fprintln(output, text(ctx,
		"Принятая предыдущая ревизия осталась в истории Git. Просмотрите изменения урока и продолжайте с `softpractice submit`.",
		"The accepted previous revision remains in Git history. Review the lesson changes, then continue with `softpractice submit`."))
	return nil
}

func downloadAndApplyCourseUpdate(
	ctx context.Context,
	client *learnercli.Client,
	repositoryRoot string,
	link learnercli.ProjectLink,
	update learnercli.CourseUpdate,
	localChecks []byte,
	archiveURLPath string,
) error {
	archive, err := os.CreateTemp("", "softpractice-course-update-*.tar.gz")
	if err != nil {
		return err
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	metadata, downloadErr := client.DownloadCourseUpdate(ctx, archiveURLPath, archive)
	closeErr := archive.Close()
	if downloadErr != nil || closeErr != nil {
		return fmt.Errorf("download course update: %w", errors.Join(downloadErr, closeErr))
	}
	if metadata.SHA256 != update.ArchiveSHA256 || metadata.Size != update.ArchiveSize || metadata.Ref != update.Ref ||
		metadata.BaseRevisionID != update.BaseRevisionID || metadata.BaseContentSHA256 != update.BaseContentSHA256 {
		return errors.New("course update archive metadata does not match the prepared update")
	}
	stage, err := os.MkdirTemp("", "softpractice-course-update-")
	if err != nil {
		return fmt.Errorf("create course-update staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	extractErr := starterbundle.Extract(file, stage, metadata.SHA256, metadata.Size)
	closeErr = file.Close()
	if extractErr != nil || closeErr != nil {
		return fmt.Errorf("verify course update: %w", errors.Join(extractErr, closeErr))
	}
	if err := verifyCourseUpdateFiles(stage, update); err != nil {
		return err
	}
	clean, err := gitWorktreeIsClean(ctx, repositoryRoot)
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("working tree changed while the course update was downloading; review and commit or stash those changes before retrying")
	}
	// Prepare both metadata files before touching the student's project, then
	// apply them under the same rollback as the lesson files.
	updatedLink := link
	updatedLink.SchemaVersion = 2
	updatedLink.AssignmentID = update.ToAssignmentID
	updatedLink.AssignmentVersion = update.ToAssignmentVersion
	if err := writeLocalChecks(stage, localChecks); err != nil {
		return err
	}
	if err := writeProjectLink(stage, updatedLink); err != nil {
		return err
	}
	application := update
	application.Files = slices.Clone(application.Files)
	application.Operations = slices.Clone(application.Operations)
	for _, path := range []string{localchecks.RelativePath, ".softpractice/project.json"} {
		for _, op := range update.Operations {
			if op.Path == path {
				return fmt.Errorf("course update cannot supply CLI metadata %q", path)
			}
		}
		kind := "replace"
		if _, err := os.Lstat(filepath.Join(repositoryRoot, filepath.FromSlash(path))); errors.Is(err, os.ErrNotExist) {
			kind = "add"
		} else if err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(stage, filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		application.Files = append(application.Files, struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}{Path: path, SHA256: hex.EncodeToString(digest[:])})
		application.Operations = append(application.Operations, struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}{Kind: kind, Path: path})
	}
	if err := applyCourseUpdate(ctx, repositoryRoot, stage, application); err != nil {
		return err
	}
	updatePaths := make([]string, 0, len(update.Operations))
	for _, operation := range update.Operations {
		updatePaths = append(updatePaths, operation.Path)
	}
	gitAddArguments := append([]string{"-C", repositoryRoot, "add", "--"}, updatePaths...)
	if gitOutput, err := exec.CommandContext(ctx, "git", gitAddArguments...).CombinedOutput(); err != nil {
		return fmt.Errorf("stage course update: %v: %s", err, gitOutput)
	}
	if gitOutput, err := exec.CommandContext(ctx, "git", "-C", repositoryRoot, "add", "--force", "--", localchecks.RelativePath).CombinedOutput(); err != nil {
		return fmt.Errorf("stage local checks update: %v: %s", err, gitOutput)
	}
	if gitOutput, err := exec.CommandContext(ctx, "git", "-C", repositoryRoot,
		"-c", "user.name=Softpractice", "-c", "user.email=starter@softpractice.invalid",
		"commit", "--no-verify", "-m", "Apply Softpractice update: "+update.Ref).CombinedOutput(); err != nil {
		return fmt.Errorf("commit course update: %v: %s", err, gitOutput)
	}
	return nil
}

func verifyCourseUpdateFiles(stage string, update learnercli.CourseUpdate) error {
	expected := make(map[string]string, len(update.Files))
	for _, file := range update.Files {
		if !safeProjectRelativePath(file.Path) || len(file.SHA256) != 64 {
			return errors.New("prepared course update has an invalid file manifest")
		}
		if _, duplicate := expected[file.Path]; duplicate {
			return errors.New("prepared course update repeats a file manifest entry")
		}
		expected[file.Path] = file.SHA256
	}
	if err := verifyCourseUpdateOperations(update, expected); err != nil {
		return err
	}
	actual := make(map[string]struct{}, len(expected))
	err := filepath.WalkDir(stage, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == stage || entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("course update contains a non-regular file")
		}
		relative, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		want, found := expected[name]
		if !found {
			return errors.New("course update contains a file outside its manifest")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != want {
			return fmt.Errorf("course update file %q does not match its manifest", name)
		}
		actual[name] = struct{}{}
		return nil
	})
	if err != nil {
		return fmt.Errorf("verify course update files: %w", err)
	}
	if len(actual) != len(expected) {
		return errors.New("course update is missing a manifest file")
	}
	return nil
}

func verifyCourseUpdateOperations(update learnercli.CourseUpdate, expected map[string]string) error {
	seen := make(map[string]struct{}, len(update.Operations))
	for _, operation := range update.Operations {
		if operation.Kind != "add" && operation.Kind != "replace" && operation.Kind != "delete" {
			return errors.New("prepared course update has an unsupported operation")
		}
		if !safeProjectRelativePath(operation.Path) {
			return errors.New("prepared course update has an unsafe path")
		}
		if _, duplicate := seen[operation.Path]; duplicate {
			return errors.New("prepared course update repeats an operation")
		}
		_, hasPayload := expected[operation.Path]
		if operation.Kind == "delete" && hasPayload {
			return errors.New("prepared course update delete operation has a manifest entry")
		}
		if operation.Kind != "delete" && !hasPayload {
			return errors.New("prepared course update operation has no manifest entry")
		}
		seen[operation.Path] = struct{}{}
	}
	for path := range expected {
		if _, found := seen[path]; !found {
			return errors.New("prepared course update manifest file has no operation")
		}
	}
	return nil
}

func applyCourseUpdate(ctx context.Context, repositoryRoot, stage string, update learnercli.CourseUpdate) (err error) {
	return applyCourseUpdateWithWriter(ctx, repositoryRoot, stage, update,
		func(root *os.Root, relative string, data []byte, mode os.FileMode) error {
			return root.WriteFile(filepath.FromSlash(relative), data, mode)
		})
}

func applyCourseUpdateWithWriter(
	ctx context.Context,
	repositoryRoot, stage string,
	update learnercli.CourseUpdate,
	writeFile func(*os.Root, string, []byte, os.FileMode) error,
) error {
	return applyCourseUpdateWithFilesystem(ctx, repositoryRoot, stage, update, writeFile,
		func(root *os.Root, relative string, mode os.FileMode) error {
			return root.MkdirAll(relative, mode)
		})
}

func applyCourseUpdateWithFilesystem(
	ctx context.Context,
	repositoryRoot, stage string,
	update learnercli.CourseUpdate,
	writeFile func(*os.Root, string, []byte, os.FileMode) error,
	mkdirAll func(*os.Root, string, os.FileMode) error,
) (err error) {
	type replacedFile struct {
		path string
		data []byte
		mode os.FileMode
	}
	expected := make(map[string]string, len(update.Files))
	for _, file := range update.Files {
		if _, duplicate := expected[file.Path]; duplicate {
			return errors.New("prepared course update repeats a file manifest entry")
		}
		expected[file.Path] = file.SHA256
	}
	if err := verifyCourseUpdateOperations(update, expected); err != nil {
		return err
	}
	projectRoot, err := os.OpenRoot(repositoryRoot)
	if err != nil {
		return fmt.Errorf("open course update project root: %w", err)
	}
	defer projectRoot.Close()
	for _, operation := range update.Operations {
		if err := verifyCourseUpdateDestination(repositoryRoot, operation.Path); err != nil {
			return err
		}
		info, statErr := projectRoot.Lstat(filepath.FromSlash(operation.Path))
		if operation.Kind == "add" && statErr == nil {
			return fmt.Errorf("course update would overwrite existing file %q", operation.Path)
		}
		if operation.Kind == "add" && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if operation.Kind == "replace" && (statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("course update can replace only the existing regular file %q", operation.Path)
		}
		if operation.Kind == "delete" && (statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("course update can delete only the existing regular file %q", operation.Path)
		}
	}
	// Порядок отката зеркалит канонический builder: добавленное снимается в
	// обратном порядке, затем восстанавливается заменённое и удалённое, затем
	// исчезают созданные каталоги. Map дал бы недетерминированный обход, и две
	// половины одного контракта разошлись бы там, где conformance-векторы
	// как раз и обязаны их удерживать вместе.
	var replaced []replacedFile
	var added, createdDirs []string
	defer func() {
		if err == nil {
			return
		}
		var rollbackErrors []error
		for index := len(added) - 1; index >= 0; index-- {
			path := added[index]
			if rollbackErr := projectRoot.Remove(filepath.FromSlash(path)); rollbackErr != nil && !errors.Is(rollbackErr, os.ErrNotExist) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove added course update file %s: %w", path, rollbackErr))
			}
		}
		for index := len(replaced) - 1; index >= 0; index-- {
			prior := replaced[index]
			path := prior.path
			name := filepath.FromSlash(path)
			if rollbackErr := projectRoot.WriteFile(name, prior.data, prior.mode.Perm()); rollbackErr != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore course update file %s: %w", path, rollbackErr))
				continue
			}
			if rollbackErr := projectRoot.Chmod(name, prior.mode.Perm()); rollbackErr != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore course update mode %s: %w", path, rollbackErr))
			}
		}
		for index := len(createdDirs) - 1; index >= 0; index-- {
			path := createdDirs[index]
			if rollbackErr := projectRoot.Remove(filepath.FromSlash(path)); rollbackErr != nil && !errors.Is(rollbackErr, os.ErrNotExist) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove created course update directory %s: %w", path, rollbackErr))
			}
		}
		err = errors.Join(append([]error{err}, rollbackErrors...)...)
	}()
	for _, operation := range update.Operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := verifyCourseUpdateDestination(repositoryRoot, operation.Path); err != nil {
			return fmt.Errorf("course update destination changed during application: %w", err)
		}
		name := filepath.FromSlash(operation.Path)
		info, statErr := projectRoot.Lstat(name)
		if operation.Kind == "add" && statErr == nil {
			return fmt.Errorf("course update destination changed during application: %q", operation.Path)
		}
		if operation.Kind == "add" && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if operation.Kind == "replace" && (statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("course update destination changed during application: %q", operation.Path)
		}
		if operation.Kind == "delete" && (statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("course update destination changed during application: %q", operation.Path)
		}
		if operation.Kind == "delete" {
			prior, readErr := projectRoot.ReadFile(name)
			if readErr != nil {
				return readErr
			}
			replaced = append(replaced, replacedFile{path: operation.Path, data: prior, mode: info.Mode()})
			if removeErr := projectRoot.Remove(name); removeErr != nil {
				return removeErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			continue
		}
		source := filepath.Join(stage, name)
		data, readErr := os.ReadFile(source)
		if readErr != nil {
			return readErr
		}
		if operation.Kind == "replace" {
			prior, readErr := projectRoot.ReadFile(name)
			if readErr != nil {
				return readErr
			}
			replaced = append(replaced, replacedFile{path: operation.Path, data: prior, mode: info.Mode()})
		} else {
			missingDirs, err := missingCourseUpdateDirectories(projectRoot, filepath.Dir(name))
			if err != nil {
				return err
			}
			// MkdirAll may create an initial prefix before returning an error, so
			// rollback must know the complete precomputed set before the call.
			createdDirs = append(createdDirs, missingDirs...)
			if mkdirErr := mkdirAll(projectRoot, filepath.Dir(name), 0o755); mkdirErr != nil {
				return mkdirErr
			}
			added = append(added, operation.Path)
		}
		if writeErr := writeFile(projectRoot, operation.Path, data, 0o644); writeErr != nil {
			return writeErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func missingCourseUpdateDirectories(root *os.Root, parent string) ([]string, error) {
	if parent == "." || parent == "" {
		return nil, nil
	}
	parts := strings.Split(filepath.ToSlash(parent), "/")
	missing := make([]string, 0, len(parts))
	for index := range parts {
		relative := strings.Join(parts[:index+1], "/")
		info, err := root.Lstat(filepath.FromSlash(relative))
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, relative)
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("course update path parent %q is not a regular directory", relative)
		}
	}
	return missing, nil
}

func verifyCourseUpdateDestination(root, relative string) error {
	current := root
	parts := strings.Split(relative, "/")
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("course update path parent %q is not a regular directory", relative)
		}
	}
	return nil
}

func safeProjectRelativePath(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	if !filepath.IsLocal(filepath.FromSlash(value)) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// updateLessonVersion moves this folder from a retired version of its lesson
// to the version that replaced it. The update replaces only author-owned files
// the bump changed and never the learner's work. It is voluntary: a folder on
// the retired version can still submit until the lesson ends.
func updateLessonVersion(
	ctx context.Context,
	client *learnercli.Client,
	root string,
	link learnercli.ProjectLink,
	workspace learnercli.WorkspaceStatus,
	output io.Writer,
) error {
	update, err := client.LessonVersionUpdate(ctx, link.WorkspaceID, link.AssignmentVersion)
	if err != nil {
		return fmt.Errorf("prepare lesson version update: %w", err)
	}
	if update.AssignmentID != link.AssignmentID || update.FromVersion != link.AssignmentVersion ||
		update.ToVersion != workspace.Assignment.Version {
		return errors.New(text(ctx, "сервер вернул обновление для другого урока; проект не изменён", "server returned a course update for a different lesson; project was left unchanged"))
	}
	if len(update.Operations) == 0 {
		return refreshLessonVersion(ctx, root, link, workspace, output)
	}
	replaced := make([]string, 0, len(update.Operations))
	for _, operation := range update.Operations {
		if operation.Kind != "replace" {
			return errors.New(text(ctx, "обновление урока может только заменять файлы урока; проект не изменён", "a lesson update may only replace lesson files; project was left unchanged"))
		}
		replaced = append(replaced, operation.Path)
	}
	courseUpdate := learnercli.CourseUpdate{
		Ref:              update.Ref,
		FromAssignmentID: update.AssignmentID, FromAssignmentVersion: update.FromVersion,
		ToAssignmentID: update.AssignmentID, ToAssignmentVersion: update.ToVersion,
		ArchiveSHA256: update.ArchiveSHA256, ArchiveSize: update.ArchiveSize,
		Files: update.Files, Operations: update.Operations,
	}
	archivePath := fmt.Sprintf("/v1/workspaces/%s/current-assignment/lesson-version-update/archive?from_version=%d&format=tar.gz",
		link.WorkspaceID, link.AssignmentVersion)
	if err := downloadAndApplyCourseUpdate(ctx, client, root, link, courseUpdate, workspace.Assignment.LocalChecks, archivePath); err != nil {
		return err
	}
	fmt.Fprintf(output,
		text(ctx, "Урок %s обновлён: v%d → v%d. Заменены файлы урока: %s. Ваши файлы не изменены; посмотрите обновлённое задание и продолжайте.\n",
			"Lesson %s updated: v%d → v%d. Lesson files replaced: %s. Your files are unchanged; review the updated assignment and continue.\n"),
		link.AssignmentID, update.FromVersion, update.ToVersion, strings.Join(replaced, ", "))
	return nil
}

// refreshLessonVersion moves this folder onto the republished version of the
// lesson it already holds when the bump changed no project file. There is no
// archive to apply: the learner's work stays exactly as it is and only the pin
// and the public checks are rewritten.
func refreshLessonVersion(
	ctx context.Context,
	root string,
	link learnercli.ProjectLink,
	workspace learnercli.WorkspaceStatus,
	output io.Writer,
) error {
	previousVersion := link.AssignmentVersion
	link.AssignmentVersion = workspace.Assignment.Version
	if err := writeLocalChecks(root, workspace.Assignment.LocalChecks); err != nil {
		return err
	}
	if err := writeProjectLink(root, link); err != nil {
		return err
	}
	// The pin itself is not tracked by Git. The public checks are, and a purely
	// editorial revision often leaves them byte-identical, so commit only when
	// this rewrite actually changed the working tree.
	clean, err := gitWorktreeIsClean(ctx, root)
	if err != nil {
		return err
	}
	if !clean {
		if err := commitLocalChecks(ctx, root, "Update Softpractice lesson version"); err != nil {
			return err
		}
	}
	fmt.Fprintf(
		output,
		text(ctx, "Урок %s обновлён: v%d → v%d. Ваши файлы не изменены; посмотрите обновлённое задание и продолжайте.\n",
			"Lesson %s updated: v%d → v%d. Your files are unchanged; review the updated assignment and continue.\n"),
		link.AssignmentID, previousVersion, link.AssignmentVersion,
	)
	return nil
}

func writeProjectLink(root string, link learnercli.ProjectLink) error {
	path := filepath.Join(root, ".softpractice", "project.json")
	data := []byte(fmt.Sprintf("{\n  \"schema_version\": %d,\n  \"workspace_id\": %q,\n  \"project_id\": %q,\n  \"assignment_id\": %q,\n  \"assignment_version\": %d\n}\n", link.SchemaVersion, link.WorkspaceID, link.ProjectID, link.AssignmentID, link.AssignmentVersion))
	return os.WriteFile(path, data, 0o600)
}

func writeLocalChecks(root string, raw []byte) error {
	config, err := localchecks.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid local checks received from server: %w", err)
	}
	body, err := localchecks.Marshal(config)
	if err != nil {
		return err
	}
	directory := filepath.Join(root, ".softpractice")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create local checks directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(localchecks.RelativePath)), body, 0o600); err != nil {
		return fmt.Errorf("write local checks: %w", err)
	}
	return nil
}

func commitLocalChecks(ctx context.Context, root, message string) error {
	if output, err := exec.CommandContext(ctx, "git", "-C", root, "add", "--force", "--", localchecks.RelativePath).CombinedOutput(); err != nil {
		return fmt.Errorf("stage local checks: %v: %s", err, output)
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", root,
		"-c", "user.name=Softpractice", "-c", "user.email=starter@softpractice.invalid",
		"commit", "--no-verify", "-m", message).CombinedOutput(); err != nil {
		return fmt.Errorf("commit local checks: %v: %s", err, output)
	}
	return nil
}

func downloadStarterInto(
	ctx context.Context,
	client *learnercli.Client,
	workspaceID, projectID, assignmentID string,
	assignmentVersion int,
	target string,
) error {
	workspace, err := client.GetWorkspaceStatus(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("load starter checks: %w", err)
	}
	if workspace.Assignment.ID != assignmentID || workspace.Assignment.Version != assignmentVersion {
		return errors.New("assignment changed while preparing the starter; retry")
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("destination %s already exists; choose a new directory", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination: %w", err)
	}
	parent := filepath.Dir(target)
	stage, err := os.MkdirTemp(parent, ".softpractice-starter-")
	if err != nil {
		return fmt.Errorf("create starter staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	archive, err := os.CreateTemp("", "softpractice-starter-download-*.tar.gz")
	if err != nil {
		return err
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	metadata, downloadErr := client.DownloadStarter(
		ctx,
		"/v1/workspaces/"+workspaceID+"/current-assignment/starter?format=tar.gz",
		archive,
	)
	closeErr := archive.Close()
	if downloadErr != nil || closeErr != nil {
		return fmt.Errorf("download starter project: %w", errors.Join(downloadErr, closeErr))
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	extractErr := starterbundle.Extract(file, stage, metadata.SHA256, metadata.Size)
	closeErr = file.Close()
	if extractErr != nil || closeErr != nil {
		return fmt.Errorf("verify starter project: %w", errors.Join(extractErr, closeErr))
	}
	if err := writeLocalChecks(stage, workspace.Assignment.LocalChecks); err != nil {
		return err
	}
	if err := initializeStarterGit(ctx, stage, learnercli.ProjectLink{
		SchemaVersion:     2,
		WorkspaceID:       workspaceID,
		ProjectID:         projectID,
		AssignmentID:      assignmentID,
		AssignmentVersion: assignmentVersion,
	}); err != nil {
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("place starter project: %w", err)
	}
	return nil
}

func login(
	ctx context.Context,
	client *learnercli.Client,
	args []string,
	output, errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	noBrowser := flags.Bool("no-browser", false, "print the verification URL without opening it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(text(ctx, "использование: softpractice login [--no-browser]", "usage: softpractice login [--no-browser]"))
	}
	authorization, err := client.StartLogin(ctx, "softpractice-cli/"+cliVersion)
	if err != nil {
		return err
	}
	fmt.Fprintf(
		output,
		text(ctx, "Откройте %s\nКод: %s\n", "Open %s\nCode: %s\n"),
		authorization.VerificationURIComplete,
		authorization.UserCode,
	)
	if !*noBrowser {
		if err := openBrowser(authorization.VerificationURIComplete); err != nil {
			fmt.Fprintf(errorOutput, text(ctx, "Не удалось открыть браузер: %v\n", "Could not open the browser: %v\n"), err)
		}
	}
	credentials, err := client.PollLogin(
		ctx,
		authorization,
		func(interval time.Duration) error {
			fmt.Fprintf(output, text(ctx, "Ожидаем подтверждение; следующая проверка через %s...\n", "Waiting for approval; next poll in %s...\n"), interval)
			return nil
		},
	)
	if err != nil {
		return err
	}
	if err := client.SaveCredentials(credentials); err != nil {
		return err
	}
	fmt.Fprintln(output, text(ctx, "Вход выполнен. Refresh-токен сохранён в системном хранилище ключей.", "Logged in. The refresh token is stored in the system keyring."))
	return nil
}

func status(
	ctx context.Context,
	client *learnercli.Client,
	startDirectory string,
	args []string,
	output, errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: softpractice status [--format text|json]")
	}
	outputFormat, err := parseOutputFormat(*format)
	if err != nil {
		return err
	}
	repository, link, workspace, err := linkedWorkspace(ctx, client, startDirectory, false)
	if err != nil {
		return err
	}
	var user currentUser
	if err := client.AuthorizedJSON(ctx, "GET", "/v1/me", nil, &user); err != nil {
		return err
	}
	if outputFormat == outputFormatJSON {
		return writeStatusJSON(output, user, link, workspace, repository)
	}
	clean := text(ctx, "чистое", "clean")
	if !repository.Clean {
		clean = text(ctx, "есть незакоммиченные или неотслеживаемые изменения", "has uncommitted or untracked changes")
	}
	fmt.Fprintf(output, text(ctx, "Пользователь: %s <%s>\n", "User: %s <%s>\n"), user.DisplayName, user.Email)
	fmt.Fprintf(output, text(ctx, "Проект: %s\n", "Project: %s\n"), link.ProjectID)
	fmt.Fprintf(output, text(ctx, "Workspace: %s (%s)\n", "Workspace: %s (%s)\n"), link.WorkspaceID, workspace.Workspace.State)
	fmt.Fprintf(
		output,
		text(ctx, "Урок: %s v%d — %s (%s)\n", "Assignment: %s v%d — %s (%s)\n"),
		workspace.Assignment.ID,
		workspace.Assignment.Version,
		workspace.Assignment.Title,
		workspace.Assignment.State,
	)
	if pendingLessonVersion(link, workspace) {
		// A newer version of this lesson is published. The folder can still
		// submit on its own version; `update` moves it when the learner chooses.
		fmt.Fprintf(
			output,
			text(ctx, "Урок обновлён: %s v%d → v%d. Отправлять решение можно и сейчас; перейти на новую версию: `softpractice update`\n",
				"Lesson updated: %s v%d → v%d. You can still submit now; to move to the new version: `softpractice update`\n"),
			link.AssignmentID, link.AssignmentVersion, workspace.Assignment.Version,
		)
	}
	if pendingTransition(link, workspace) {
		// The server has already opened the next lesson while this folder is
		// still on the previous one. Without this line `Assignment` names a
		// lesson whose files are not here yet, and the submission line below
		// reads as if the previous lesson had disappeared.
		fmt.Fprintf(
			output,
			text(ctx, "Переход не применён: %s v%d → %s v%d (выполните `softpractice update`)\n",
				"Transition pending: %s v%d → %s v%d (run `softpractice update`)\n"),
			link.AssignmentID, link.AssignmentVersion,
			workspace.Assignment.ID, workspace.Assignment.Version,
		)
	}
	fmt.Fprintf(output, text(ctx, "Локальный HEAD: %s\n", "Local HEAD: %s\n"), repository.CommitSHA)
	fmt.Fprintf(output, text(ctx, "Рабочее дерево: %s\n", "Working tree: %s\n"), clean)
	// The server reports submissions of the current lesson only, so an
	// unqualified "none" hides the accepted history of the previous lessons.
	if workspace.LatestSubmission == nil {
		fmt.Fprintf(
			output,
			text(ctx, "Последняя отправка урока %s: нет\n", "Latest %s submission: none\n"),
			workspace.Assignment.ID,
		)
	} else {
		fmt.Fprintf(
			output,
			text(ctx, "Последняя отправка урока %s: %s (%s, %s)\n", "Latest %s submission: %s (%s, %s)\n"),
			workspace.Assignment.ID,
			workspace.LatestSubmission.ID,
			workspace.LatestSubmission.JobState,
			workspace.LatestSubmission.SubmittedAt.Format(time.RFC3339),
		)
	}
	return nil
}

// pendingTransition reports whether the server has opened the next lesson
// while this folder still holds the previous one. A v1 link pins no lesson, so
// it can never be compared this way.
func pendingTransition(link learnercli.ProjectLink, workspace learnercli.WorkspaceStatus) bool {
	return link.SchemaVersion == 2 && link.AssignmentID != workspace.Assignment.ID
}

// pendingLessonVersion reports whether this folder still names a version of
// the current lesson that the catalog has replaced. The learner keeps their
// work: only the pin and the public checks are refreshed.
func pendingLessonVersion(link learnercli.ProjectLink, workspace learnercli.WorkspaceStatus) bool {
	// Only an older pin is a republished lesson. A pin newer than the server
	// (a rolled-back release, a hand-edited link) is not an update to offer.
	return link.SchemaVersion == 2 && link.AssignmentID == workspace.Assignment.ID &&
		link.AssignmentVersion < workspace.Assignment.Version
}

func submit(
	ctx context.Context,
	client *learnercli.Client,
	startDirectory string,
	args []string,
	input io.Reader,
	output, errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("submit", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	yes := flags.Bool("yes", false, "submit without an interactive confirmation")
	checksFlag := flags.Bool("checks", false, "run local public checks before submitting")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(text(ctx, "использование: softpractice submit [--yes]", "usage: softpractice submit [--yes]"))
	}
	repository, link, workspace, err := linkedWorkspace(ctx, client, startDirectory, true)
	if err != nil {
		return err
	}
	defer os.Remove(repository.ArchivePath)
	if !repository.Clean {
		return errors.New(text(ctx, "рабочее дерево не чистое; сделайте commit всех изменений перед отправкой", "working tree is not clean; commit all changes before submitting"))
	}
	if workspace.Workspace.State != "active" {
		return fmt.Errorf(text(ctx, "workspace находится в состоянии %s и не принимает отправки", "workspace is %s and cannot accept a submission"), workspace.Workspace.State)
	}
	if workspace.Assignment.State != "available" && workspace.Assignment.State != "submitted" {
		return fmt.Errorf(
			text(ctx, "урок находится в состоянии %s и не принимает отправки", "assignment is %s and cannot accept a submission"),
			workspace.Assignment.State,
		)
	}
	fmt.Fprintf(
		output,
		text(ctx, "Проект: %s\nУрок: %s v%d — %s\n\n", "Project: %s\nAssignment: %s v%d — %s\n\n"),
		link.ProjectID,
		workspace.Assignment.ID,
		submittedLessonVersion(link, workspace),
		workspace.Assignment.Title,
	)
	fmt.Fprintf(
		output,
		text(ctx, "Commit: %s\nФайлы: %d\nСжатый размер: %s\n", "Commit: %s\nFiles: %d\nCompressed size: %s\n"),
		repository.CommitSHA,
		len(repository.Files),
		formatBytes(repository.CompressedBytes),
	)
	for _, path := range suspiciousPaths(repository.Files) {
		fmt.Fprintf(errorOutput, text(ctx, "Предупреждение: commit содержит потенциально чувствительный файл: %s\n", "Warning: the commit contains a potentially sensitive file: %s\n"), path)
	}
	enabled, explicit := *checksFlag, false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "checks" {
			explicit = true
		}
	})
	if !explicit {
		enabled, err = projectAutoChecks(ctx, repository.Root)
		if err != nil {
			return err
		}
	}
	if enabled {
		if err := runSubmissionChecks(ctx, repository, output, errorOutput); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(output, text(ctx, "Локальные проверки не запускались. Решение проверит сервер. Для отдельного локального запуска: softpractice check.", "Local checks were not run. The server will check your solution. To run them separately: softpractice check."))
	}
	if !*yes {
		confirmed, err := confirmSubmission(ctx, input, output)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(output, text(ctx, "Отправка отменена.", "Submission cancelled."))
			return nil
		}
	}
	requestPath, headers, idempotencyKey := submissionRequest(
		link,
		workspace,
		repository.CommitSHA,
	)
	var receipt submissionReceipt
	err = client.Submit(
		ctx,
		requestPath,
		repository.ArchivePath,
		headers,
		&receipt,
	)
	if err != nil {
		return fmt.Errorf(
			"submit %s at %s (idempotency key %s): %w",
			repository.Root,
			repository.CommitSHA,
			idempotencyKey,
			err,
		)
	}
	replay := ""
	if receipt.Replayed {
		replay = " (already submitted)"
	}
	fmt.Fprintf(
		output,
		text(ctx, "Отправка: %s%s\nСостояние проверки: %s\n", "Submission: %s%s\nEvaluation state: %s\n"),
		receipt.SubmissionID,
		replay,
		receipt.JobState,
	)
	if update := receipt.LessonUpdate; update != nil {
		// The submission is accepted and evaluated as usual. The notice only
		// says a newer version exists; updating stays the learner's choice.
		fmt.Fprintf(
			output,
			text(ctx, "\nУрок обновлён: %s v%d → v%d. Решение принято и проверяется как обычно. Чтобы перейти на новую версию, выполните `softpractice update`.\n",
				"\nLesson updated: %s v%d → v%d. Your submission is accepted and evaluated as usual. To move to the new version, run `softpractice update`.\n"),
			update.AssignmentID, update.SubmittedVersion, update.CurrentVersion,
		)
	}
	return nil
}

// submittedLessonVersion is the lesson version this folder's tree was made
// for. After a version bump the server has moved on, but the tree has not
// until `softpractice update`, and the server picks the evaluation profile
// that accepts this tree from the version it names.
func submittedLessonVersion(link learnercli.ProjectLink, workspace learnercli.WorkspaceStatus) int {
	if pendingLessonVersion(link, workspace) {
		return link.AssignmentVersion
	}
	return workspace.Assignment.Version
}

func submissionRequest(
	link learnercli.ProjectLink,
	workspace learnercli.WorkspaceStatus,
	commitSHA string,
) (string, map[string]string, string) {
	baseRevisionID := ""
	if workspace.Workspace.BaseRevisionID != nil {
		baseRevisionID = *workspace.Workspace.BaseRevisionID
	}
	version := submittedLessonVersion(link, workspace)
	idempotencyKey := uuid.NewSHA1(
		submissionNamespace,
		[]byte(
			link.WorkspaceID+"\x00"+
				workspace.Assignment.ID+"\x00"+
				fmt.Sprint(version)+"\x00"+
				baseRevisionID+"\x00"+
				commitSHA,
		),
	).String()
	headers := map[string]string{
		"Idempotency-Key":                   idempotencyKey,
		"X-Softpractice-Commit-SHA":         commitSHA,
		"X-Softpractice-Assignment-Version": fmt.Sprint(version),
		"X-Softpractice-CLI-Version":        cliVersion,
	}
	if baseRevisionID != "" {
		headers["X-Softpractice-Base-Revision-ID"] = baseRevisionID
	}
	return "/v1/workspaces/" + link.WorkspaceID +
			"/assignments/" + workspace.Assignment.ID + "/submissions",
		headers,
		idempotencyKey
}

func openCurrent(
	ctx context.Context,
	client *learnercli.Client,
	startDirectory string,
	args []string,
	output, errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("open", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	webURL := flags.String(
		"web",
		settingsFromContext(ctx).WebURL,
		"web app base URL",
	)
	noBrowser := flags.Bool("no-browser", false, "print without opening a browser")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(text(ctx, "использование: softpractice open [--web URL] [--no-browser]", "usage: softpractice open [--web URL] [--no-browser]"))
	}
	if err := validateWebURL(*webURL); err != nil {
		return err
	}
	_, link, workspace, err := linkedWorkspace(ctx, client, startDirectory, false)
	if err != nil {
		return err
	}
	target := strings.TrimRight(*webURL, "/") + "/workspaces/" + link.WorkspaceID
	if workspace.LatestSubmission != nil {
		target = strings.TrimRight(*webURL, "/") +
			"/submissions/" + workspace.LatestSubmission.ID + "/result"
	}
	fmt.Fprintln(output, target)
	if *noBrowser {
		return nil
	}
	return openBrowser(target)
}

func linkedWorkspace(
	ctx context.Context,
	client *learnercli.Client,
	startDirectory string,
	createArchive bool,
) (gitRepository, learnercli.ProjectLink, learnercli.WorkspaceStatus, error) {
	repository, err := inspectGitRepository(ctx, startDirectory, createArchive)
	if err != nil {
		return gitRepository{}, learnercli.ProjectLink{}, learnercli.WorkspaceStatus{}, err
	}
	link, err := learnercli.LoadProjectLink(repository.Root)
	if err != nil {
		if createArchive {
			_ = os.Remove(repository.ArchivePath)
		}
		return gitRepository{}, learnercli.ProjectLink{}, learnercli.WorkspaceStatus{}, err
	}
	workspace, err := client.GetLinkedWorkspace(ctx, link)
	if err != nil {
		if createArchive {
			_ = os.Remove(repository.ArchivePath)
		}
		return gitRepository{}, learnercli.ProjectLink{}, learnercli.WorkspaceStatus{}, err
	}
	return repository, link, workspace, nil
}

func confirmSubmission(ctx context.Context, input io.Reader, output io.Writer) (bool, error) {
	fmt.Fprint(output, text(ctx, "\nОтправить этот commit? [Y/n] ", "\nSubmit this commit? [Y/n] "))
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && answer != "") {
		return false, errors.New(text(ctx, "интерактивное подтверждение недоступно; используйте --yes", "interactive confirmation is unavailable; use --yes"))
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "", "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, errors.New(text(ctx, "подтверждение должно быть yes или no", "confirmation must be yes or no"))
	}
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
