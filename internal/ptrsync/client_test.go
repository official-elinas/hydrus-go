package ptrsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/db/hydrusdb"
)

func TestClientFetchRemoteState(t *testing.T) {
	t.Run("logs in with Hydrus-Key and fetches the real snapshot flow", func(t *testing.T) {
		const accessKey = "4a285629721ca442541ef2c15ea17d1f7f7578b0c3f4f5f2a05f8f0ab297786f"

		var (
			mu           sync.Mutex
			requestPaths []string
		)

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requestPaths = append(requestPaths, formatRequestPath(r.URL.Path, r.URL.RawQuery))
			mu.Unlock()

			switch r.URL.Path {
			case "/session_key":
				if got := r.Header.Get("Hydrus-Key"); got != accessKey {
					t.Errorf("Hydrus-Key = %q, want %q", got, accessKey)
				}

				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "remote-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key: "account",
					metaValue: metaJSON([]any{
						strings.Repeat("aa", 32),
						unsupportedSerialisable(102),
						int64(1699990000),
						nil,
						serialisableDictionaryString(t,
							hydrusDictEntry{key: "banned_info", metaValue: metaJSON(nil)},
							hydrusDictEntry{key: "bandwidth_tracker", metaValue: metaHydrus(unsupportedSerialisable(39))},
							hydrusDictEntry{key: "message", metaValue: metaJSON("shared read-only")},
							hydrusDictEntry{key: "message_created", metaValue: metaJSON(int64(1699990100))},
						),
					}),
				}))
			case "/options":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key:       "service_options",
					metaValue: metaHydrus(serialisableDictionary(hydrusDictEntry{key: "update_period", metaValue: metaJSON(int64(3600))}, hydrusDictEntry{key: "nullification_period", metaValue: metaJSON(int64(86400))})),
				}))
			case "/tag_filter":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key:       "tag_filter",
					metaValue: metaHydrus(serialisableTagFilter(map[string]int{":": 1, "creator:": 0})),
				}))
			case "/metadata":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				if got := r.URL.Query().Get("since"); got != "2" {
					t.Errorf("metadata since = %q, want %q", got, "2")
				}
				_, _ = w.Write(hydrusArgsBytes(t, hydrusDictEntry{
					key:       "metadata_slice",
					metaValue: metaHydrus(serialisableMetadata(1700000200, metadataRow{updateIndex: 2, updateHashes: []string{strings.Repeat("11", 32)}, begin: 10, end: 20})),
				}))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, err := NewClient(testPTRConfigFromServer(t, server.URL, accessKey))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		remoteState, err := client.FetchRemoteState(context.Background(), 2)
		if err != nil {
			t.Fatalf("FetchRemoteState() error = %v", err)
		}

		if remoteState.Account.Message != "shared read-only" {
			t.Fatalf("remoteState.Account.Message = %q, want %q", remoteState.Account.Message, "shared read-only")
		}

		if remoteState.ServiceOptions.UpdatePeriod != 3600 {
			t.Fatalf("remoteState.ServiceOptions.UpdatePeriod = %d, want 3600", remoteState.ServiceOptions.UpdatePeriod)
		}

		if remoteState.TagFilter.Rules[":"] != 1 {
			t.Fatalf("remoteState.TagFilter.Rules[:] = %d, want 1", remoteState.TagFilter.Rules[":"])
		}

		if remoteState.Metadata.NextUpdateDue != 1700000200 {
			t.Fatalf("remoteState.Metadata.NextUpdateDue = %d, want 1700000200", remoteState.Metadata.NextUpdateDue)
		}

		if len(remoteState.Metadata.Updates) != 1 || remoteState.Metadata.Updates[0].UpdateIndex != 2 {
			t.Fatalf("remoteState.Metadata.Updates = %#v, want one update at index 2", remoteState.Metadata.Updates)
		}

		mu.Lock()
		defer mu.Unlock()
		wantPaths := []string{"/session_key", "/account", "/options", "/tag_filter", "/metadata?since=2"}
		if strings.Join(requestPaths, "|") != strings.Join(wantPaths, "|") {
			t.Fatalf("request paths = %#v, want %#v", requestPaths, wantPaths)
		}
	})

	t.Run("requires a session_key cookie from login", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/session_key" {
				t.Errorf("unexpected path %q, want only /session_key", r.URL.Path)
				http.Error(w, "unexpected path", http.StatusNotFound)
				return
			}

			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client, err := NewClient(testPTRConfigFromServer(t, server.URL, coreptrsync.DefaultSharedAccessKey))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		_, err = client.FetchRemoteState(context.Background(), 0)
		if err == nil || !strings.Contains(err.Error(), "session_key cookie") {
			t.Fatalf("FetchRemoteState() error = %v, want missing session_key cookie error", err)
		}
	})

	t.Run("does not follow redirected session login responses", func(t *testing.T) {
		var leaked atomic.Bool

		leakServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			leaked.Store(true)
			http.Error(w, "should not be called", http.StatusTeapot)
		}))
		defer leakServer.Close()

		sessionServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/session_key" {
				t.Errorf("unexpected path %q, want only /session_key", r.URL.Path)
				http.Error(w, "unexpected path", http.StatusNotFound)
				return
			}

			http.Redirect(w, r, leakServer.URL+"/session_key", http.StatusTemporaryRedirect)
		}))
		defer sessionServer.Close()

		client, err := NewClient(testPTRConfigFromServer(t, sessionServer.URL, coreptrsync.DefaultSharedAccessKey))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		_, err = client.FetchRemoteState(context.Background(), 0)
		if err == nil || !strings.Contains(err.Error(), "/session_key") {
			t.Fatalf("FetchRemoteState() error = %v, want session_key redirect failure", err)
		}

		if leaked.Load() {
			t.Fatal("redirect target was called, want no Hydrus-Key redirect follow")
		}
	})

	t.Run("rejects oversized compressed endpoint responses", func(t *testing.T) {
		originalLimit := ptrSyncMaxCompressedResponseBytes
		ptrSyncMaxCompressedResponseBytes = 8
		defer func() { ptrSyncMaxCompressedResponseBytes = originalLimit }()

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "remote-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				_, _ = w.Write([]byte("123456789"))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, err := NewClient(testPTRConfigFromServer(t, server.URL, coreptrsync.DefaultSharedAccessKey))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		_, err = client.FetchRemoteState(context.Background(), 0)
		if err == nil || !strings.Contains(err.Error(), "exceeded") {
			t.Fatalf("FetchRemoteState() error = %v, want size limit failure", err)
		}
	})

	t.Run("fetches update bytes via update_hash query and classifies content update payload", func(t *testing.T) {
		const accessKey = "4a285629721ca442541ef2c15ea17d1f7f7578b0c3f4f5f2a05f8f0ab297786f"
		updateBody := hydrusNetworkBytes(t, []any{hydrusSerialisableTypeContentUpdate, 1, []any{}})
		updateHash := sha256Hex(updateBody)

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				if got := r.Header.Get("Hydrus-Key"); got != accessKey {
					t.Errorf("Hydrus-Key = %q, want %q", got, accessKey)
				}
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "remote-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/update":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}

				if got := r.URL.Query().Get("update_hash"); got != updateHash {
					t.Fatalf("update_hash = %q, want %q", got, updateHash)
				}

				_, _ = w.Write(updateBody)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, err := NewClient(testPTRConfigFromServer(t, server.URL, accessKey))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		body, mimeEnum, err := client.FetchUpdate(context.Background(), mustDecodeHexString(t, updateHash))
		if err != nil {
			t.Fatalf("FetchUpdate() error = %v", err)
		}

		if string(body) != string(updateBody) {
			t.Fatalf("FetchUpdate() body mismatch")
		}

		if mimeEnum != 29 {
			t.Fatalf("mimeEnum = %d, want 29", mimeEnum)
		}
	})

	t.Run("rejects update bodies whose bytes do not match the requested hash", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "remote-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/update":
				_, _ = w.Write(hydrusNetworkBytes(t, []any{hydrusSerialisableTypeDefinitionsUpdate, 1, []any{}}))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, err := NewClient(testPTRConfigFromServer(t, server.URL, coreptrsync.DefaultSharedAccessKey))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		_, _, err = client.FetchUpdate(context.Background(), mustDecodeHexString(t, strings.Repeat("ab", 32)))
		if err == nil || !strings.Contains(err.Error(), "did not match expected") {
			t.Fatalf("FetchUpdate() error = %v, want hash mismatch", err)
		}
	})

	t.Run("uploads pending mappings to update with hydrus client-to-server payload", func(t *testing.T) {
		const accessKey = "4a285629721ca442541ef2c15ea17d1f7f7578b0c3f4f5f2a05f8f0ab297786f"

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				if got := r.Header.Get("Hydrus-Key"); got != accessKey {
					t.Errorf("Hydrus-Key = %q, want %q", got, accessKey)
				}
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "remote-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/update":
				if err := validateSessionCookie(r); err != nil {
					t.Error(err)
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}

				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", got)
				}

				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("io.ReadAll(update body) error = %v", err)
				}

				decoded, err := decodeHydrusNetworkBytes(body)
				if err != nil {
					t.Fatalf("decodeHydrusNetworkBytes(update body) error = %v", err)
				}

				serialisableDict := decoded.([]any)
				entries := serialisableDict[2].([]any)
				pair := entries[0].([]any)
				valueTuple := pair[1].([]any)
				clientUpdate := valueTuple[1].([]any)
				actions := clientUpdate[2].([]any)
				actionTuple := actions[0].([]any)
				if got, err := anyToInt(actionTuple[0]); err != nil || got != hydrusContentUpdatePend {
					t.Fatalf("action = %d, want %d", got, hydrusContentUpdatePend)
				}

				contentsAndReasons := actionTuple[1].([]any)
				if len(contentsAndReasons) != 2 {
					t.Fatalf("len(contentsAndReasons) = %d, want 2", len(contentsAndReasons))
				}

				firstContent := contentsAndReasons[0].([]any)[0].([]any)
				contentInfo := firstContent[2].([]any)
				mappingData := contentInfo[1].([]any)
				if mappingData[0] != "creator:alice" {
					t.Fatalf("first mapping tag = %v, want creator:alice", mappingData[0])
				}

				w.WriteHeader(http.StatusOK)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, err := NewClient(testPTRConfigFromServer(t, server.URL, accessKey))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		err = client.CommitPendingMappings(context.Background(), []hydrusdb.PTRPendingMappingGroup{
			{Tag: "creator:alice", Hashes: []string{strings.Repeat("11", 32), strings.Repeat("22", 32)}},
			{Tag: "series:zeta", Hashes: []string{strings.Repeat("33", 32)}},
		})
		if err != nil {
			t.Fatalf("CommitPendingMappings() error = %v", err)
		}
	})
}

func TestClientFetchRemoteStateBusyResponses(t *testing.T) {
	t.Run("returns a typed busy error without retrying GET requests", func(t *testing.T) {
		var accountAttempts atomic.Int32

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "remote-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				accountAttempts.Add(1)
				http.Error(
					w,
					`{"error":"This server is busy, please try again later.","exception_type":"ServerBusyException","status_code":503}`,
					http.StatusServiceUnavailable,
				)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, err := NewClient(testPTRConfigFromServer(t, server.URL, coreptrsync.DefaultSharedAccessKey))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		_, err = client.FetchRemoteState(context.Background(), 0)
		if err == nil || !strings.Contains(err.Error(), `PTR GET /account returned 503 Service Unavailable`) {
			t.Fatalf("FetchRemoteState() error = %v, want /account 503 busy error", err)
		}

		retryAfter, ok := ptrBusyRetryAfter(err)
		if !ok {
			t.Fatalf("ptrBusyRetryAfter(%v) ok = false, want true", err)
		}

		if retryAfter != 0 {
			t.Fatalf("retryAfter = %v, want 0", retryAfter)
		}

		if got := accountAttempts.Load(); got != 1 {
			t.Fatalf("account attempts = %d, want 1", got)
		}
	})

	t.Run("parses Retry-After from busy responses", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "remote-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				w.Header().Set("Retry-After", "120")
				http.Error(
					w,
					`{"error":"This server is busy, please try again later.","exception_type":"ServerBusyException","status_code":503}`,
					http.StatusServiceUnavailable,
				)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, err := NewClient(testPTRConfigFromServer(t, server.URL, coreptrsync.DefaultSharedAccessKey))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		_, err = client.FetchRemoteState(context.Background(), 0)
		if err == nil {
			t.Fatal("FetchRemoteState() error = nil, want busy error")
		}

		retryAfter, ok := ptrBusyRetryAfter(err)
		if !ok {
			t.Fatalf("ptrBusyRetryAfter(%v) ok = false, want true", err)
		}

		if retryAfter != 120*time.Second {
			t.Fatalf("retryAfter = %v, want %v", retryAfter, 120*time.Second)
		}
	})

	t.Run("does not retry non-503 GET failures", func(t *testing.T) {
		var accountAttempts atomic.Int32

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/session_key":
				http.SetCookie(w, &http.Cookie{Name: "session_key", Value: "remote-session", Path: "/"})
				w.WriteHeader(http.StatusOK)
			case "/account":
				accountAttempts.Add(1)
				http.Error(w, `{"error":"boom","status_code":500}`, http.StatusInternalServerError)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, err := NewClient(testPTRConfigFromServer(t, server.URL, coreptrsync.DefaultSharedAccessKey))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		_, err = client.FetchRemoteState(context.Background(), 0)
		if err == nil || !strings.Contains(err.Error(), `PTR GET /account returned 500 Internal Server Error`) {
			t.Fatalf("FetchRemoteState() error = %v, want immediate /account 500 error", err)
		}

		if got := accountAttempts.Load(); got != 1 {
			t.Fatalf("account attempts = %d, want 1", got)
		}
	})
}

func validateSessionCookie(r *http.Request) error {
	cookie, err := r.Cookie("session_key")
	if err != nil {
		return err
	}

	if strings.TrimSpace(cookie.Value) == "" {
		return http.ErrNoCookie
	}

	return nil
}

func testPTRConfigFromServer(t *testing.T, rawURL string, accessKey string) coreptrsync.Config {
	t.Helper()

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", rawURL, err)
	}

	port, err := strconv.Atoi(parsedURL.Port())
	if err != nil {
		t.Fatalf("strconv.Atoi(%q) error = %v", parsedURL.Port(), err)
	}

	return coreptrsync.Config{
		Enabled:     true,
		Host:        parsedURL.Hostname(),
		Port:        port,
		AccessKey:   accessKey,
		ServiceName: coreptrsync.DefaultServiceName,
	}
}

func mustDecodeHexString(t *testing.T, value string) []byte {
	t.Helper()

	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("hex.DecodeString(%q) error = %v", value, err)
	}

	return decoded
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
