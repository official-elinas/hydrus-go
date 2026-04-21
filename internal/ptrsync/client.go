// Package ptrsync manages daemon-owned Public Tag Repository sync state.
package ptrsync

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
)

const ptrSyncUserAgent = "hydrus-go-ptrsync/0.1"

var (
	// ptrSyncMaxCompressedResponseBytes bounds the raw HTTP response body before
	// any Hydrus zlib decompression is attempted.
	ptrSyncMaxCompressedResponseBytes int64 = 32 << 20
	// ptrSyncMaxDecompressedResponseBytes bounds the inflated Hydrus payload after
	// zlib decompression to defend against oversized or zip-bomb-like responses.
	ptrSyncMaxDecompressedResponseBytes int64 = 128 << 20
)

// Client performs one anonymous PTR remote snapshot fetch flow.
type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	accessKey  string
	jar        http.CookieJar
}

// NewClient constructs a real Hydrus remote client using cookie-based session
// auth. For Hydrus-network parity it accepts self-signed TLS certificates,
// while still refusing redirects so the shared Hydrus-Key login header and
// subsequent session cookie are never forwarded to another origin.
func NewClient(cfg coreptrsync.Config) (*Client, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return nil, fmt.Errorf("PTR host is required")
	}

	if cfg.Port <= 0 {
		return nil, fmt.Errorf("PTR port must be greater than zero")
	}

	accessKey := strings.ToLower(strings.TrimSpace(cfg.AccessKey))
	if accessKey == "" {
		return nil, fmt.Errorf("PTR access key is required")
	}

	if _, err := hex.DecodeString(accessKey); err != nil {
		return nil, fmt.Errorf("decode PTR access key: %w", err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create PTR cookie jar: %w", err)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // Hydrus network parity uses self-signed service certs.

	baseURL := &url.URL{
		Scheme: "https",
		Host:   net.JoinHostPort(host, strconv.Itoa(cfg.Port)),
		Path:   "/",
	}

	return &Client{
		httpClient: &http.Client{
			Timeout:   5 * time.Minute,
			Transport: transport,
			Jar:       jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Hydrus-Key is a custom auth header, so never forward it across redirects.
				return http.ErrUseLastResponse
			},
		},
		baseURL:   baseURL,
		accessKey: accessKey,
		jar:       jar,
	}, nil
}

// FetchRemoteState performs the real remote anonymous PTR snapshot flow.
func (c *Client) FetchRemoteState(
	ctx context.Context,
	since int64,
) (coreptrsync.RemoteState, error) {
	if err := c.ensureSession(ctx); err != nil {
		return coreptrsync.RemoteState{}, err
	}

	account, err := c.fetchAccount(ctx)
	if err != nil {
		return coreptrsync.RemoteState{}, err
	}

	serviceOptions, err := c.fetchOptions(ctx)
	if err != nil {
		return coreptrsync.RemoteState{}, err
	}

	tagFilter, err := c.fetchTagFilter(ctx)
	if err != nil {
		return coreptrsync.RemoteState{}, err
	}

	metadata, err := c.fetchMetadata(ctx, since)
	if err != nil {
		return coreptrsync.RemoteState{}, err
	}

	return coreptrsync.RemoteState{
		Account:        account,
		ServiceOptions: serviceOptions,
		TagFilter:      tagFilter,
		Metadata:       metadata,
	}, nil
}

func (c *Client) ensureSession(ctx context.Context) error {
	if c.hasSessionCookie() {
		return nil
	}

	req, err := c.newGETRequest(ctx, "session_key", nil)
	if err != nil {
		return err
	}

	req.Header.Set("Hydrus-Key", c.accessKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request PTR /session_key: %w", err)
	}
	defer resp.Body.Close()

	if err := checkPTRResponse(resp, "GET", "/session_key"); err != nil {
		return err
	}

	if _, err := readLimitedBytes(resp.Body, ptrSyncMaxCompressedResponseBytes, "PTR /session_key response body"); err != nil {
		return fmt.Errorf("read PTR /session_key response: %w", err)
	}

	if !c.hasSessionCookie() {
		return fmt.Errorf("PTR /session_key did not return a session_key cookie")
	}

	return nil
}

func (c *Client) fetchAccount(ctx context.Context) (coreptrsync.AccountSnapshot, error) {
	body, err := c.doGET(ctx, "account", nil)
	if err != nil {
		return coreptrsync.AccountSnapshot{}, err
	}

	account, err := decodeAccountResponse(body)
	if err != nil {
		return coreptrsync.AccountSnapshot{}, fmt.Errorf("decode PTR /account response: %w", err)
	}

	return account, nil
}

func (c *Client) fetchOptions(ctx context.Context) (coreptrsync.ServiceOptions, error) {
	body, err := c.doGET(ctx, "options", nil)
	if err != nil {
		return coreptrsync.ServiceOptions{}, err
	}

	options, err := decodeOptionsResponse(body)
	if err != nil {
		return coreptrsync.ServiceOptions{}, fmt.Errorf("decode PTR /options response: %w", err)
	}

	return options, nil
}

func (c *Client) fetchTagFilter(ctx context.Context) (coreptrsync.TagFilterSnapshot, error) {
	body, err := c.doGET(ctx, "tag_filter", nil)
	if err != nil {
		return coreptrsync.TagFilterSnapshot{}, err
	}

	tagFilter, err := decodeTagFilterResponse(body)
	if err != nil {
		return coreptrsync.TagFilterSnapshot{}, fmt.Errorf("decode PTR /tag_filter response: %w", err)
	}

	return tagFilter, nil
}

func (c *Client) fetchMetadata(
	ctx context.Context,
	since int64,
) (coreptrsync.MetadataSlice, error) {
	query := url.Values{}
	query.Set("since", strconv.FormatInt(since, 10))

	body, err := c.doGET(ctx, "metadata", query)
	if err != nil {
		return coreptrsync.MetadataSlice{}, err
	}

	metadata, err := decodeMetadataResponse(body)
	if err != nil {
		return coreptrsync.MetadataSlice{}, fmt.Errorf("decode PTR /metadata response: %w", err)
	}

	return metadata, nil
}

func (c *Client) doGET(ctx context.Context, path string, query url.Values) ([]byte, error) {
	req, err := c.newGETRequest(ctx, path, query)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request PTR /%s: %w", path, err)
	}
	defer resp.Body.Close()

	if err := checkPTRResponse(resp, http.MethodGet, "/"+strings.TrimPrefix(path, "/")); err != nil {
		return nil, err
	}

	body, err := readLimitedBytes(
		resp.Body,
		ptrSyncMaxCompressedResponseBytes,
		fmt.Sprintf("PTR /%s response body", strings.TrimPrefix(path, "/")),
	)
	if err != nil {
		return nil, fmt.Errorf("read PTR /%s response: %w", path, err)
	}

	return body, nil
}

func (c *Client) newGETRequest(
	ctx context.Context,
	path string,
	query url.Values,
) (*http.Request, error) {
	reqURL := c.baseURL.ResolveReference(&url.URL{
		Path:     strings.TrimPrefix(path, "/"),
		RawQuery: query.Encode(),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("construct PTR GET %s request: %w", reqURL.String(), err)
	}

	req.Header.Set("User-Agent", ptrSyncUserAgent)
	return req, nil
}

func (c *Client) hasSessionCookie() bool {
	if c == nil || c.jar == nil || c.baseURL == nil {
		return false
	}

	for _, cookie := range c.jar.Cookies(c.baseURL) {
		if cookie.Name == "session_key" && strings.TrimSpace(cookie.Value) != "" {
			return true
		}
	}

	return false
}

func checkPTRResponse(resp *http.Response, method string, path string) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	message := strings.TrimSpace(readLimitedResponseBody(resp.Body, 4096))
	if message == "" {
		return fmt.Errorf("PTR %s %s returned %s", method, path, resp.Status)
	}

	return fmt.Errorf("PTR %s %s returned %s: %s", method, path, resp.Status, message)
}

func readLimitedResponseBody(body io.Reader, maxBytes int64) string {
	if body == nil || maxBytes <= 0 {
		return ""
	}

	payload, err := io.ReadAll(io.LimitReader(body, maxBytes))
	if err != nil {
		return ""
	}

	return string(payload)
}

func readLimitedBytes(reader io.Reader, maxBytes int64, description string) ([]byte, error) {
	if maxBytes <= 0 {
		return io.ReadAll(reader)
	}

	payload, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}

	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("%s exceeded %d bytes", description, maxBytes)
	}

	return payload, nil
}
