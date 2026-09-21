package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigStoreRoundTripAndRejectsUnknownFields(t *testing.T) {
	store := configStore{Path: filepath.Join(t.TempDir(), "softpractice", "config.json")}
	want := cliConfig{
		SchemaVersion: configSchemaVersion,
		APIURL:        "https://api.example.test",
		WebURL:        "https://app.example.test",
		Language:      "ru",
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got != want {
		t.Fatalf("config = %#v, %v; want %#v", got, err, want)
	}
	if err := os.WriteFile(store.Path, []byte(`{"schema_version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("extended config was accepted: %v", err)
	}
	if err := os.WriteFile(store.Path, []byte(`{"schema_version":1}{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("config with trailing JSON was accepted: %v", err)
	}
}

func TestConfigStoreSaveReplacesAnExistingConfig(t *testing.T) {
	store := configStore{Path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.Save(cliConfig{APIURL: "https://first.example.test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(cliConfig{APIURL: "https://second.example.test"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got.APIURL != "https://second.example.test" {
		t.Fatalf("config after replacement = %#v, %v", got, err)
	}
}

func TestResolveSettingsUsesEnvironmentBeforeConfigAndDefaultsToEnglish(t *testing.T) {
	store := configStore{Path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.Save(cliConfig{
		APIURL: "https://api.example.test", WebURL: "https://app.example.test", Language: "ru",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOFTPRACTICE_API_URL", "https://override.example.test")
	t.Setenv("SOFTPRACTICE_WEB_URL", "https://override-web.example.test")
	t.Setenv("SOFTPRACTICE_LANGUAGE", "en")
	settings, err := resolveSettings(store)
	if err != nil {
		t.Fatal(err)
	}
	if settings.APIURL != "https://override.example.test" || settings.WebURL != "https://override-web.example.test" || settings.Language != languageEnglish {
		t.Fatalf("settings = %#v", settings)
	}
	t.Setenv("SOFTPRACTICE_LANGUAGE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "und")
	if language := detectSystemLanguage(); language != languageEnglish {
		t.Fatalf("fallback language = %q, want en", language)
	}
}

func TestResolveSettingsLetsEnvironmentMaskInvalidStoredValues(t *testing.T) {
	store := configStore{Path: filepath.Join(t.TempDir(), "config.json")}
	if err := os.WriteFile(store.Path, []byte(`{
  "schema_version": 1,
  "api_url": "http://not-secure.example.test",
  "web_url": "http://not-secure.example.test",
  "language": "not-a-language"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOFTPRACTICE_API_URL", "https://api.example.test")
	t.Setenv("SOFTPRACTICE_WEB_URL", "https://app.example.test")
	t.Setenv("SOFTPRACTICE_LANGUAGE", "en")
	settings, err := resolveSettings(store)
	if err != nil {
		t.Fatal(err)
	}
	if settings.APIURL != "https://api.example.test" || settings.WebURL != "https://app.example.test" || settings.Language != languageEnglish {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestRuntimeLanguageRejectsUnmaskedInvalidStoredValue(t *testing.T) {
	if err := validateRuntimeLanguage(language("not-a-language")); err == nil {
		t.Fatal("invalid runtime language was accepted")
	}
}

func TestConfigCommandLetsFlagsMaskInvalidStoredValues(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(configDirectoryEnv, directory)
	t.Setenv("SOFTPRACTICE_WEB_URL", "https://app.example.test")
	path := filepath.Join(directory, "config.json")
	if err := os.WriteFile(path, []byte(`{
  "schema_version": 1,
  "api_url": "http://not-secure.example.test",
  "language": "not-a-language"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output, errors strings.Builder
	err := run(context.Background(), []string{
		"--api", "https://api.example.test", "--lang", "en", "config",
	}, strings.NewReader(""), &output, &errors)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"API: https://api.example.test", "Web: https://app.example.test", "Language: en"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("config output %q does not contain %q", output.String(), expected)
		}
	}
}

func TestHelpUsesSavedLanguageWithoutRequiringValidRuntimeSettings(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(configDirectoryEnv, directory)
	t.Setenv("SOFTPRACTICE_LANGUAGE", "")
	t.Setenv("LC_ALL", "ru_RU.UTF-8")
	store := configStore{Path: filepath.Join(directory, "config.json")}
	if err := store.Save(cliConfig{Language: "en"}); err != nil {
		t.Fatal(err)
	}
	var output, errorOutput strings.Builder
	err := run(context.Background(), []string{"--help"}, strings.NewReader(""), &output, &errorOutput)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
	if !strings.Contains(errorOutput.String(), "Usage:") {
		t.Fatalf("help output = %q", errorOutput.String())
	}
}

func TestSetAndConfigCommandsUseTheUserConfigDirectory(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(configDirectoryEnv, directory)
	t.Setenv("SOFTPRACTICE_API_URL", "")
	t.Setenv("SOFTPRACTICE_WEB_URL", "")
	t.Setenv("SOFTPRACTICE_LANGUAGE", "en")
	var output, errors strings.Builder
	if err := run(context.Background(), []string{"set", "api-url", "https://api.example.test"}, strings.NewReader(""), &output, &errors); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"config"}, strings.NewReader(""), &output, &errors); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "https://api.example.test") || !strings.Contains(output.String(), filepath.Join(directory, "config.json")) {
		t.Fatalf("config output = %q", output.String())
	}
}

func TestConfigShowsProjectAutoChecks(t *testing.T) {
	root := createPinnedLinkedGitRepository(t, "00000000-0000-4000-8000-000000000001", "pa-foundation-02", 1)
	t.Chdir(root)
	t.Setenv(configDirectoryEnv, t.TempDir())
	t.Setenv("SOFTPRACTICE_LANGUAGE", "en")
	for _, test := range []struct {
		name    string
		enabled bool
	}{
		{name: "disabled", enabled: false},
		{name: "enabled", enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := gitOutput(context.Background(), root, "config", "--local", autoChecksKey, fmt.Sprintf("%t", test.enabled)); err != nil {
				t.Fatal(err)
			}
			var output, errors strings.Builder
			if err := run(context.Background(), []string{"config"}, strings.NewReader(""), &output, &errors); err != nil {
				t.Fatal(err)
			}
			if expected := fmt.Sprintf("Auto-checks: %t", test.enabled); !strings.Contains(output.String(), expected) {
				t.Fatalf("config output %q does not contain %q", output.String(), expected)
			}
		})
	}
}

func TestConfigExplainsAutoChecksOutsideLinkedProject(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(configDirectoryEnv, t.TempDir())
	t.Setenv("SOFTPRACTICE_LANGUAGE", "ru")
	var output, errors strings.Builder
	if err := run(context.Background(), []string{"config"}, strings.NewReader(""), &output, &errors); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Автопроверки: недоступны (запустите команду в связанном проекте)") {
		t.Fatalf("config output = %q", output.String())
	}
}

func TestConfigExplainsWhenEnvironmentOverridesSavedURLs(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(configDirectoryEnv, directory)
	t.Setenv("SOFTPRACTICE_API_URL", "https://api.example.test")
	t.Setenv("SOFTPRACTICE_WEB_URL", "https://app.example.test")
	store := configStore{Path: filepath.Join(directory, "config.json")}
	if err := store.Save(cliConfig{APIURL: "https://softpractice.ru", WebURL: "https://softpractice.ru"}); err != nil {
		t.Fatal(err)
	}
	var output, errors strings.Builder
	if err := run(context.Background(), []string{"config"}, strings.NewReader(""), &output, &errors); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"API: https://api.example.test (saved: https://softpractice.ru, overridden by SOFTPRACTICE_API_URL)",
		"Web: https://app.example.test (saved: https://softpractice.ru, overridden by SOFTPRACTICE_WEB_URL)",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("config output %q does not contain %q", output.String(), expected)
		}
	}
}

func TestSetRepairsAnInvalidRegularConfiguration(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(configDirectoryEnv, directory)
	t.Setenv("SOFTPRACTICE_API_URL", "http://invalid config value")
	t.Setenv("SOFTPRACTICE_LANGUAGE", "not-a-language")
	path := filepath.Join(directory, "config.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1}{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var helpOutput, output, errors strings.Builder
	if err := run(context.Background(), []string{"help"}, strings.NewReader(""), &helpOutput, &errors); err != nil {
		t.Fatalf("help with invalid settings: %v", err)
	}
	if !strings.Contains(helpOutput.String(), "Usage:") {
		t.Fatalf("help output = %q", helpOutput.String())
	}
	if err := run(context.Background(), []string{"set", "lang", "ru"}, strings.NewReader(""), &output, &errors); err != nil {
		t.Fatalf("set with invalid settings: %v", err)
	}
	store := configStore{Path: path}
	config, err := store.Load()
	if err != nil || config.Language != "ru" {
		t.Fatalf("repaired config = %#v, %v", config, err)
	}
	if !strings.Contains(output.String(), "invalid configuration was replaced") {
		t.Fatalf("set output = %q", output.String())
	}
}

func TestRussianPromptKeepsStandardYNAnswers(t *testing.T) {
	ctx := withSettings(context.Background(), runtimeSettings{Language: languageRussian})
	var output strings.Builder
	confirmed, err := confirmSubmission(ctx, strings.NewReader("n\n"), &output)
	if err != nil || confirmed {
		t.Fatalf("Russian confirmation = %t, %v", confirmed, err)
	}
	if !strings.Contains(output.String(), "[Y/n]") || !strings.Contains(output.String(), "Отправить") {
		t.Fatalf("Russian prompt = %q", output.String())
	}
}
