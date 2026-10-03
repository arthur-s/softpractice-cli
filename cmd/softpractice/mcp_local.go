package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A fixed loopback port permits only one active lesson server at a time. The
// bridge authenticates with a per-run secret before forwarding MCP messages.
const localMCPAddress = "127.0.0.1:39473"

func localMCPTokenPath() (string, error) {
	store, err := defaultConfigStore()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(store.Path), "mcp-local.token"), nil
}

func serveLocalMCP(ctx context.Context, server *mcp.Server, project string, output io.Writer) error {
	listener, err := net.Listen("tcp4", localMCPAddress)
	if err != nil {
		return fmt.Errorf(text(ctx, "не удалось запустить MCP-сервер: %w. Возможно, уже запущен сервер другого урока; останови его перед переключением проекта", "could not start the MCP server: %w. Another lesson server may be running; stop it before switching projects"), err)
	}
	defer listener.Close()
	path, err := localMCPTokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)
	// Recreate the file to avoid retaining loose permissions or following a
	// symlink left at this path. The listener prevents competing server writes.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	_, writeErr := io.WriteString(file, token)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	fmt.Fprintf(output, text(ctx,
		"SoftPractice MCP-сервер запущен.\nПроект: %s\nПодключение: %s (только этот компьютер).\nНастрой клиент из любой папки: softpractice mcp setup claude-desktop или softpractice mcp setup codex-desktop.\nОставь терминал открытым. Остановить: Ctrl+C.\n",
		"SoftPractice MCP server is running.\nProject: %s\nConnection: %s (this computer only).\nConfigure the client from any folder: softpractice mcp setup claude-desktop or softpractice mcp setup codex-desktop.\nKeep this terminal open. Stop: Ctrl+C.\n"), project, localMCPAddress)
	err = acceptLocalMCP(ctx, listener, server, token)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func acceptLocalMCP(ctx context.Context, listener net.Listener, server *mcp.Server, token string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	var sessions sync.WaitGroup
	defer sessions.Wait()
	for {
		connection, err := listener.Accept()
		if err != nil {
			cancel()
			return err
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			defer connection.Close()
			stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
			defer stop()
			_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
			reader := bufio.NewReaderSize(connection, 4096)
			line, err := reader.ReadSlice('\n')
			if err != nil || !hmac.Equal(line, []byte(token+"\n")) {
				return
			}
			if _, err := io.WriteString(connection, "OK\n"); err != nil {
				return
			}
			_ = connection.SetDeadline(time.Time{})
			_ = server.Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(reader), Writer: connection})
		}()
	}
}

func connectLocalMCP(ctx context.Context, input io.Reader, output io.Writer) error {
	unavailable := func(err error) error {
		return fmt.Errorf(text(ctx,
			"MCP-сервер недоступен: %w. Запусти `softpractice mcp` в папке урока или `softpractice mcp --project DIR`, затем переподключи MCP-клиент",
			"MCP server is unavailable: %w. Run `softpractice mcp` in the lesson folder or `softpractice mcp --project DIR`, then reconnect the MCP client"), err)
	}
	path, err := localMCPTokenPath()
	if err != nil {
		return unavailable(err)
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return unavailable(err)
	}
	if len(token) != 64 {
		return unavailable(errors.New("invalid local MCP token"))
	}
	connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", localMCPAddress)
	if err != nil {
		return unavailable(err)
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(connection, "%s\n", token); err != nil {
		return unavailable(err)
	}
	reader := bufio.NewReader(connection)
	if ack, err := reader.ReadString('\n'); err != nil || ack != "OK\n" {
		if err == nil {
			err = errors.New("local MCP authentication failed")
		}
		return unavailable(err)
	}
	_ = connection.SetDeadline(time.Time{})
	// stdin may be an uninterruptible pipe: do not wait for its goroutine when
	// the server disconnects. Closing the TCP connection ends its next write.
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(connection, input)
		_ = connection.(*net.TCPConn).CloseWrite()
		copied <- err
	}()
	_, err = io.Copy(output, reader)
	if ctx.Err() != nil {
		return nil
	}
	select {
	case copyErr := <-copied:
		return errors.Join(err, copyErr)
	default:
		return unavailable(errors.Join(err, io.EOF))
	}
}

func mcpSetupArgs(root string) []string {
	if root == "" {
		return []string{"mcp", "connect"}
	}
	return []string{"mcp", "--stdio", "--project", root}
}

func printMCPSetupTarget(ctx context.Context, executable, root string, output io.Writer) {
	fmt.Fprintf(output, text(ctx, "  Программа: %s\n", "  Program: %s\n"), executable)
	if root != "" {
		fmt.Fprintf(output, text(ctx, "  Проект: %s\n", "  Project: %s\n"), root)
	}
}

func printMCPSetupNextStep(ctx context.Context, root string, output io.Writer) {
	if root != "" {
		fmt.Fprintln(output, text(ctx, "Клиент сам запускает сервер для указанного проекта. Чтобы перейти на ручной запуск, повтори setup без --project.", "The client starts the server for the specified project. To switch to manual startup, rerun setup without --project."))
		return
	}
	fmt.Fprintln(output, text(ctx,
		"Запусти softpractice mcp в папке урока или softpractice mcp --project DIR и оставь терминал открытым. При смене проекта останови сервер, запусти его в новой папке и переподключи MCP-клиент. Повторять setup не нужно.",
		"Run softpractice mcp in the lesson folder or softpractice mcp --project DIR and keep the terminal open. To switch projects, stop the server, start it in the new folder, and reconnect the MCP client. Setup does not need to be repeated."))
}
