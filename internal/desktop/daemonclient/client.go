// Package daemonclient provides a thin HTTP client for the local hydrusd daemon.
package daemonclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	coredownloader "github.com/official-elinas/hydrus-go/internal/core/downloader"
	"github.com/official-elinas/hydrus-go/internal/core/clientsessions"
	"github.com/official-elinas/hydrus-go/internal/core/fileimport"
	"github.com/official-elinas/hydrus-go/internal/core/mimes"
	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/media/ffmpegutil"
	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const hydrusContentUpdatePendAction = "2"

const userAgent = "hydrus-desktop-prototype/0.1"

const (
	defaultGridThumbnailMaxDimension = 256
	gridThumbnailSourceByteLimit     = 4 << 20
	selectedPreviewThumbnailByteLimit = 8 << 20
)

// Client talks to hydrusd over HTTP.
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
	FileID    int64                             `json:"file_id"`
	Hash      string                            `json:"hash"`
	MIME      string                            `json:"mime"`
	Size      int64                             `json:"size"`
	Width     *int64                            `json:"width,omitempty"`
	Height    *int64                            `json:"height,omitempty"`
	IsLocal   bool                              `json:"is_local"`
	IsTrashed bool                              `json:"is_trashed"`
	IsDeleted bool                              `json:"is_deleted"`
	Ratings   map[string]any                    `json:"ratings,omitempty"`
	Tags      map[string]FileMetadataTagService `json:"tags,omitempty"`
}

// FileMetadataTagService is one daemon-served metadata tag group keyed by
// service key.
type FileMetadataTagService struct {
	Name        string              `json:"name"`
	Type        int                 `json:"type"`
	TypePretty  string              `json:"type_pretty"`
	StorageTags map[string][]string `json:"storage_tags,omitempty"`
	DisplayTags map[string][]string `json:"display_tags,omitempty"`
}

// ImportResult is the public file import response payload.
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

type tagSuggestionsResponse struct {
	Suggestions []string `json:"suggestions"`
}

type downloaderStatusResponse struct {
	Downloader coredownloader.Status `json:"downloader"`
}

type downloaderMapResponse struct {
	Downloaders map[string]string `json:"downloaders"`
}

type addTagsRequest struct {
	Hash                       string                         `json:"hash,omitempty"`
	Hashes                     []string                       `json:"hashes,omitempty"`
	FileID                     *int64                         `json:"file_id,omitempty"`
	FileIDs                    []int64                        `json:"file_ids,omitempty"`
	ServiceKeysToActionsToTags map[string]map[string][]string `json:"service_keys_to_actions_to_tags"`
}

type commitPendingRequest struct {
	ServiceKey string `json:"service_key,omitempty"`
}

type sessionResponse struct {
	SessionKey string `json:"session_key"`
}

// New constructs a daemon client with an upload-safe request timeout.
func New() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 5 * time.Minute},
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

// SearchOptions controls optional query parameters for a SearchByTags call.
type SearchOptions struct {
	// SortBy is the server-side sort order. Accepted values: import_newest,
	// import_oldest, size_desc, size_asc. Omitted when empty.
	SortBy string
	// SystemPredicates is a list of raw system predicate strings without the
	// "system:" prefix (e.g. "size>=2048", "width<800"). Encoded as repeated
	// system_predicates[] query parameters. Omitted when empty.
	SystemPredicates []string
}

// SearchByTags loads one page of local files that have all of the given tags.
// Callers may pass an optional SearchOptions value to supply sort order and
// system predicates; those parameters are omitted from the request when empty.
func (c *Client) SearchByTags(
	ctx context.Context,
	tags []string,
	offset int,
	limit int,
	opts ...SearchOptions,
) (RecentPage, error) {
	params := url.Values{}
	params.Set("offset", strconv.Itoa(offset))
	params.Set("limit", strconv.Itoa(limit))
	for _, t := range tags {
		params.Add("tags", t)
	}

	var options SearchOptions
	if len(opts) > 0 {
		options = opts[0]
	}

	if options.SortBy != "" {
		params.Set("sort_by", options.SortBy)
	}

	for _, sp := range options.SystemPredicates {
		params.Add("system_predicates[]", sp)
	}

	path := "/v1/library/search?" + params.Encode()
	var page RecentPage
	if err := c.doJSON(ctx, http.MethodGet, path, nil, true, &page); err != nil {
		return RecentPage{}, err
	}

	return page, nil
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

// GetBasicFileMetadata loads the fast basic metadata subset for one selected file.
func (c *Client) GetBasicFileMetadata(ctx context.Context, fileID int64) (FileMetadata, error) {
	path := "/get_files/file_metadata?file_id=" + strconv.FormatInt(fileID, 10) + "&only_return_basic_information=true&include_services_object=false"
	var response metadataResponse
	if err := c.doJSON(ctx, http.MethodGet, path, nil, true, &response); err != nil {
		return FileMetadata{}, err
	}

	if len(response.Metadata) == 0 {
		return FileMetadata{}, fmt.Errorf("daemon returned no metadata for file_id %d", fileID)
	}

	return response.Metadata[0], nil
}

// SuggestTags loads daemon-backed tag suggestions for one normalized prefix.
func (c *Client) SuggestTags(
	ctx context.Context,
	prefix string,
	limit int,
) ([]string, error) {
	normalizedPrefix := strings.TrimSpace(prefix)
	if normalizedPrefix == "" {
		return []string{}, nil
	}

	path := "/v1/tags/autocomplete?q=" + url.QueryEscape(normalizedPrefix)
	if limit > 0 {
		path += "&limit=" + strconv.Itoa(limit)
	}

	var response tagSuggestionsResponse
	if err := c.doJSON(ctx, http.MethodGet, path, nil, true, &response); err != nil {
		return nil, err
	}

	return response.Suggestions, nil
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

// ImportURL asks hydrusd to download and import one direct file URL.
func (c *Client) ImportURL(ctx context.Context, request fileimport.URLRequest) (ImportResult, error) {
	var response ImportResult
	body := map[string]string{
		"url": strings.TrimSpace(request.URL),
	}
	if strings.TrimSpace(request.ReferralURL) != "" {
		body["referral_url"] = strings.TrimSpace(request.ReferralURL)
	}
	if strings.TrimSpace(request.LocalFileServiceKey) != "" {
		body["local_file_service_key"] = strings.TrimSpace(request.LocalFileServiceKey)
	}

	if err := c.doJSON(ctx, http.MethodPost, "/v1/import/url", body, true, &response); err != nil {
		return ImportResult{}, err
	}

	return response, nil
}

// GetDownloaderStatus returns the current daemon-owned hydownloader status.
func (c *Client) GetDownloaderStatus(ctx context.Context) (coredownloader.Status, error) {
	var response downloaderStatusResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/downloader/status", nil, true, &response); err != nil {
		return coredownloader.Status{}, err
	}

	return response.Downloader, nil
}

// GetDownloaderDownloaders returns the supported hydownloader subscription downloaders.
func (c *Client) GetDownloaderDownloaders(ctx context.Context) (map[string]string, error) {
	var response downloaderMapResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/downloader/downloaders", nil, true, &response); err != nil {
		return nil, err
	}

	return response.Downloaders, nil
}

// QueueDownloaderURL asks hydrusd to queue a hydownloader single URL job.
func (c *Client) QueueDownloaderURL(ctx context.Context, request coredownloader.URLRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/downloader/url", request, true, nil)
}

// QueueDownloaderSubscription asks hydrusd to queue a hydownloader subscription.
func (c *Client) QueueDownloaderSubscription(ctx context.Context, request coredownloader.SubscriptionRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/downloader/subscription", request, true, nil)
}

// UploadFile streams one local file to hydrusd for import.
func (c *Client) UploadFile(ctx context.Context, path string) (ImportResult, error) {
	normalizedPath := filepath.Clean(strings.TrimSpace(path))
	if normalizedPath == "." || normalizedPath == "" {
		return ImportResult{}, fmt.Errorf("file path is required")
	}

	file, err := os.Open(normalizedPath)
	if err != nil {
		return ImportResult{}, fmt.Errorf("open file for upload: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return ImportResult{}, fmt.Errorf("stat file for upload: %w", err)
	}

	if !info.Mode().IsRegular() {
		_ = file.Close()
		return ImportResult{}, fmt.Errorf("file path must point to a regular file")
	}

	pipeReader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)

	req, err := c.newRequest(ctx, http.MethodPost, "/v1/import/upload", pipeReader, true)
	if err != nil {
		_ = file.Close()
		_ = pipeReader.Close()
		_ = pipeWriter.Close()
		return ImportResult{}, err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	writeErrCh := make(chan error, 1)
	go func() {
		writeErrCh <- writeUploadRequestBody(
			writer,
			pipeWriter,
			file,
			filepath.Base(normalizedPath),
			info.ModTime(),
		)
	}()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if writeErr := <-writeErrCh; writeErr != nil {
			return ImportResult{}, fmt.Errorf("stream upload request: %w", writeErr)
		}

		return ImportResult{}, fmt.Errorf("perform daemon request: %w", err)
	}
	defer resp.Body.Close()

	if writeErr := <-writeErrCh; writeErr != nil {
		return ImportResult{}, fmt.Errorf("stream upload request: %w", writeErr)
	}

	if err := checkAPIResponse(resp); err != nil {
		return ImportResult{}, err
	}

	var response ImportResult
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return ImportResult{}, fmt.Errorf("decode daemon response: %w", err)
	}

	return response, nil
}

// TrashFile moves one file into the hydrusd trash domain.
func (c *Client) TrashFile(ctx context.Context, fileID int64) (TrashResult, error) {
	var response TrashResult
	if err := c.doJSON(ctx, http.MethodPost, "/v1/files/trash", map[string]int64{"file_id": fileID}, true, &response); err != nil {
		return TrashResult{}, err
	}

	return response, nil
}

// AddPendingMappings stages add-only pending PTR tag mappings through hydrusd.
func (c *Client) AddPendingMappings(
	ctx context.Context,
	request coreptrsync.PendingMappingsRequest,
) (coreptrsync.PendingMappingsResult, error) {
	serviceKey := strings.ToLower(strings.TrimSpace(request.ServiceKey))
	if serviceKey == "" {
		serviceKey = coreptrsync.DaemonServiceKeyHex()
	}

	body := addTagsRequest{
		Hashes:  append([]string(nil), request.Hashes...),
		FileIDs: append([]int64(nil), request.FileIDs...),
		ServiceKeysToActionsToTags: map[string]map[string][]string{
			serviceKey: {
				hydrusContentUpdatePendAction: append([]string(nil), request.Tags...),
			},
		},
	}

	if len(body.Hashes) == 1 {
		body.Hash = body.Hashes[0]
		body.Hashes = nil
	}

	if len(body.FileIDs) == 1 {
		fileID := body.FileIDs[0]
		body.FileID = &fileID
		body.FileIDs = nil
	}

	var response coreptrsync.PendingMappingsResult
	if err := c.doJSON(ctx, http.MethodPost, "/add_tags/add_tags", body, true, &response); err != nil {
		return coreptrsync.PendingMappingsResult{}, err
	}

	return response, nil
}

// CommitPending commits staged PTR pending mappings through hydrusd.
func (c *Client) CommitPending(
	ctx context.Context,
	request coreptrsync.CommitPendingRequest,
) (coreptrsync.CommitPendingResult, error) {
	body := commitPendingRequest{
		ServiceKey: strings.ToLower(strings.TrimSpace(request.ServiceKey)),
	}

	var response coreptrsync.CommitPendingResult
	if err := c.doJSON(ctx, http.MethodPost, "/manage_services/commit_pending", body, true, &response); err != nil {
		return coreptrsync.CommitPendingResult{}, err
	}

	return response, nil
}

// GetPendingCount fetches the locally staged pending mapping count for the
// given PTR service. If serviceKey is blank the server default is used.
func (c *Client) GetPendingCount(
	ctx context.Context,
	serviceKey string,
) (coreptrsync.PendingInfo, error) {
	path := "/manage_services/pending_counts"
	normalizedKey := strings.ToLower(strings.TrimSpace(serviceKey))
	if normalizedKey != "" {
		path += "?service_key=" + url.QueryEscape(normalizedKey)
	}

	var info coreptrsync.PendingInfo
	if err := c.doJSON(ctx, http.MethodGet, path, nil, true, &info); err != nil {
		return coreptrsync.PendingInfo{}, err
	}

	return info, nil
}

// PTRStatusResponse wraps the daemon PTR status API payload.
type PTRStatusResponse struct {
	PTR coreptrsync.Status `json:"ptr"`
}

// DBIntegrityResult is the daemon-served SQLite integrity-check payload.
type DBIntegrityResult struct {
	Passed  bool     `json:"passed"`
	Results []string `json:"results"`
}

// DBIntegrityResponse wraps the daemon database integrity-check API payload.
type DBIntegrityResponse struct {
	Integrity DBIntegrityResult `json:"integrity"`
}

// GetPTRStatus retrieves the daemon-side PTR sync status.
func (c *Client) GetPTRStatus(ctx context.Context) (PTRStatusResponse, error) {
	var out PTRStatusResponse
	err := c.doJSON(ctx, http.MethodGet, "/service/ptr/status", nil, true, &out)
	return out, err
}

// TriggerPTRSync triggers a manual sync pass for the daemon-side PTR.
func (c *Client) TriggerPTRSync(ctx context.Context) (PTRStatusResponse, error) {
	var out PTRStatusResponse
	err := c.doJSON(ctx, http.MethodPost, "/service/ptr/sync", nil, true, &out)
	return out, err
}

// TriggerDBIntegrityCheck runs a manual SQLite integrity check through hydrusd.
func (c *Client) TriggerDBIntegrityCheck(ctx context.Context) (DBIntegrityResponse, error) {
	var out DBIntegrityResponse
	err := c.doJSON(ctx, http.MethodPost, "/manage_database/integrity_check", nil, true, &out)
	return out, err
}

// GenerateGridThumbnail prefers the daemon thumbnail endpoint and falls back to
// deriving a grid thumbnail from a bounded original payload.
func (c *Client) GenerateGridThumbnail(ctx context.Context, item RecentItem, maxDimension int) ([]byte, error) {
	if strings.TrimSpace(item.ContentURL) == "" {
		return nil, fmt.Errorf("no content URL is available for file_id %d", item.FileID)
	}

	if maxDimension <= 0 {
		maxDimension = defaultGridThumbnailMaxDimension
	}

	if strings.TrimSpace(item.ThumbnailURL) != "" {
		thumbnailPayload, err := c.doBytesLimited(ctx, http.MethodGet, item.ThumbnailURL, true, gridThumbnailSourceByteLimit)
		if err == nil && len(thumbnailPayload) > 0 {
			return thumbnailPayload, nil
		}
	}

	payload, err := c.doBytesLimited(ctx, http.MethodGet, item.ContentURL, true, gridThumbnailSourceByteLimit)
	if err != nil {
		return nil, err
	}

	if len(payload) == 0 {
		return nil, fmt.Errorf("daemon returned an empty original for file_id %d", item.FileID)
	}

	source, _, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		fallback, fallbackErr := ffmpegutil.TranscodeBytesToPNG(
			ctx,
			payload,
			previewInputExt(item.MIME),
			maxDimension,
			strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.MIME)), "video/") || strings.TrimSpace(strings.ToLower(item.MIME)) == "video",
		)
		if fallbackErr != nil {
			return nil, fmt.Errorf("decode original for grid thumbnail: %w", err)
		}

		return fallback, nil
	}

	thumbnail := resizeImageToFit(source, maxDimension)
	var buf bytes.Buffer
	if err := png.Encode(&buf, thumbnail); err != nil {
		return nil, fmt.Errorf("encode grid thumbnail: %w", err)
	}

	return buf.Bytes(), nil
}

// FetchSelectedPreview prefers daemon thumbnail bytes for preview-pane display
// and falls back to the bounded original only when no thumbnail is available.
func (c *Client) FetchSelectedPreview(ctx context.Context, item RecentItem) ([]byte, error) {
	if strings.TrimSpace(item.ThumbnailURL) != "" {
		thumbnailPayload, err := c.doBytesLimited(ctx, http.MethodGet, item.ThumbnailURL, true, selectedPreviewThumbnailByteLimit)
		if err == nil && len(thumbnailPayload) > 0 {
			return thumbnailPayload, nil
		}
	}

	return c.FetchFileContent(ctx, item, selectedPreviewThumbnailByteLimit)
}

func previewInputExt(mime string) string {
	if mimeEnum, ok := mimes.FromMIMEType(mime); ok {
		return mimes.Lookup(mimeEnum).Ext
	}

	return ""
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

// FetchFileContentToTemp streams one daemon-served managed original into a temp
// file so callers can hand large video payloads to native tools without keeping
// the whole file in memory.
func (c *Client) FetchFileContentToTemp(ctx context.Context, item RecentItem, maxBytes int64) (string, func(), error) {
	if strings.TrimSpace(item.ContentURL) == "" {
		return "", func() {}, fmt.Errorf("no content URL is available for file_id %d", item.FileID)
	}

	req, err := c.newRequest(ctx, http.MethodGet, item.ContentURL, nil, true)
	if err != nil {
		return "", func() {}, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", func() {}, fmt.Errorf("perform daemon request: %w", err)
	}
	defer resp.Body.Close()

	if err := checkAPIResponse(resp); err != nil {
		return "", func() {}, err
	}

	tempFile, err := os.CreateTemp("", "hydrus-go-content-*"+previewInputExt(item.MIME))
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp content file: %w", err)
	}
	tempPath := tempFile.Name()
	cleanup := func() {
		_ = os.Remove(tempPath)
	}

	bodyReader := io.Reader(resp.Body)
	if maxBytes > 0 {
		bodyReader = io.LimitReader(resp.Body, maxBytes+1)
	}

	written, err := io.Copy(tempFile, bodyReader)
	if closeErr := tempFile.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("stream daemon response body: %w", err)
	}
	if maxBytes > 0 && written > maxBytes {
		cleanup()
		return "", func() {}, fmt.Errorf("daemon response exceeded %d bytes", maxBytes)
	}

	return tempPath, cleanup, nil
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

func resizeImageToFit(source image.Image, maxDimension int) image.Image {
	bounds := source.Bounds()
	sourceWidth := bounds.Dx()
	sourceHeight := bounds.Dy()
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}

	if maxDimension <= 0 {
		maxDimension = defaultGridThumbnailMaxDimension
	}

	targetWidth := sourceWidth
	targetHeight := sourceHeight
	if sourceWidth > maxDimension || sourceHeight > maxDimension {
		if sourceWidth >= sourceHeight {
			targetWidth = maxDimension
			targetHeight = max(1, sourceHeight*maxDimension/sourceWidth)
		} else {
			targetHeight = maxDimension
			targetWidth = max(1, sourceWidth*maxDimension/sourceHeight)
		}
	}

	target := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	xdraw.CatmullRom.Scale(target, target.Bounds(), source, bounds, xdraw.Over, nil)
	return target
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

func writeUploadRequestBody(
	writer *multipart.Writer,
	pipeWriter *io.PipeWriter,
	file *os.File,
	filename string,
	modifiedAt time.Time,
) error {
	defer file.Close()

	closeWithError := func(err error) error {
		_ = pipeWriter.CloseWithError(err)
		return err
	}

	if modifiedAtMS := modifiedAt.UTC().UnixMilli(); modifiedAtMS > 0 {
		if err := writer.WriteField("file_modified_at_ms", strconv.FormatInt(modifiedAtMS, 10)); err != nil {
			return closeWithError(err)
		}
	}

	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return closeWithError(err)
	}

	if _, err := io.Copy(part, file); err != nil {
		return closeWithError(err)
	}

	if err := writer.Close(); err != nil {
		return closeWithError(err)
	}

	return pipeWriter.Close()
}

func (c *Client) ListSearchSessions(ctx context.Context) ([]clientsessions.Session, error) {
	var response struct {
		Sessions []clientsessions.Session `json:"sessions"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sessions", nil, true, &response); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return response.Sessions, nil
}

func (c *Client) CreateSearchSession(ctx context.Context, req clientsessions.CreateRequest) (clientsessions.Session, error) {
	var response struct {
		Session clientsessions.Session `json:"session"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/sessions", req, true, &response); err != nil {
		return clientsessions.Session{}, fmt.Errorf("create session: %w", err)
	}
	return response.Session, nil
}

func (c *Client) UpdateSearchSession(ctx context.Context, id int64, req clientsessions.UpdateRequest) (clientsessions.Session, error) {
	var response struct {
		Session clientsessions.Session `json:"session"`
	}
	path := fmt.Sprintf("/v1/sessions/%d", id)
	if err := c.doJSON(ctx, http.MethodPatch, path, req, true, &response); err != nil {
		return clientsessions.Session{}, fmt.Errorf("update session %d: %w", id, err)
	}
	return response.Session, nil
}

func (c *Client) DeleteSearchSession(ctx context.Context, id int64) error {
	path := fmt.Sprintf("/v1/sessions/%d", id)
	if err := c.doJSON(ctx, http.MethodDelete, path, nil, true, nil); err != nil {
		return fmt.Errorf("delete session %d: %w", id, err)
	}
	return nil
}
