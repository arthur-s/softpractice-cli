package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDesktopEntry = `{"command":"/opt/softpractice","args":["mcp","--project","/work/lesson"]}`

// Claude Desktop keeps its own preferences in the same file: setup changes
// only mcpServers.softpractice and keeps every other member in its place.
func TestAddMCPServerToConfigKeepsOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	original := `{"preferences":{"sidebarMode":"chat"},"mcpServers":{"other":{"command":"x"}},"zeta":1}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, changed, err := addMCPServerToConfig(context.Background(), path, json.RawMessage(testDesktopEntry))
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
	members, err := decodeJSONObject(data)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, member := range members {
		keys = append(keys, member.Key)
	}
	if strings.Join(keys, ",") != "preferences,mcpServers,zeta" {
		t.Fatalf("keys = %v", keys)
	}
	var config struct {
		Preferences map[string]string
		MCPServers  map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Preferences["sidebarMode"] != "chat" || config.MCPServers["other"].Command != "x" ||
		config.MCPServers["softpractice"].Command != "/opt/softpractice" ||
		strings.Join(config.MCPServers["softpractice"].Args, " ") != "mcp --project /work/lesson" {
		t.Fatalf("config = %s", data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}

	// A second run with the same entry changes nothing.
	backup, changed, err = addMCPServerToConfig(context.Background(), path, json.RawMessage(testDesktopEntry))
	if err != nil || changed || backup != "" {
		t.Fatalf("repeat: backup=%q changed=%v err=%v", backup, changed, err)
	}
}

func TestAddMCPServerToConfigCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	backup, changed, err := addMCPServerToConfig(context.Background(), path, json.RawMessage(testDesktopEntry))
	if err != nil || !changed || backup != "" {
		t.Fatalf("backup=%q changed=%v err=%v", backup, changed, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil || config["mcpServers"]["softpractice"] == nil {
		t.Fatalf("config = %s, %v", data, err)
	}
}

func TestAddMCPServerToConfigLeavesInvalidFileUntouched(t *testing.T) {
	for _, original := range []string{`{"mcpServers": `, `[]`, `{"mcpServers": []}`, `{} {}`} {
		path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
		if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := addMCPServerToConfig(context.Background(), path, json.RawMessage(testDesktopEntry)); err == nil {
			t.Fatalf("%s: setup succeeded", original)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != original {
			t.Fatalf("%s: file = %q, %v", original, data, err)
		}
		if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
			t.Fatalf("%s: backup created: %v", original, err)
		}
	}
}
