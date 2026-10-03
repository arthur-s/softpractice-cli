package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestMCPRemovePreservesOtherSettingsAndBackup(t *testing.T) {
	tests := []struct {
		name, original string
		remove         func(context.Context, string) (string, bool, error)
		verify         func(*testing.T, []byte)
	}{
		{"Claude", `{"preferences":{"theme":"dark"},"mcpServers":{"other":{"command":"other"},"softpractice":{"command":"sp"}},"zeta":1}`,
			func(ctx context.Context, path string) (string, bool, error) {
				return updateMCPServerConfig(ctx, path, nil)
			},
			func(t *testing.T, data []byte) {
				var config map[string]any
				if err := json.Unmarshal(data, &config); err != nil {
					t.Fatal(err)
				}
				servers := config["mcpServers"].(map[string]any)
				if len(servers) != 1 || servers["other"].(map[string]any)["command"] != "other" || config["preferences"].(map[string]any)["theme"] != "dark" || config["zeta"] != float64(1) {
					t.Fatalf("settings: %s", data)
				}
			}},
		{"Codex", "# preferences\nmodel = 'example'\n\n[mcp_servers.other]\ncommand = 'other' # keep\n\n[mcp_servers.softpractice]\ncommand = 'sp'\n\n[mcp_servers.softpractice.env]\nLANG = 'en'\n\n# trusted project\n[projects.'/lesson']\ntrust_level = 'trusted'\n",
			func(ctx context.Context, path string) (string, bool, error) {
				return updateCodexMCPServerConfig(ctx, path, nil)
			},
			func(t *testing.T, data []byte) {
				var config map[string]any
				if err := toml.Unmarshal(data, &config); err != nil {
					t.Fatal(err)
				}
				servers := config["mcp_servers"].(map[string]any)
				if len(servers) != 1 || servers["other"].(map[string]any)["command"] != "other" || config["model"] != "example" || !bytes.Contains(data, []byte("command = 'other' # keep")) || !bytes.Contains(data, []byte("# trusted project\n[projects.'/lesson']")) {
					t.Fatalf("settings: %s", data)
				}
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(path, []byte(test.original), 0o600); err != nil {
				t.Fatal(err)
			}
			backup, changed, err := test.remove(context.Background(), path)
			if err != nil || !changed || backup != path+".bak" {
				t.Fatalf("remove: %q %v %v", backup, changed, err)
			}
			saved, _ := os.ReadFile(backup)
			if string(saved) != test.original {
				t.Fatalf("backup: %s", saved)
			}
			updated, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			test.verify(t, updated)
			backup, changed, err = test.remove(context.Background(), path)
			if err != nil || changed || backup != "" {
				t.Fatalf("repeat: %q %v %v", backup, changed, err)
			}
			repeated, _ := os.ReadFile(path)
			if !bytes.Equal(updated, repeated) {
				t.Fatal("repeat changed settings")
			}
			saved, _ = os.ReadFile(path + ".bak")
			if string(saved) != test.original {
				t.Fatal("repeat replaced backup")
			}
		})
	}
}

func TestMCPRemoveNoConnectionDoesNotCreateOrRewriteFiles(t *testing.T) {
	for _, format := range []string{"json", "toml"} {
		t.Run(format, func(t *testing.T) {
			remove := func(path string) (string, bool, error) {
				if format == "json" {
					return updateMCPServerConfig(context.Background(), path, nil)
				}
				return updateCodexMCPServerConfig(context.Background(), path, nil)
			}
			path := filepath.Join(t.TempDir(), "missing", "config")
			if backup, changed, err := remove(path); err != nil || changed || backup != "" {
				t.Fatalf("missing: %q %v %v", backup, changed, err)
			}
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("created directory: %v", err)
			}
			path = filepath.Join(t.TempDir(), "config")
			original := "{\"mcpServers\":{\"other\":{\"command\":\"other\"}}}"
			if format == "toml" {
				original = "[mcp_servers.other]\ncommand = 'other'\n"
			}
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if backup, changed, err := remove(path); err != nil || changed || backup != "" {
				t.Fatalf("absent: %q %v %v", backup, changed, err)
			}
			data, _ := os.ReadFile(path)
			if string(data) != original {
				t.Fatal("changed unrelated settings")
			}
		})
	}
}

func TestCodexRemoveOnlyServerAndRejectsUnsafeLayouts(t *testing.T) {
	for _, original := range []string{"[mcp_servers.softpractice]\ncommand = 'sp'\n", "[mcp_servers]\n[mcp_servers.softpractice]\ncommand = 'sp'\n"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		_ = os.WriteFile(path, []byte(original), 0o600)
		if _, changed, err := updateCodexMCPServerConfig(context.Background(), path, nil); err != nil || !changed {
			t.Fatalf("only server: %v %v", changed, err)
		}
		data, _ := os.ReadFile(path)
		if bytes.Contains(data, []byte("softpractice")) {
			t.Fatalf("server remains: %s", data)
		}
	}
	for _, original := range []string{"[broken", "mcp_servers = 42", "mcp_servers.softpractice = {command = 'sp'}", "[mcp_servers]\nsoftpractice = 42"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		_ = os.WriteFile(path, []byte(original), 0o600)
		if _, changed, err := updateCodexMCPServerConfig(context.Background(), path, nil); err == nil || changed {
			t.Fatalf("unsafe layout: %v %v", changed, err)
		}
		data, _ := os.ReadFile(path)
		if string(data) != original {
			t.Fatal("changed unsupported settings")
		}
	}
}

func TestMCPRemoveCommandOutsideProject(t *testing.T) {
	t.Setenv(configDirectoryEnv, t.TempDir())
	t.Setenv("SOFTPRACTICE_CREDENTIALS_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	path, err := codexConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := addCodexMCPServerToConfig(context.Background(), path, codexServerEntry("/opt/sp")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"mcp", "remove", "codex-desktop"}, strings.NewReader(""), &output, &output); err != nil || !strings.Contains(output.String(), "Codex") {
		t.Fatalf("command: %v, %s", err, output.String())
	}
	data, _ := os.ReadFile(path)
	if bytes.Contains(data, []byte("softpractice")) {
		t.Fatalf("connection remains: %s", data)
	}
}

func TestClaudeRemoveRejectsInvalidConfig(t *testing.T) {
	for _, original := range []string{`[]`, `{"mcpServers": []}`, `{"mcpServers":`, `{} {}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := updateMCPServerConfig(context.Background(), path, nil); err == nil || changed {
			t.Fatalf("invalid config: %v %v", changed, err)
		}
		saved, _ := os.ReadFile(path)
		if string(saved) != original {
			t.Fatal("modified invalid config")
		}
		if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
			t.Fatal("created backup on failure")
		}
	}
}
