package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestCodexSetupPreservesSettingsAndUpdatesExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	prefix := "# My preferences\nmodel = 'example'\nnote = '''\n[mcp_servers.softpractice]\nthis is just text\n'''\n\n[mcp_servers.other]\ncommand = 'other' # keep this\n"
	original := prefix + "\n[mcp_servers.'softpractice']\nurl = 'https://example.com/mcp'\nenabled = false\ndisabled_tools = ['submit']\n\n[mcp_servers.softpractice.env]\nSOFTPRACTICE_LANGUAGE = 'en'\n\n[projects.'/work/other']\ntrust_level = 'trusted'\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := codexServerEntry(`C:\Program Files\SoftPractice\softpractice.exe`)
	backup, changed, err := addCodexMCPServerToConfig(context.Background(), path, entry)
	if err != nil || !changed || backup != path+".bak" {
		t.Fatalf("backup=%q changed=%v err=%v", backup, changed, err)
	}
	saved, err := os.ReadFile(backup)
	if err != nil || string(saved) != original {
		t.Fatalf("backup = %q, %v", saved, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte(prefix)) || !bytes.Contains(data, []byte("[projects.'/work/other']\ntrust_level = 'trusted'")) {
		t.Fatalf("unrelated content changed: %s", data)
	}
	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	server := config["mcp_servers"].(map[string]any)[mcpServerName].(map[string]any)
	if server["command"] != entry["command"] || server["enabled"] != true || server["tool_timeout_sec"] != int64(180) ||
		!reflect.DeepEqual(server["args"], []any{"mcp", "connect"}) ||
		server["url"] != nil || server["env"].(map[string]any)["SOFTPRACTICE_LANGUAGE"] != "en" ||
		!reflect.DeepEqual(server["disabled_tools"], []any{"submit"}) {
		t.Fatalf("server = %#v", server)
	}
	backup, changed, err = addCodexMCPServerToConfig(context.Background(), path, entry)
	if err != nil || changed || backup != "" {
		t.Fatalf("repeat: backup=%q changed=%v err=%v", backup, changed, err)
	}
	repeated, _ := os.ReadFile(path)
	if !bytes.Equal(data, repeated) {
		t.Fatal("repeat changed the file")
	}
	entry = codexServerEntry("/opt/softpractice")
	if _, changed, err := addCodexMCPServerToConfig(context.Background(), path, entry); err != nil || !changed {
		t.Fatalf("switch: changed=%v err=%v", changed, err)
	}
	switched, _ := os.ReadFile(path)
	if err := toml.Unmarshal(switched, &config); err != nil {
		t.Fatal(err)
	}
	server = config["mcp_servers"].(map[string]any)[mcpServerName].(map[string]any)
	if server["command"] != "/opt/softpractice" || !reflect.DeepEqual(server["args"], []any{"mcp", "connect"}) {
		t.Fatalf("updated server = %#v", server)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("permissions: %v, %v", info, err)
	}
}

func TestCodexSetupCreatesConfigDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".codex", "config.toml")
	backup, changed, err := addCodexMCPServerToConfig(context.Background(), path, codexServerEntry("/opt/softpractice"))
	if err != nil || !changed || backup != "" {
		t.Fatalf("backup=%q changed=%v err=%v", backup, changed, err)
	}
	data, err := os.ReadFile(path)
	var config map[string]any
	if err != nil || toml.Unmarshal(data, &config) != nil || config["mcp_servers"] == nil {
		t.Fatalf("config = %s, %v", data, err)
	}
}

func TestCodexSetupLeavesInvalidOrUnsupportedConfigUntouched(t *testing.T) {
	for _, original := range []string{
		"[broken", "mcp_servers = 42", "[mcp_servers]\nsoftpractice = 42",
		"[mcp_servers.softpractice]\ncommand = 'a'\ncommand = 'b'",
		"mcp_servers.softpractice = { command = 'old' }",
	} {
		t.Run(original, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := addCodexMCPServerToConfig(context.Background(), path, codexServerEntry("/opt/softpractice")); err == nil {
				t.Fatal("setup succeeded")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != original {
				t.Fatalf("file = %q, %v", data, err)
			}
			if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
				t.Fatalf("backup created: %v", err)
			}
		})
	}
}

func TestCodexDesktopSetupCommandPrintAndWrite(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex home"))
	executable := filepath.Join(t.TempDir(), "softpractice")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", executable, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s, %v", output, err)
	}
	args := []string{"mcp", "setup", "codex-desktop", "--print"}
	output, err := exec.Command(executable, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("print: %s, %v", output, err)
	}
	var config map[string]any
	if err := toml.Unmarshal(output, &config); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output, []byte("[mcp_servers]\n")) {
		t.Fatal("printed an unnecessary parent table")
	}
	server := config["mcp_servers"].(map[string]any)[mcpServerName].(map[string]any)
	if !filepath.IsAbs(server["command"].(string)) || !reflect.DeepEqual(server["args"], []any{"mcp", "connect"}) {
		t.Fatalf("entry = %#v", server)
	}
	path, err := codexConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("--print wrote the config: %v", err)
	}
	output, err = exec.Command(executable, args[:len(args)-1]...).CombinedOutput()
	if err != nil {
		t.Fatalf("setup: %s, %v", output, err)
	}
	if _, err := os.Stat(path); err != nil || !strings.Contains(string(output), "Codex Desktop") {
		t.Fatalf("setup: %s, %v", output, err)
	}

	// Setup and --print work before a lesson project exists.
	outside := t.TempDir()
	for _, client := range []string{"claude-desktop", "codex-desktop"} {
		command := exec.Command(executable, "mcp", "setup", client, "--print")
		command.Dir = outside
		printed, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s outside project: %s, %v", client, printed, err)
		}
		if client == "claude-desktop" {
			var config struct {
				Servers map[string]struct{ Args []string } `json:"mcpServers"`
			}
			if err := json.Unmarshal(printed, &config); err != nil || !reflect.DeepEqual(config.Servers[mcpServerName].Args, []string{"mcp", "connect"}) {
				t.Fatalf("Claude entry: %s, %v", printed, err)
			}
		} else {
			var config map[string]any
			if err := toml.Unmarshal(printed, &config); err != nil {
				t.Fatal(err)
			}
			if args := config["mcp_servers"].(map[string]any)[mcpServerName].(map[string]any)["args"]; !reflect.DeepEqual(args, []any{"mcp", "connect"}) {
				t.Fatalf("Codex args: %v", args)
			}
		}
	}
	command := exec.Command(executable, "mcp", "setup", "codex-desktop")
	command.Dir = outside
	if output, err := command.CombinedOutput(); err != nil || !strings.Contains(string(output), "softpractice mcp --project DIR") {
		t.Fatalf("global setup: %s, %v", output, err)
	}

	// Isolate all platform-specific Claude configuration paths from the user's
	// real desktop settings when exercising the writer outside a project.
	configBase := t.TempDir()
	t.Setenv("HOME", configBase)
	t.Setenv("XDG_CONFIG_HOME", configBase)
	t.Setenv("APPDATA", configBase)
	t.Setenv("LOCALAPPDATA", configBase)
	if runtime.GOOS == "darwin" {
		configBase = filepath.Join(configBase, "Library", "Application Support")
	}
	claudeDirectory := filepath.Join(configBase, "Claude")
	if err := os.MkdirAll(claudeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	command = exec.Command(executable, "mcp", "setup", "claude-desktop")
	command.Dir = outside
	if output, err := command.CombinedOutput(); err != nil || !strings.Contains(string(output), "softpractice mcp --project DIR") {
		t.Fatalf("Claude global setup: %s, %v", output, err)
	}
	saved, err := os.ReadFile(filepath.Join(claudeDirectory, "claude_desktop_config.json"))
	if err != nil || !bytes.Contains(saved, []byte(`"connect"`)) {
		t.Fatalf("Claude global config: %s, %v", saved, err)
	}

	// Both removal commands use the same isolated desktop settings and need
	// no lesson project or running server.
	for _, client := range []string{"claude-desktop", "codex-desktop"} {
		command := exec.Command(executable, "mcp", "remove", client)
		command.Dir = outside
		if removed, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s remove: %s, %v", client, removed, err)
		}
		// Repeat without replacing the saved backup or failing.
		command = exec.Command(executable, "mcp", "remove", client)
		command.Dir = outside
		if removed, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s repeated remove: %s, %v", client, removed, err)
		}
	}

}

// Comments directly above a table belong to it: replacing the server table
// keeps the comments of the table that follows and the file's line endings.
func TestCodexSetupKeepsCommentsOfFollowingTable(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		original := strings.ReplaceAll("# SoftPractice\n[mcp_servers.softpractice]\ncommand = 'old'\n\n# Settings for project A\n# trusted\n[projects.'/a']\ntrust_level = 'trusted'\n", "\n", newline)
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := addCodexMCPServerToConfig(context.Background(), path, codexServerEntry("/opt/softpractice")); err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		kept := strings.ReplaceAll("# Settings for project A\n# trusted\n[projects.'/a']\ntrust_level = 'trusted'\n", "\n", newline)
		if !strings.HasPrefix(string(data), kept) || strings.Contains(string(data), "old") ||
			strings.Count(string(data), "\n") != strings.Count(string(data), newline) {
			t.Fatalf("config = %q", data)
		}
	}
}

func TestCodexSetupNewFileStartsWithServerTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, _, err := addCodexMCPServerToConfig(context.Background(), path, codexServerEntry("/opt/softpractice")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(data), "[mcp_servers.softpractice]\n") {
		t.Fatalf("config = %q, %v", data, err)
	}
}
