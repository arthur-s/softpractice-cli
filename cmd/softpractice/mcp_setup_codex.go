package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

func codexConfigPath() (string, error) {
	directory := os.Getenv("CODEX_HOME")
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		directory = filepath.Join(home, ".codex")
	}
	return filepath.Abs(filepath.Join(directory, "config.toml"))
}

func codexServerEntry(executable string) map[string]any {
	return map[string]any{
		"command":          executable,
		"args":             []string{"mcp", "connect"},
		"tool_timeout_sec": int64(180),
		"enabled":          true,
	}
}

func setupCodexDesktop(ctx context.Context, executable string, printOnly bool, output io.Writer) error {
	entry := codexServerEntry(executable)
	if printOnly {
		document, err := toml.Marshal(map[string]any{"mcp_servers": map[string]any{mcpServerName: entry}})
		if err != nil {
			return err
		}
		_, err = output.Write(bytes.TrimPrefix(document, []byte("[mcp_servers]\n")))
		return err
	}
	path, err := codexConfigPath()
	if err != nil {
		return err
	}
	backup, changed, err := addCodexMCPServerToConfig(ctx, path, entry)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(output, text(ctx, "Сервер softpractice добавлен в настройки Codex Desktop: %s\n", "Added the softpractice server to the Codex Desktop settings: %s\n"), path)
	} else {
		fmt.Fprintf(output, text(ctx, "Codex Desktop уже настроен: %s\n", "Codex Desktop is already configured: %s\n"), path)
	}
	printMCPSetupTarget(ctx, executable, output)
	if backup != "" {
		fmt.Fprintf(output, text(ctx, "Прежний файл сохранён: %s\n", "The previous file is saved as %s\n"), backup)
	}
	if changed {
		fmt.Fprintln(output, text(ctx,
			"\nПерезапусти Codex Desktop и открой папку проекта урока. Проверь сервер softpractice в настройках MCP и попроси агента показать статус урока и задание.",
			"\nRestart Codex Desktop and open the lesson project folder. Check the softpractice server in the MCP settings and ask the agent to show the lesson status and task."))
	}
	printMCPSetupNextStep(ctx, output)
	return nil
}

// Replace only the server's TOML tables. Parsing headers avoids mistaking text
// in multiline strings for a table; all other sections remain byte-for-byte.
func replaceCodexServerTables(original, entry []byte) ([]byte, error) {
	var parser unstable.Parser
	parser.Reset(original)
	var updated bytes.Buffer
	start, copied, target := 0, 0, false
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind != unstable.Table && node.Kind != unstable.ArrayTable {
			continue
		}
		keys := node.Key()
		var path []string
		position := 0
		for keys.Next() {
			if len(path) == 0 {
				position = int(keys.Node().Raw.Offset)
			}
			path = append(path, string(keys.Node().Data))
		}
		position = tableStart(original, bytes.LastIndexByte(original[:position], '\n')+1)
		if target {
			updated.Write(original[copied:start])
			copied = position
		}
		start = position
		target = len(path) >= 2 && path[0] == "mcp_servers" && path[1] == mcpServerName
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	if target {
		updated.Write(original[copied:start])
	} else {
		updated.Write(original[copied:])
	}
	newline := []byte("\n")
	if bytes.Contains(original, []byte("\r\n")) {
		newline = []byte("\r\n")
	}
	if len(entry) > 0 && updated.Len() > 0 {
		if !bytes.HasSuffix(updated.Bytes(), []byte("\n")) {
			updated.Write(newline)
		}
		updated.Write(newline)
	}
	// Marshal emits an empty parent table. It may already exist in the file.
	entry = bytes.TrimPrefix(entry, []byte("[mcp_servers]\n"))
	updated.Write(bytes.ReplaceAll(entry, []byte("\n"), newline))
	return updated.Bytes(), nil
}

// tableStart moves a table's start from its header line up over the comment
// lines directly above it: they describe this table, not the previous one.
func tableStart(document []byte, header int) int {
	start := header
	for start > 0 {
		previous := bytes.LastIndexByte(document[:start-1], '\n') + 1
		if !bytes.HasPrefix(bytes.TrimSpace(document[previous:start]), []byte("#")) {
			break
		}
		start = previous
	}
	return start
}

func addCodexMCPServerToConfig(ctx context.Context, path string, entry map[string]any) (string, bool, error) {
	return updateCodexMCPServerConfig(ctx, path, entry)
}

// A nil entry removes only SoftPractice, including its nested tables.
func updateCodexMCPServerConfig(ctx context.Context, path string, entry map[string]any) (string, bool, error) {
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	exists := err == nil
	if !exists && entry == nil {
		return "", false, nil
	}
	mode := fs.FileMode(0o600)
	if exists {
		info, err := os.Stat(path)
		if err != nil {
			return "", false, err
		}
		mode = info.Mode().Perm()
	}
	unchanged := func(err error) (string, bool, error) {
		if entry == nil {
			return "", false, fmt.Errorf(text(ctx, "Не удалось удалить подключение из %s, файл не изменён: %w", "Could not remove the connection from %s; the file is unchanged: %w"), path, err)
		}
		return "", false, fmt.Errorf(text(ctx,
			"Не удалось обновить %s, файл не изменён; используй softpractice mcp setup codex-desktop --print для ручной настройки: %w",
			"Could not update %s; the file is unchanged. Use softpractice mcp setup codex-desktop --print for manual setup: %w"), path, err)
	}
	config := map[string]any{}
	if err := toml.Unmarshal(original, &config); err != nil {
		return unchanged(err)
	}
	servers, ok := config["mcp_servers"].(map[string]any)
	if !ok {
		if _, exists := config["mcp_servers"]; exists {
			return unchanged(errors.New("mcp_servers is not a TOML table"))
		}
		if entry == nil {
			return "", false, nil
		}
		servers = map[string]any{}
		config["mcp_servers"] = servers
	}
	server := map[string]any{}
	if previous, exists := servers[mcpServerName]; exists {
		var ok bool
		server, ok = previous.(map[string]any)
		if !ok {
			return unchanged(errors.New("mcp_servers.softpractice is not a TOML table"))
		}
	}
	if entry == nil && servers[mcpServerName] == nil {
		return "", false, nil
	}
	previous, _ := toml.Marshal(server)
	// A stdio entry must not retain a previous HTTP transport.
	for _, key := range []string{"url", "bearer_token_env_var", "http_headers", "env_http_headers", "oauth"} {
		delete(server, key)
	}
	for key, value := range entry {
		server[key] = value
	}
	current, err := toml.Marshal(server)
	if err != nil {
		return unchanged(err)
	}
	if entry != nil && exists && bytes.Equal(previous, current) {
		return "", false, nil
	}

	var document []byte
	if entry == nil {
		delete(servers, mcpServerName)
	} else {
		servers[mcpServerName] = server
		document, err = toml.Marshal(map[string]any{"mcp_servers": map[string]any{mcpServerName: server}})
		if err != nil {
			return unchanged(err)
		}
	}
	updated, err := replaceCodexServerTables(original, document)
	if err != nil {
		return unchanged(err)
	}
	verified := map[string]any{}
	if err := toml.Unmarshal(updated, &verified); err != nil {
		return unchanged(err)
	}
	// Inline or dotted server definitions cannot be safely replaced as sections.
	expected := map[string]any{}
	canonical, err := toml.Marshal(config)
	if err != nil {
		return unchanged(err)
	}
	if err := toml.Unmarshal(canonical, &expected); err != nil {
		return unchanged(err)
	}
	if entry == nil && len(servers) == 0 {
		delete(expected, "mcp_servers")
		if remaining, ok := verified["mcp_servers"].(map[string]any); ok && len(remaining) == 0 {
			delete(verified, "mcp_servers")
		}
	}
	if !reflect.DeepEqual(verified, expected) {
		return unchanged(errors.New("unsupported TOML server layout"))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".codex_config-*")
	if err != nil {
		return "", false, err
	}
	defer os.Remove(temporary.Name())
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
	backup := ""
	if exists {
		backup = path + ".bak"
		if err := os.WriteFile(backup, original, mode); err != nil {
			return "", false, err
		}
	}
	err = replaceConfigFile(temporary.Name(), path)
	return backup, err == nil, err
}
