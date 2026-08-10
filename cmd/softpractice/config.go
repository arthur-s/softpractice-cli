package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	configSchemaVersion = 1
	configDirectoryEnv  = "SOFTPRACTICE_CONFIG_DIR"
)

type cliConfig struct {
	SchemaVersion int    `json:"schema_version"`
	APIURL        string `json:"api_url,omitempty"`
	WebURL        string `json:"web_url,omitempty"`
	Language      string `json:"language,omitempty"`
}

type configStore struct{ Path string }

func defaultConfigStore() (configStore, error) {
	directory := strings.TrimSpace(os.Getenv(configDirectoryEnv))
	if directory == "" {
		var err error
		directory, err = os.UserConfigDir()
		if err != nil {
			return configStore{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		directory = filepath.Join(directory, "softpractice")
	} else {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return configStore{}, fmt.Errorf("resolve %s: %w", configDirectoryEnv, err)
		}
		directory = absolute
	}
	return configStore{Path: filepath.Join(directory, "config.json")}, nil
}

func (s configStore) Load() (cliConfig, error) {
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return cliConfig{SchemaVersion: configSchemaVersion}, nil
	}
	if err != nil {
		return cliConfig{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return cliConfig{}, errors.New("CLI configuration must be a regular file")
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return cliConfig{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config cliConfig
	if err := decoder.Decode(&config); err != nil {
		return cliConfig{}, fmt.Errorf("decode CLI configuration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return cliConfig{}, errors.New("CLI configuration contains trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return cliConfig{}, fmt.Errorf("decode CLI configuration: %w", err)
	}
	if config.SchemaVersion != configSchemaVersion {
		return cliConfig{}, errors.New("CLI configuration has an unsupported schema version")
	}
	return config, nil
}

// LoadForUpdate returns a blank configuration when a regular configuration file
// cannot be decoded. This lets `softpractice set` repair a bad user-created or
// interrupted configuration without following or replacing unsafe file types.
func (s configStore) LoadForUpdate() (cliConfig, bool, error) {
	config, err := s.Load()
	if err == nil {
		return config, false, nil
	}
	info, statErr := os.Lstat(s.Path)
	if errors.Is(statErr, os.ErrNotExist) {
		return cliConfig{SchemaVersion: configSchemaVersion}, false, nil
	}
	if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return cliConfig{}, false, err
	}
	return cliConfig{SchemaVersion: configSchemaVersion}, true, nil
}

func (s configStore) Save(config cliConfig) error {
	config.SchemaVersion = configSchemaVersion
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.Path), ".config-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceConfigFile(name, s.Path)
}

func validateWebURL(raw string) error {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("web URL is invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("web URL must not contain credentials, a query, or a fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackURLHost(parsed.Hostname())) {
		return errors.New("web URL must use HTTPS except for loopback development")
	}
	return nil
}

func isLoopbackURLHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
