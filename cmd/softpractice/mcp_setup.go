package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
)

const mcpServerName = "softpractice"

// mcpSetupCommand registers the MCP server without requiring a client's CLI.
func mcpSetupCommand(ctx context.Context, args []string, output, errorOutput io.Writer) error {
	usageText := func() error {
		return usage(ctx,
			"использование: softpractice mcp setup <codex-desktop|claude-desktop> [--project DIR] [--print]",
			"usage: softpractice mcp setup <codex-desktop|claude-desktop> [--project DIR] [--print]")
	}
	if len(args) == 0 || (args[0] != "claude-desktop" && args[0] != "codex-desktop") {
		return usageText()
	}
	flags := flag.NewFlagSet("mcp setup "+args[0], flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	project := flags.String("project", "", "optional lesson project folder for direct stdio mode")
	printOnly := flags.Bool("print", false, "print the configuration entry instead of writing it")
	if err := parseFlags(flags, args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageText()
	}
	root := ""
	if strings.TrimSpace(*project) != "" {
		var err error
		root, err = mcpProjectRoot(ctx, strings.TrimSpace(*project))
		if err != nil {
			return err
		}
	}
	executable, err := mcpSetupExecutable(ctx)
	if err != nil {
		return err
	}
	if args[0] == "codex-desktop" {
		return setupCodexDesktop(ctx, executable, root, *printOnly, output)
	}
	entry, err := json.Marshal(struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{executable, mcpSetupArgs(root)})
	if err != nil {
		return err
	}
	if *printOnly {
		document, err := encodeJSONObject([]jsonMember{{"mcpServers", mustEncodeJSONObject([]jsonMember{{mcpServerName, entry}})}})
		if err != nil {
			return err
		}
		_, err = output.Write(document)
		return err
	}
	path, err := claudeDesktopConfigPath(ctx)
	if err != nil {
		return err
	}
	backup, changed, err := addMCPServerToConfig(ctx, path, entry)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(output, text(ctx, "Сервер softpractice добавлен в настройки Claude Desktop: %s\n", "Added the softpractice server to the Claude Desktop settings: %s\n"), path)
	} else {
		fmt.Fprintf(output, text(ctx, "Claude Desktop уже настроен: %s\n", "Claude Desktop is already configured: %s\n"), path)
	}
	printMCPSetupTarget(ctx, executable, root, output)
	if backup != "" {
		fmt.Fprintf(output, text(ctx, "Прежний файл сохранён: %s\n", "The previous file is saved as %s\n"), backup)
	}
	if changed {
		fmt.Fprintln(output, text(ctx,
			"\nПолностью закрой Claude Desktop (не только окно) и запусти снова: инструменты softpractice появятся в меню инструментов чата.",
			"\nQuit Claude Desktop completely, not just its window, and start it again: the softpractice tools appear in the chat's tools menu."))
	}
	printMCPSetupNextStep(ctx, root, output)
	return nil
}

func mcpProjectRoot(ctx context.Context, directory string) (string, error) {
	if directory != "" {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return "", err
		}
		directory = absolute
	}
	repository, err := inspectGitRepository(ctx, directory, false)
	if err == nil {
		_, err = learnercli.LoadProjectLink(repository.Root)
	}
	if err != nil {
		return "", fmt.Errorf(text(ctx,
			"запусти сервер в папке проекта урока или укажи --project: %w",
			"start the server in the lesson project folder or pass --project: %w"), err)
	}
	return repository.Root, nil
}

// mcpSetupExecutable is the native program, not the npm wrapper that started
// it: a desktop client may find neither the wrapper nor Node.js on its PATH.
func mcpSetupExecutable(ctx context.Context) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	if strings.Contains(executable, string(filepath.Separator)+"go-build") {
		return "", errors.New(text(ctx,
			"программа запущена через go run из временной папки; собери или установи CLI",
			"the program runs through go run from a temporary folder; build or install the CLI"))
	}
	return executable, nil
}

// claudeDesktopConfigPath is the file Claude Desktop reads. Its Microsoft
// Store (MSIX) build reads a virtualized copy of %APPDATA% once it has
// written one, while its Edit Config button still opens the real file.
func claudeDesktopConfigPath(ctx context.Context) (string, error) {
	const name = "claude_desktop_config.json"
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			matches, _ := filepath.Glob(filepath.Join(local, "Packages", "Claude_*", "LocalCache", "Roaming", "Claude"))
			for _, directory := range matches {
				if info, err := os.Stat(directory); err == nil && info.IsDir() {
					return filepath.Join(directory, name), nil
				}
			}
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(base, "Claude")
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return "", fmt.Errorf(text(ctx,
			"Claude Desktop не найден (нет папки %s); установи и запусти его один раз или получи запись для ручной настройки: softpractice mcp setup claude-desktop --print",
			"Claude Desktop is not found (no folder %s); install and start it once, or get the entry for manual setup: softpractice mcp setup claude-desktop --print"), directory)
	}
	return filepath.Join(directory, name), nil
}

// addMCPServerToConfig sets mcpServers.softpractice and keeps every other
// member as it was, in its order: the file also holds the app's preferences.
// A file that is not a JSON object is left untouched.
func addMCPServerToConfig(ctx context.Context, path string, entry json.RawMessage) (backup string, changed bool, err error) {
	mode := fs.FileMode(0o644)
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	exists := err == nil
	if exists {
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
	}
	var members []jsonMember
	if len(bytes.TrimSpace(original)) != 0 {
		if members, err = decodeJSONObject(original); err != nil {
			return "", false, fmt.Errorf(text(ctx,
				"%s не удалось разобрать как объект JSON, файл не изменён: %w",
				"%s is not a valid JSON object; the file is unchanged: %w"), path, err)
		}
	}
	var servers []jsonMember
	serversIndex := -1
	for i, member := range members {
		if member.Key == "mcpServers" {
			serversIndex = i
			if servers, err = decodeJSONObject(member.Value); err != nil {
				return "", false, fmt.Errorf(text(ctx,
					"mcpServers в %s не объект JSON, файл не изменён: %w",
					"mcpServers in %s is not a JSON object; the file is unchanged: %w"), path, err)
			}
		}
	}
	servers = setJSONMember(servers, mcpServerName, entry)
	encodedServers, err := encodeJSONObject(servers)
	if err != nil {
		return "", false, err
	}
	if serversIndex >= 0 {
		members[serversIndex].Value = encodedServers
	} else {
		members = append(members, jsonMember{"mcpServers", encodedServers})
	}
	updated, err := encodeJSONObject(members)
	if err != nil {
		return "", false, err
	}
	if exists {
		if previous, err := encodeJSONObject(mustDecodeJSONObject(original)); err == nil && bytes.Equal(previous, updated) {
			return "", false, nil
		}
		backup = path + ".bak"
		if err := os.WriteFile(backup, original, mode); err != nil {
			return "", false, err
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".claude_desktop_config-*")
	if err != nil {
		return "", false, err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		_ = temporary.Close()
		return "", false, err
	}
	if _, err := temporary.Write(updated); err != nil {
		_ = temporary.Close()
		return "", false, err
	}
	if err := temporary.Close(); err != nil {
		return "", false, err
	}
	return backup, true, replaceConfigFile(name, path)
}

type jsonMember struct {
	Key   string
	Value json.RawMessage
}

// decodeJSONObject reads the members of one JSON object in document order,
// which a Go map would lose.
func decodeJSONObject(data []byte) ([]jsonMember, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	members := []jsonMember{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, jsonMember{key, value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return members, nil
}

func mustDecodeJSONObject(data []byte) []jsonMember {
	members, _ := decodeJSONObject(data)
	return members
}

func setJSONMember(members []jsonMember, key string, value json.RawMessage) []jsonMember {
	for i := range members {
		if members[i].Key == key {
			members[i].Value = value
			return members
		}
	}
	return append(members, jsonMember{key, value})
}

// encodeJSONObject writes members in order, indented by two spaces.
func encodeJSONObject(members []jsonMember) ([]byte, error) {
	var compact bytes.Buffer
	compact.WriteByte('{')
	for i, member := range members {
		if i > 0 {
			compact.WriteByte(',')
		}
		key, err := json.Marshal(member.Key)
		if err != nil {
			return nil, err
		}
		compact.Write(key)
		compact.WriteByte(':')
		compact.Write(member.Value)
	}
	compact.WriteByte('}')
	var indented bytes.Buffer
	if err := json.Indent(&indented, compact.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	indented.WriteByte('\n')
	return indented.Bytes(), nil
}

func mustEncodeJSONObject(members []jsonMember) json.RawMessage {
	data, _ := encodeJSONObject(members)
	return data
}
