package learnercli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	credentialSchemaVersion = 1
	keyringService          = "softpractice-cli"
	credentialsDirEnv       = "SOFTPRACTICE_CREDENTIALS_DIR"
)

// Credentials contains process-local access credentials and the rotating
// refresh credential loaded from the operating system's secret store.
type Credentials struct {
	APIURL                   string
	AccessToken              string
	AccessExpiresAt          time.Time
	RefreshToken             string
	RefreshIdleExpiresAt     time.Time
	RefreshAbsoluteExpiresAt time.Time
}

type credentialMetadata struct {
	SchemaVersion            int       `json:"schema_version"`
	APIURL                   string    `json:"api_url"`
	RefreshIdleExpiresAt     time.Time `json:"refresh_idle_expires_at"`
	RefreshAbsoluteExpiresAt time.Time `json:"refresh_absolute_expires_at"`
}

type SecretStore interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

type systemKeyring struct{}

func (systemKeyring) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}

func (systemKeyring) Set(service, account, secret string) error {
	return keyring.Set(service, account, secret)
}

func (systemKeyring) Delete(service, account string) error {
	return keyring.Delete(service, account)
}

type CredentialStore struct {
	Path                 string
	Secrets              SecretStore
	KeyringAccountSuffix string
}

func DefaultCredentialStore() (CredentialStore, error) {
	root := os.Getenv(credentialsDirEnv)
	if root == "" {
		var err error
		root, err = os.UserConfigDir()
		if err != nil {
			return CredentialStore{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		root = filepath.Join(root, "softpractice")
		return CredentialStore{
			Path: filepath.Join(root, "credentials.json"), Secrets: systemKeyring{},
		}, nil
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return CredentialStore{}, fmt.Errorf("resolve %s: %w", credentialsDirEnv, err)
	}
	digest := sha256.Sum256([]byte(absoluteRoot))
	return CredentialStore{
		Path:                 filepath.Join(absoluteRoot, "credentials.json"),
		Secrets:              systemKeyring{},
		KeyringAccountSuffix: "-" + hex.EncodeToString(digest[:]),
	}, nil
}

func (s CredentialStore) Load() (Credentials, error) {
	if s.Secrets == nil {
		return Credentials{}, errors.New("credential secret store is not configured")
	}
	info, err := os.Lstat(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credentials{}, errors.New("not logged in; run `softpractice login`")
		}
		return Credentials{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Credentials{}, errors.New("credentials metadata must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Credentials{}, errors.New("credentials metadata permissions are too broad; require 0600")
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return Credentials{}, err
	}
	var metadata credentialMetadata
	if err := decodeSingleJSON(data, &metadata); err != nil {
		return Credentials{}, fmt.Errorf("decode credentials metadata: %w", err)
	}
	if metadata.SchemaVersion != credentialSchemaVersion ||
		metadata.APIURL == "" ||
		metadata.RefreshIdleExpiresAt.IsZero() ||
		metadata.RefreshAbsoluteExpiresAt.IsZero() {
		return Credentials{}, errors.New("credentials metadata is incomplete; login again")
	}
	refreshToken, err := s.Secrets.Get(keyringService, s.refreshTokenAccount(metadata.APIURL))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Credentials{}, errors.New("refresh credential is missing; run `softpractice login`")
		}
		return Credentials{}, fmt.Errorf("read refresh credential from system keyring: %w", err)
	}
	if refreshToken == "" {
		return Credentials{}, errors.New("refresh credential is empty; run `softpractice login`")
	}
	return Credentials{
		APIURL: metadata.APIURL, RefreshToken: refreshToken,
		RefreshIdleExpiresAt:     metadata.RefreshIdleExpiresAt,
		RefreshAbsoluteExpiresAt: metadata.RefreshAbsoluteExpiresAt,
	}, nil
}

func (s CredentialStore) Save(credentials Credentials) error {
	if s.Secrets == nil {
		return errors.New("credential secret store is not configured")
	}
	if credentials.APIURL == "" || credentials.RefreshToken == "" ||
		credentials.RefreshIdleExpiresAt.IsZero() ||
		credentials.RefreshAbsoluteExpiresAt.IsZero() {
		return errors.New("credentials are incomplete")
	}
	// Rotation invalidates the old refresh token at the server. Persist the new
	// secret before metadata so an interrupted write never restores the old one.
	if err := s.Secrets.Set(
		keyringService, s.refreshTokenAccount(credentials.APIURL),
		credentials.RefreshToken,
	); err != nil {
		return fmt.Errorf("write refresh credential to system keyring: %w", err)
	}
	metadata := credentialMetadata{
		SchemaVersion:            credentialSchemaVersion,
		APIURL:                   credentials.APIURL,
		RefreshIdleExpiresAt:     credentials.RefreshIdleExpiresAt,
		RefreshAbsoluteExpiresAt: credentials.RefreshAbsoluteExpiresAt,
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writePrivateAtomic(s.Path, data)
}

func (s CredentialStore) Delete(apiURL string) error {
	if s.Secrets == nil {
		return errors.New("credential secret store is not configured")
	}
	if err := s.Secrets.Delete(keyringService, s.refreshTokenAccount(apiURL)); err != nil &&
		!errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete refresh credential from system keyring: %w", err)
	}
	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete credential metadata: %w", err)
	}
	return nil
}

func writePrivateAtomic(target string, data []byte) error {
	directory := filepath.Dir(target)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".credentials-*")
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
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceFile(name, target)
}

func refreshTokenAccount(apiURL string) string {
	digest := sha256.Sum256([]byte(apiURL))
	return "refresh-" + hex.EncodeToString(digest[:])
}

func (s CredentialStore) refreshTokenAccount(apiURL string) string {
	return refreshTokenAccount(apiURL) + s.KeyringAccountSuffix
}

func decodeSingleJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
