package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

type localMCPStatus struct {
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Project string `json:"project,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Address string `json:"address"`
	Version string `json:"version,omitempty"`
}

func readLocalMCPStatus(ctx context.Context) (localMCPStatus, error) {
	stopped := localMCPStatus{Kind: "softpractice.mcp.status", State: "not_running", Address: localMCPAddress}
	connection, err := dialLocalMCP(ctx, " status")
	// Windows reports Winsock WSAECONNREFUSED (10061), rather than the
	// application-level syscall.ECONNREFUSED constant.
	refused := errors.Is(err, syscall.ECONNREFUSED) || (runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10061)))
	if errors.Is(err, os.ErrNotExist) || refused {
		return stopped, nil
	}
	if err != nil {
		return localMCPStatus{}, err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	var status localMCPStatus
	if err := json.NewDecoder(io.LimitReader(connection, 64<<10)).Decode(&status); err != nil {
		return localMCPStatus{}, err
	}
	if status.Kind != stopped.Kind || status.State != "running" || !filepath.IsAbs(status.Project) || status.PID <= 0 || status.Address != localMCPAddress || status.Version == "" {
		return localMCPStatus{}, errors.New("invalid local MCP status response")
	}
	return status, nil
}

func mcpStatusCommand(ctx context.Context, args []string, output, errorOutput io.Writer) (err error) {
	defer func() { err = reportJSONError(requestsJSON(args), output, err) }()
	flags := flag.NewFlagSet("mcp status", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	jsonOutput := jsonFlag(flags)
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usage(ctx, "использование: softpractice mcp status [--json]", "usage: softpractice mcp status [--json]")
	}
	status, err := readLocalMCPStatus(ctx)
	if err != nil {
		return fmt.Errorf(text(ctx, "не удалось проверить MCP-сервер: %w", "could not check the MCP server: %w"), err)
	}
	if *jsonOutput {
		return writeMachineJSON(output, status)
	}
	if status.State == "not_running" {
		_, err = fmt.Fprintln(output, text(ctx,
			"MCP-сервер не запущен. Запусти softpractice mcp в папке урока или softpractice mcp --project DIR.",
			"MCP server is not running. Run softpractice mcp in the lesson folder or softpractice mcp --project DIR."))
		return err
	}
	_, err = fmt.Fprintf(output, text(ctx,
		"MCP-сервер работает.\nПроект: %s\nPID: %d\nПодключение: %s\nВерсия CLI: %s\n",
		"MCP server is running.\nProject: %s\nPID: %d\nConnection: %s\nCLI version: %s\n"), status.Project, status.PID, status.Address, status.Version)
	return err
}
