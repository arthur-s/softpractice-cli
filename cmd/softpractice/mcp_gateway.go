package main

import (
	"context"
	"errors"
	"io"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpGatewayOperationsKey struct{}
type mcpGatewayIdentityKey struct{}

// Desktop owns this process. It can initialize and list tools while the lesson
// server is stopped; each operation resolves the live lesson before running
// the same use cases as the project server. No separate daemon is installed.
func newMCPGateway(ctx context.Context, client *learnercli.Client, logOutput io.Writer) (*mcp.Server, error) {
	settings := settingsFromContext(ctx)
	server, err := newMCPServer(ctx, newLearnerUseCases(client, ""), logOutput)
	if err != nil {
		return nil, err
	}
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method != "tools/call" && method != "resources/read" {
				return next(ctx, method, request)
			}
			ctx = withSettings(ctx, settings)
			status, err := readLocalMCPStatus(ctx)
			var failure error
			if err != nil {
				failure = mcpFailure("lesson_server_unavailable", text(ctx,
					"Не удалось проверить сервер урока. Выполни softpractice mcp status в терминале. Подключение к Desktop остаётся активным.",
					"Could not check the lesson server. Run softpractice mcp status in a terminal. The Desktop connection remains active."))
			} else if status.State != "running" {
				failure = mcpFailure("lesson_server_not_running", text(ctx,
					"Сервер урока не запущен. Выполни softpractice mcp в папке урока или softpractice mcp --project DIR, затем повтори запрос. Перезапуск Desktop не нужен.",
					"The lesson server is not running. Run softpractice mcp in the lesson folder or softpractice mcp --project DIR, then repeat the request. Desktop does not need to be restarted."))
			} else if status.InstanceID == "" {
				failure = mcpFailure("lesson_server_version_mismatch", text(ctx,
					"Перезапусти сервер урока установленной версией CLI: softpractice mcp --project DIR.",
					"Restart the lesson server with the installed CLI version: softpractice mcp --project DIR."))
			}
			if failure != nil {
				if method == "tools/call" {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: failure.Error()}}}, nil
				}
				return nil, failure
			}
			// Capture the directory once: concurrent requests cannot change each
			// other's project. A pending confirmation is tied to this server run.
			ctx = context.WithValue(ctx, mcpGatewayOperationsKey{}, newLearnerUseCases(client, status.Project))
			ctx = context.WithValue(ctx, mcpGatewayIdentityKey{}, mcpGatewayIdentity(status))
			return next(ctx, method, request)
		}
	})
	return server, nil
}

func runMCPGateway(ctx context.Context, client *learnercli.Client, input io.Reader, output, logOutput io.Writer) error {
	server, err := newMCPGateway(ctx, client, logOutput)
	if err != nil {
		return err
	}
	err = server.Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(input), Writer: mcpGatewayWriter{output}})
	if errors.Is(err, io.EOF) || errors.Is(err, mcp.ErrConnectionClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

type mcpGatewayWriter struct{ io.Writer }

func (mcpGatewayWriter) Close() error { return nil }

func mcpGatewayIdentity(status localMCPStatus) string {
	return status.Project + "\n" + status.InstanceID
}
