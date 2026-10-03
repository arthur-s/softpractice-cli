package main

import (
	"context"
	"fmt"
	"io"
)

// Removing a client connection does not stop the lesson server or change it.
func mcpRemoveCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) != 1 || (args[0] != "claude-desktop" && args[0] != "codex-desktop") {
		return usage(ctx, "использование: softpractice mcp remove <codex-desktop|claude-desktop>", "usage: softpractice mcp remove <codex-desktop|claude-desktop>")
	}
	var path, backup string
	var changed bool
	var err error
	var client string
	if args[0] == "codex-desktop" {
		client = "Codex"
		path, err = codexConfigPath()
		if err == nil {
			backup, changed, err = updateCodexMCPServerConfig(ctx, path, nil)
		}
	} else {
		client = "Claude Desktop"
		path, err = claudeDesktopConfigPath(ctx, false)
		if err == nil {
			backup, changed, err = updateMCPServerConfig(ctx, path, nil)
		}
	}
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(output, text(ctx, "Подключение softpractice уже отсутствует в настройках %s: %s\n", "The softpractice connection is already absent from the %s settings: %s\n"), client, path)
		return nil
	}
	fmt.Fprintf(output, text(ctx, "Подключение softpractice удалено из настроек %s: %s\n", "Removed the softpractice connection from the %s settings: %s\n"), client, path)
	fmt.Fprintf(output, text(ctx, "Прежний файл сохранён: %s\n", "The previous file is saved as %s\n"), backup)
	fmt.Fprintf(output, text(ctx,
		"Перезапусти клиент, чтобы применить изменение. Работающий сервер урока можно остановить через Ctrl+C в его терминале. Чтобы подключить MCP снова: softpractice mcp setup %s.\n",
		"Restart the client to apply the change. Stop the running lesson server with Ctrl+C in its terminal. To reconnect MCP: softpractice mcp setup %s.\n"), args[0])
	if args[0] == "codex-desktop" {
		fmt.Fprintln(output, text(ctx, "Изменение действует на общие настройки Codex Desktop, CLI и расширения IDE. Настройки отдельных проектов могут переопределять их.", "This changes the shared settings of Codex Desktop, CLI, and the IDE extension. Project settings may override them."))
	}
	return nil
}
