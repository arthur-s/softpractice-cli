package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
)

func TestCodexSetupPreservesSettingsAndSwitchesProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	prefix := "# My preferences\nmodel = 'example'\nnote = '''\n[mcp_servers.softpractice]\nthis is just text\n'''\n\n[mcp_servers.other]\ncommand = 'other' # keep this\n"
	original := prefix + "\n[mcp_servers.'softpractice']\nurl = 'https://example.com/mcp'\nenabled = false\ndisabled_tools = ['submit']\n\n[mcp_servers.softpractice.env]\nSOFTPRACTICE_LANGUAGE = 'en'\n\n[projects.'/work/other']\ntrust_level = 'trusted'\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := codexServerEntry(`C:\Program Files\SoftPractice\softpractice.exe`, `/work/lesson "one"`)
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
		!reflect.DeepEqual(server["args"], []any{"mcp", "--project", `/work/lesson "one"`}) ||
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
	entry = codexServerEntry("/opt/softpractice", "/work/second")
	if _, changed, err := addCodexMCPServerToConfig(context.Background(), path, entry); err != nil || !changed {
		t.Fatalf("switch: changed=%v err=%v", changed, err)
	}
	switched, _ := os.ReadFile(path)
	if err := toml.Unmarshal(switched, &config); err != nil {
		t.Fatal(err)
	}
	server = config["mcp_servers"].(map[string]any)[mcpServerName].(map[string]any)
	if !reflect.DeepEqual(server["args"], []any{"mcp", "--project", "/work/second"}) {
		t.Fatalf("switched args = %#v", server["args"])
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions: %v, %v", info, err)
	}
}

func TestCodexSetupCreatesConfigDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".codex", "config.toml")
	backup, changed, err := addCodexMCPServerToConfig(context.Background(), path, codexServerEntry("/opt/softpractice", "/work/lesson"))
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
			if _, _, err := addCodexMCPServerToConfig(context.Background(), path, codexServerEntry("/opt/softpractice", "/work/lesson")); err == nil {
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
	root := createLinkedGitRepository(t, uuid.NewString())
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "softpractice")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", executable, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s, %v", output, err)
	}
	args := []string{"mcp", "setup", "codex-desktop", "--project", root, "--print"}
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
	if !filepath.IsAbs(server["command"].(string)) || !reflect.DeepEqual(server["args"], []any{"mcp", "--project", root}) {
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
		if _, changed, err := addCodexMCPServerToConfig(context.Background(), path, codexServerEntry("/opt/softpractice", "/work/lesson")); err != nil || !changed {
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
	if _, _, err := addCodexMCPServerToConfig(context.Background(), path, codexServerEntry("/opt/softpractice", "/work/lesson")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(data), "[mcp_servers.softpractice]\n") {
		t.Fatalf("config = %q, %v", data, err)
	}
}
