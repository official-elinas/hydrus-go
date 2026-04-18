// Package daemonclient provides a thin HTTP client for the local hydrusd daemon.
package daemonclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const userAgent = "hydrus-desktop-prototype/0.1"

// Client talks to hydrusd over HTTP/JSON.
type Client struct {
	httpClient *http.Client
	baseURL    string
	accessKey  string
	sessionKey string
}

// VerifyAccessKeyResponse is the bootstrap access-key verification payload.
type VerifyAccessKeyResponse struct {
	Name              string `json:"name"`
	HumanDescription  string `json:"human_description"`
	PermitsEverything bool   `json:"permits_everything"`
}

// RecentPage is one page of recent local files.
type RecentPage struct {
	Offset  int          `json:"offset"`
	Limit   int          `json:"limit"`
	HasMore bool         `json:"has_more"`
	Items   []RecentItem `json:"items"`
}

// RecentItem is one file tile returned by the thin-client browse endpoint.
type RecentItem struct {
	FileID       int64  `json:"file_id"`
	Hash         string `json:"hash"`
	MIME         string `json:"mime"`
	Width        *int64 `json:"width,omitempty"`
	Height       *int64 `json:"height,omitempty"`
	ImportedAtMS *int64 `json:"imported_at_ms,omitempty"`
	HasThumbnail bool   `json:"has_thumbnail"`
	ContentURL   string `json:"content_url"`
	ThumbnailURL string `json:"thumbnail_url"`
	MetadataURL  string `json:"metadata_url"`
}

// FileMetadata is the selected-file metadata subset shown by the prototype UI.
type FileMetadata struct {
	FileID    int64  `json:"file_id"`
	Hash      string `json:"hash"`
	MIME      string `json:"mime"`
	Size      int64  `json:"size"`
	Width     *int64 `json:"width,omitempty"`
	Height    *int64 `json:"height,omitempty"`
	IsLocal   bool   `json:"is_local"`
	IsTrashed bool   `json:"is_trashed"`
	IsDeleted bool   `json:"is_deleted"`
}

// ImportResult is the public local-path import response payload.
type ImportResult struct {
	FileID                    int64  `json:"file_id"`
	Hash                      string `json:"hash"`
	AlreadyImported           bool   `json:"already_imported"`
	ManagedFileAlreadyPresent bool   `json:"managed_file_already_present"`
}

// TrashResult is the public file trash response payload.
type TrashResult struct {
	FileID            int64  `json:"file_id"`
	Trashed           bool   `json:"trashed"`
	RemovedFromRecent bool   `json:"removed_from_recent"`
	State             string `json:"state"`
}

type metadataResponse struct {
	Metadata []FileMetadata `json:"metadata"`
}

type sessionResponse struct {
	SessionKey string `json:"session_key"`
}

// New constructs a daemon client with a modest request timeout.
func New() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

// SetConnection updates the daemon base URL and access key and clears any prior
// session key.
func (c *Client) SetConnection(baseURL string, accessKey string) error {
	normalizedURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return err
	}

	c.baseURL = normalizedURL
	c.accessKey = strings.TrimSpace(accessKey)
	c.sessionKey = ""
	return nil
}

// BaseURL returns the configured daemon base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// AccessKey returns the configured daemon access key.
func (c *Client) AccessKey() string {
	return c.accessKey
}

// VerifyAccessKey verifies the configured access key with hydrusd.
func (c *Client) VerifyAccessKey(ctx context.Context) (VerifyAccessKeyResponse, error) {
	var response VerifyAccessKeyResponse
	if err := c.doJSON(ctx, http.MethodGet, "/verify_access_key", nil, false, &response); err != nil {
		return VerifyAccessKeyResponse{}, err
	}

	return response, nil
}

// CreateSession creates a session key for follow-up daemon requests.
func (c *Client) CreateSession(ctx context.Context) (string, error) {
	var response sessionResponse
	if err := c.doJSON(ctx, http.MethodGet, "/session_key", nil, false, &response); err != nil {
		return "", err
	}

	c.sessionKey = strings.TrimSpace(response.SessionKey)
	if c.sessionKey == "" {
		return "", fmt.Errorf("daemon returned an empty session key")
	}

	return c.sessionKey, nil
}

// ListRecent loads one recent-files browse page.
func (c *Client) ListRecent(
	ctx context.Context,
	offset int,
	limit int,
) (RecentPage, error) {
	path := "/v1/library/recent?offset=" + strconv.Itoa(offset) + "&limit=" + strconv.Itoa(limit)
	var page RecentPage
	if err := c.doJSON(ctx, http.MethodGet, path, nil, true, &page); err != nil {
		return RecentPage{}, err
	}

	return page, nil
}

// GetFileMetadata loads the prototype's selected-file metadata subset.
func (c *Client) GetFileMetadata(ctx context.Context, fileID int64) (FileMetadata, error) {
	path := "/get_files/file_metadata?file_id=" + strconv.FormatInt(fileID, 10) + "&include_services_object=false"
	var response metadataResponse
	if err := c.doJSON(ctx, http.MethodGet, path, nil, true, &response); err != nil {
		return FileMetadata{}, err
	}

	if len(response.Metadata) == 0 {
		return FileMetadata{}, fmt.Errorf("daemon returned no metadata for file_id %d", fileID)
	}

	return response.Metadata[0], nil
}

// ImportLocalFile sends one local path to hydrusd for import.
func (c *Client) ImportLocalFile(ctx context.Context, path string) (ImportResult, error) {
	var response ImportResult
	body := map[string]string{"path": strings.TrimSpace(path)}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/import/local_file", body, true, &response); err != nil {
		return ImportResult{}, err
	}

	return response, nil
}

// TrashFile moves one file into the hydrusd trash domain.
func (c *Client) TrashFile(ctx context.Context, fileID int64) (TrashResult, error) {
	var response TrashResult
	body := map[string]int64{"file_id": fileID}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/files/trash", body, true, &response); err != nil {
		return TrashResult{}, err
	}

	return response, nil
}

// FetchGridImage returns the bytes for a grid-preview image.
func (c *Client) FetchGridImage(ctx context.Context, item RecentItem) ([]byte, error) {
	if !item.HasThumbnail {
		return nil, fmt.Errorf("no thumbnail is available for file_id %d", item.FileID)
	}

	payload, err := c.doBytes(ctx, http.MethodGet, item.ThumbnailURL, true)
	if err != nil {
		return nil, err
	}

	if len(payload) == 0 {
		return nil, fmt.Errorf("daemon returned an empty thumbnail for file_id %d", item.FileID)
	}

	return payload, nil
}

// FetchFileContent returns the bytes for one daemon-served managed original.
// If maxBytes is greater than zero, the response body must not exceed that size.
func (c *Client) FetchFileContent(ctx context.Context, item RecentItem, maxBytes int64) ([]byte, error) {
	if strings.TrimSpace(item.ContentURL) == "" {
		return nil, fmt.Errorf("no content URL is available for file_id %d", item.FileID)
	}

	payload, err := c.doBytesLimited(ctx, http.MethodGet, item.ContentURL, true, maxBytes)
	if err != nil {
		return nil, err
	}

	return payload, nil
}

func (c *Client) doJSON(
	ctx context.Context,
	method string,
	path string,
	body any,
	preferSession bool,
	target any,
) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}

		requestBody = bytes.NewReader(encoded)
	}

	req, err := c.newRequest(ctx, method, path, requestBody, preferSession)
	if err != nil {
		return err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("perform daemon request: %w", err)
	}
	defer resp.Body.Close()

	if err := checkAPIResponse(resp); err != nil {
		return err
	}

	if target == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode daemon response: %w", err)
	}

	return nil
}

func (c *Client) doBytes(
	ctx context.Context,
	method string,
	path string,
	preferSession bool,
) ([]byte, error) {
	return c.doBytesLimited(ctx, method, path, preferSession, 0)
}

func (c *Client) doBytesLimited(
	ctx context.Context,
	method string,
	path string,
	preferSession bool,
	maxBytes int64,
) ([]byte, error) {
	req, err := c.newRequest(ctx, method, path, nil, preferSession)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perform daemon request: %w", err)
	}
	defer resp.Body.Close()

	if err := checkAPIResponse(resp); err != nil {
		return nil, err
	}

	bodyReader := io.Reader(resp.Body)
	if maxBytes > 0 {
		bodyReader = io.LimitReader(resp.Body, maxBytes+1)
	}

	payload, err := io.ReadAll(bodyReader)
	if err != nil {
		return nil, fmt.Errorf("read daemon response body: %w", err)
	}

	if maxBytes > 0 && int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("daemon response exceeded %d bytes", maxBytes)
	}

	return payload, nil
}

func (c *Client) newRequest(
	ctx context.Context,
	method string,
	path string,
	body io.Reader,
	preferSession bool,
) (*http.Request, error) {
	if strings.TrimSpace(c.baseURL) == "" {
		return nil, fmt.Errorf("daemon URL is not configured")
	}

	urlString, err := c.resolveURL(path)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, urlString, body)
	if err != nil {
		return nil, fmt.Errorf("build daemon request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)

	credential := strings.TrimSpace(c.accessKey)
	headerName := "Hydrus-Client-API-Access-Key"
	if preferSession && strings.TrimSpace(c.sessionKey) != "" {
		credential = strings.TrimSpace(c.sessionKey)
		headerName = "Hydrus-Client-API-Session-Key"
	}

	if credential != "" {
		req.Header.Set(headerName, credential)
	}

	return req, nil
}

func (c *Client) resolveURL(path string) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base daemon URL: %w", err)
	}

	reference, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("parse daemon path %q: %w", path, err)
	}

	return base.ResolveReference(reference).String(), nil
}

func normalizeBaseURL(raw string) (string, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return "", fmt.Errorf("daemon URL is required")
	}

	if !strings.Contains(normalized, "://") {
		normalized = "http://" + normalized
	}

	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("parse daemon URL: %w", err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("daemon URL must include a host")
	}

	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return strings.TrimRight(parsed.String(), "/"), nil
}

func checkAPIResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("daemon returned HTTP %d", resp.StatusCode)
	}

	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}

	return fmt.Errorf("daemon returned HTTP %d: %s", resp.StatusCode, message)
}
