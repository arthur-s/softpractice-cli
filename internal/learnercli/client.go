package learnercli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Store   CredentialStore

	credentialsMu sync.Mutex
	credentials   *Credentials
}

func NewClient(baseURL string, store CredentialStore) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("API URL is invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("API URL must not contain credentials, a query, or a fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("API URL must use HTTPS except for loopback development")
	}
	return &Client{
		BaseURL: baseURL, Store: store,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

type DeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresInSeconds        int    `json:"expires_in_seconds"`
	IntervalSeconds         int    `json:"interval_seconds"`
}

type tokenSet struct {
	TokenType                string    `json:"token_type"`
	AccessToken              string    `json:"access_token"`
	ExpiresInSeconds         int       `json:"expires_in_seconds"`
	RefreshToken             string    `json:"refresh_token"`
	RefreshIdleExpiresAt     time.Time `json:"refresh_idle_expires_at"`
	RefreshAbsoluteExpiresAt time.Time `json:"refresh_absolute_expires_at"`
	Scopes                   []string  `json:"scopes"`
}

type apiError struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		SupportID string `json:"support_id"`
	} `json:"error"`
	NextPollSeconds int    `json:"next_poll_seconds"`
	RequestID       string `json:"request_id"`
}

func (c *Client) StartLogin(ctx context.Context, clientName string) (DeviceAuthorization, error) {
	var result DeviceAuthorization
	err := c.jsonRequest(ctx, http.MethodPost, "/v1/auth/device/authorizations", "", map[string]any{
		"client_name": clientName,
		"scopes":      []string{"profile:read", "workspace:read", "submission:write"},
	}, &result)
	return result, err
}

func (c *Client) PollLogin(
	ctx context.Context,
	authorization DeviceAuthorization,
	notify func(interval time.Duration) error,
) (Credentials, error) {
	interval := time.Duration(authorization.IntervalSeconds) * time.Second
	expires := time.Now().Add(time.Duration(authorization.ExpiresInSeconds) * time.Second)
	for {
		if !time.Now().Before(expires) {
			return Credentials{}, errors.New("device authorization expired")
		}
		if err := notify(interval); err != nil {
			return Credentials{}, err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Credentials{}, ctx.Err()
		case <-timer.C:
		}
		status, body, headers, err := c.rawJSON(
			ctx, http.MethodPost, "/v1/auth/device/tokens", "",
			map[string]string{"device_code": authorization.DeviceCode},
		)
		if err != nil {
			return Credentials{}, err
		}
		if status == http.StatusOK {
			var tokens tokenSet
			if err := decodeJSON(body, &tokens); err != nil {
				return Credentials{}, err
			}
			now := time.Now()
			return Credentials{
				APIURL: c.BaseURL, AccessToken: tokens.AccessToken,
				AccessExpiresAt:          now.Add(time.Duration(tokens.ExpiresInSeconds) * time.Second),
				RefreshToken:             tokens.RefreshToken,
				RefreshIdleExpiresAt:     tokens.RefreshIdleExpiresAt,
				RefreshAbsoluteExpiresAt: tokens.RefreshAbsoluteExpiresAt,
			}, nil
		}
		var response apiError
		_ = decodeJSON(body, &response)
		switch response.Error.Code {
		case "authorization_pending", "slow_down":
			next := response.NextPollSeconds
			if header := headers.Get("Retry-After"); header != "" {
				if parsed, parseErr := strconv.Atoi(header); parseErr == nil {
					next = parsed
				}
			}
			if next < 1 {
				return Credentials{}, errors.New("server returned an invalid polling interval")
			}
			interval = time.Duration(next) * time.Second
		case "access_denied", "expired_token":
			return Credentials{}, errors.New(response.Error.Message)
		default:
			return Credentials{}, fmt.Errorf("device login failed: %s", response.Error.Message)
		}
	}
}

func (c *Client) AuthorizedJSON(
	ctx context.Context,
	method, path string,
	request any,
	response any,
) error {
	credentials, err := c.validCredentials(ctx)
	if err != nil {
		return err
	}
	err = c.jsonRequest(ctx, method, path, credentials.AccessToken, request, response)
	if err == nil {
		return nil
	}
	var statusError *HTTPError
	if !errors.As(err, &statusError) || statusError.Status != http.StatusUnauthorized {
		return err
	}
	credentials, err = c.refresh(ctx, credentials)
	if err != nil {
		return err
	}
	return c.jsonRequest(ctx, method, path, credentials.AccessToken, request, response)
}

func (c *Client) Submit(
	ctx context.Context,
	path string,
	archivePath string,
	headers map[string]string,
	response any,
) error {
	credentials, err := c.validCredentials(ctx)
	if err != nil {
		return err
	}
	err = c.submitOnce(ctx, credentials.AccessToken, path, archivePath, headers, response)
	var statusError *HTTPError
	if !errors.As(err, &statusError) || statusError.Status != http.StatusUnauthorized {
		return err
	}
	credentials, err = c.refresh(ctx, credentials)
	if err != nil {
		return err
	}
	return c.submitOnce(ctx, credentials.AccessToken, path, archivePath, headers, response)
}

func (c *Client) SaveCredentials(credentials Credentials) error {
	if credentials.APIURL != c.BaseURL {
		return errors.New("credentials belong to a different API URL")
	}
	if err := c.Store.Save(credentials); err != nil {
		return err
	}
	c.cacheCredentials(credentials)
	return nil
}

func (c *Client) Logout(ctx context.Context) error {
	credentials, err := c.Store.Load()
	if err != nil {
		return err
	}
	if credentials.APIURL != c.BaseURL {
		return errors.New("stored credentials belong to a different API URL; login again")
	}
	revokeErr := c.jsonRequest(
		ctx,
		http.MethodPost,
		"/v1/auth/tokens/revoke",
		"",
		map[string]string{"refresh_token": credentials.RefreshToken},
		nil,
	)
	deleteErr := c.Store.Delete(credentials.APIURL)
	c.credentialsMu.Lock()
	c.credentials = nil
	c.credentialsMu.Unlock()
	if revokeErr != nil || deleteErr != nil {
		return fmt.Errorf("logout CLI: %w", errors.Join(revokeErr, deleteErr))
	}
	return nil
}

func (c *Client) validCredentials(ctx context.Context) (Credentials, error) {
	c.credentialsMu.Lock()
	var credentials Credentials
	if c.credentials != nil {
		credentials = *c.credentials
	}
	c.credentialsMu.Unlock()
	if credentials.RefreshToken == "" {
		loaded, err := c.Store.Load()
		if err != nil {
			return Credentials{}, err
		}
		credentials = loaded
	}
	if credentials.APIURL != c.BaseURL {
		return Credentials{}, errors.New("stored credentials belong to a different API URL; login again")
	}
	if time.Now().Add(30 * time.Second).Before(credentials.AccessExpiresAt) {
		return credentials, nil
	}
	now := time.Now()
	if !credentials.RefreshIdleExpiresAt.IsZero() &&
		!now.Before(credentials.RefreshIdleExpiresAt) {
		return Credentials{}, errors.New("CLI login expired; run `softpractice login`")
	}
	if !credentials.RefreshAbsoluteExpiresAt.IsZero() &&
		!now.Before(credentials.RefreshAbsoluteExpiresAt) {
		return Credentials{}, errors.New("CLI login expired; run `softpractice login`")
	}
	return c.refresh(ctx, credentials)
}

func (c *Client) refresh(ctx context.Context, credentials Credentials) (Credentials, error) {
	var tokens tokenSet
	if err := c.jsonRequest(ctx, http.MethodPost, "/v1/auth/tokens/refresh", "", map[string]string{
		"refresh_token": credentials.RefreshToken,
	}, &tokens); err != nil {
		return Credentials{}, fmt.Errorf("refresh CLI login: %w", err)
	}
	credentials.AccessToken = tokens.AccessToken
	credentials.AccessExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresInSeconds) * time.Second)
	credentials.RefreshToken = tokens.RefreshToken
	credentials.RefreshIdleExpiresAt = tokens.RefreshIdleExpiresAt
	credentials.RefreshAbsoluteExpiresAt = tokens.RefreshAbsoluteExpiresAt
	if err := c.Store.Save(credentials); err != nil {
		return Credentials{}, err
	}
	c.cacheCredentials(credentials)
	return credentials, nil
}

func (c *Client) cacheCredentials(credentials Credentials) {
	c.credentialsMu.Lock()
	defer c.credentialsMu.Unlock()
	c.credentials = &credentials
}

func (c *Client) submitOnce(
	ctx context.Context,
	token, requestPath, archivePath string,
	headers map[string]string,
	response any,
) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.BaseURL+requestPath, archive,
	)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/gzip")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	result, err := c.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer result.Body.Close()
	body, err := io.ReadAll(io.LimitReader(result.Body, 2<<20))
	if err != nil {
		return err
	}
	if result.StatusCode != http.StatusCreated && result.StatusCode != http.StatusOK {
		return parseHTTPError(result.StatusCode, body)
	}
	return decodeJSON(body, response)
}

func (c *Client) jsonRequest(
	ctx context.Context,
	method, requestPath, token string,
	input, output any,
) error {
	status, body, _, err := c.rawJSON(ctx, method, requestPath, token, input)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return parseHTTPError(status, body)
	}
	if output == nil || status == http.StatusNoContent {
		return nil
	}
	return decodeJSON(body, output)
}

func (c *Client) rawJSON(
	ctx context.Context,
	method, requestPath, token string,
	input any,
) (int, []byte, http.Header, error) {
	var requestBody io.Reader = http.NoBody
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return 0, nil, nil, err
		}
		requestBody = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(
		ctx, method, c.BaseURL+requestPath, requestBody,
	)
	if err != nil {
		return 0, nil, nil, err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	result, err := c.HTTP.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer result.Body.Close()
	body, err := io.ReadAll(io.LimitReader(result.Body, 2<<20))
	return result.StatusCode, body, result.Header.Clone(), err
}

type HTTPError struct {
	Status    int
	Code      string
	Message   string
	SupportID string
}

type StarterArchive struct {
	SHA256 string
	Size   int64
	Ref    string
}

type CourseUpdateArchive struct {
	SHA256            string
	Size              int64
	Ref               string
	BaseRevisionID    string
	BaseContentSHA256 string
}

// RevisionArchive describes a ZIP containing one learner-owned immutable
// submission revision.
type RevisionArchive struct {
	Size int64
}

// DownloadStarter streams an authenticated starter bundle to destination and
// validates the delivery metadata before callers unpack it.
func (c *Client) DownloadStarter(ctx context.Context, path string, destination io.Writer) (StarterArchive, error) {
	credentials, err := c.validCredentials(ctx)
	if err != nil {
		return StarterArchive{}, err
	}
	archive, err := c.downloadStarterOnce(ctx, credentials.AccessToken, path, destination)
	var statusError *HTTPError
	if !errors.As(err, &statusError) || statusError.Status != http.StatusUnauthorized {
		return archive, err
	}
	credentials, err = c.refresh(ctx, credentials)
	if err != nil {
		return StarterArchive{}, err
	}
	return c.downloadStarterOnce(ctx, credentials.AccessToken, path, destination)
}

func (c *Client) downloadStarterOnce(ctx context.Context, token, path string, destination io.Writer) (StarterArchive, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return StarterArchive{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return StarterArchive{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if readErr != nil {
			return StarterArchive{}, readErr
		}
		return StarterArchive{}, parseHTTPError(response.StatusCode, body)
	}
	size, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if err != nil || size < 1 || size > 10<<20 {
		return StarterArchive{}, errors.New("starter response has an invalid Content-Length")
	}
	checksum := response.Header.Get("X-Softpractice-Starter-SHA256")
	if len(checksum) != 64 || strings.Trim(checksum, "0123456789abcdef") != "" {
		return StarterArchive{}, errors.New("starter response has an invalid checksum")
	}
	written, err := io.Copy(destination, io.LimitReader(response.Body, size+1))
	if err != nil || written != size {
		return StarterArchive{}, errors.New("starter download was incomplete")
	}
	return StarterArchive{SHA256: checksum, Size: size, Ref: response.Header.Get("X-Softpractice-Starter-Ref")}, nil
}

func (c *Client) DownloadCourseUpdate(ctx context.Context, path string, destination io.Writer) (CourseUpdateArchive, error) {
	credentials, err := c.validCredentials(ctx)
	if err != nil {
		return CourseUpdateArchive{}, err
	}
	archive, err := c.downloadCourseUpdateOnce(ctx, credentials.AccessToken, path, destination)
	var statusError *HTTPError
	if !errors.As(err, &statusError) || statusError.Status != http.StatusUnauthorized {
		return archive, err
	}
	credentials, err = c.refresh(ctx, credentials)
	if err != nil {
		return CourseUpdateArchive{}, err
	}
	return c.downloadCourseUpdateOnce(ctx, credentials.AccessToken, path, destination)
}

func (c *Client) downloadCourseUpdateOnce(ctx context.Context, token, path string, destination io.Writer) (CourseUpdateArchive, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return CourseUpdateArchive{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return CourseUpdateArchive{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if readErr != nil {
			return CourseUpdateArchive{}, readErr
		}
		return CourseUpdateArchive{}, parseHTTPError(response.StatusCode, body)
	}
	size, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if err != nil || size < 1 || size > 10<<20 {
		return CourseUpdateArchive{}, errors.New("course update response has an invalid Content-Length")
	}
	checksum := response.Header.Get("X-Softpractice-Course-Update-SHA256")
	if len(checksum) != 64 || strings.Trim(checksum, "0123456789abcdef") != "" {
		return CourseUpdateArchive{}, errors.New("course update response has an invalid checksum")
	}
	written, err := io.Copy(destination, io.LimitReader(response.Body, size+1))
	if err != nil || written != size {
		return CourseUpdateArchive{}, errors.New("course update download was incomplete")
	}
	return CourseUpdateArchive{
		SHA256: checksum, Size: size, Ref: response.Header.Get("X-Softpractice-Course-Update-Ref"),
		BaseRevisionID:    response.Header.Get("X-Softpractice-Course-Update-Base-Revision-ID"),
		BaseContentSHA256: response.Header.Get("X-Softpractice-Course-Update-Base-Content-SHA256"),
	}, nil
}

// DownloadRevisionArchive streams one learner-owned submission revision as a
// ZIP. The archive is deliberately opaque to the client: the server keeps the
// object-storage details and evaluator-only data out of this public response.
func (c *Client) DownloadRevisionArchive(ctx context.Context, path string, destination io.Writer) (RevisionArchive, error) {
	credentials, err := c.validCredentials(ctx)
	if err != nil {
		return RevisionArchive{}, err
	}
	archive, err := c.downloadRevisionArchiveOnce(ctx, credentials.AccessToken, path, destination)
	var statusError *HTTPError
	if !errors.As(err, &statusError) || statusError.Status != http.StatusUnauthorized {
		return archive, err
	}
	credentials, err = c.refresh(ctx, credentials)
	if err != nil {
		return RevisionArchive{}, err
	}
	return c.downloadRevisionArchiveOnce(ctx, credentials.AccessToken, path, destination)
}

func (c *Client) downloadRevisionArchiveOnce(ctx context.Context, token, path string, destination io.Writer) (RevisionArchive, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return RevisionArchive{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return RevisionArchive{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if readErr != nil {
			return RevisionArchive{}, readErr
		}
		return RevisionArchive{}, parseHTTPError(response.StatusCode, body)
	}
	if response.Header.Get("Content-Type") != "application/zip" {
		return RevisionArchive{}, errors.New("revision response has an unexpected Content-Type")
	}
	size, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if err != nil || size < 1 || size > 10<<20 {
		return RevisionArchive{}, errors.New("revision response has an invalid Content-Length")
	}
	written, err := io.Copy(destination, io.LimitReader(response.Body, size+1))
	if err != nil || written != size {
		return RevisionArchive{}, errors.New("revision download was incomplete")
	}
	return RevisionArchive{Size: size}, nil
}

func (e *HTTPError) Error() string {
	if e.SupportID != "" {
		return fmt.Sprintf("API %d %s (support %s): %s", e.Status, e.Code, e.SupportID, e.Message)
	}
	return fmt.Sprintf("API %d %s: %s", e.Status, e.Code, e.Message)
}

func parseHTTPError(status int, body []byte) error {
	var response apiError
	if err := decodeJSON(body, &response); err != nil {
		return &HTTPError{Status: status, Code: "invalid_response", Message: http.StatusText(status)}
	}
	return &HTTPError{
		Status: status, Code: response.Error.Code, Message: response.Error.Message,
		SupportID: response.Error.SupportID,
	}
}

// decodeJSON reads one API response. Unknown fields are ignored on purpose:
// a learner runs whichever CLI version they installed, so every published
// version has to keep working when the API adds a field to a response it
// already serves. Rejecting those fields would turn an additive server change
// into a broken `update` for everyone who has not upgraded, and it never
// caught the opposite failure anyway, because a field the server stops sending
// decodes as a zero value either way. Locally owned files stay strict: they
// are written by this CLI, so an unknown key there is a real defect.
func decodeJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode API response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("decode API response: trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode API response: %w", err)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
