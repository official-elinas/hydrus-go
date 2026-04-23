// Package ptrsync defines the daemon-owned Public Tag Repository sync contract.
package ptrsync

import (
	"context"
	"encoding/hex"
	"errors"
)

const (
	DefaultServiceName     = "public tag repository"
	DefaultHost            = "ptr.hydrus.network"
	DefaultPort            = 45871
	DefaultSharedAccessKey = "4a285629721ca442541ef2c15ea17d1f7f7578b0c3f4f5f2a05f8f0ab297786f"
	daemonServiceKeySeed   = "hydrus-go-public-tag-repository"

	AccountModeSharedReadOnly = "shared-read-only"

	PhaseDisabled    = "disabled"
	PhaseUnavailable = "unavailable"
	PhaseIdle        = "idle"
	PhaseSyncing     = "syncing"
	PhaseRetrying    = "retrying"
	PhaseThrottling  = PhaseRetrying
)

var (
	// ErrSyncDisabled reports that PTR sync was requested while the daemon-side
	// PTR feature is disabled.
	ErrSyncDisabled = errors.New("ptr sync is disabled")
	// ErrSyncUnavailable reports that PTR sync cannot currently run because the
	// daemon lacks required local prerequisites.
	ErrSyncUnavailable = errors.New("ptr sync is unavailable")
	// ErrSyncAlreadyRunning reports that a PTR sync pass is already in progress.
	ErrSyncAlreadyRunning = errors.New("ptr sync is already running")
)

// Config describes daemon-side PTR sync settings.
type Config struct {
	Enabled     bool
	Host        string
	Port        int
	AccessKey   string
	ServiceName string
}

// DefaultConfig returns the documented public PTR defaults with sync disabled.
func DefaultConfig() Config {
	return Config{
		Enabled:     false,
		Host:        DefaultHost,
		Port:        DefaultPort,
		AccessKey:   DefaultSharedAccessKey,
		ServiceName: DefaultServiceName,
	}
}

// DaemonServiceKeyBytes returns the stable daemon-owned local Hydrus service key
// used for the public tag repository foundation.
func DaemonServiceKeyBytes() []byte {
	return []byte(daemonServiceKeySeed)
}

// DaemonServiceKeyHex returns the stable daemon-owned local Hydrus service key
// used for the public tag repository foundation.
func DaemonServiceKeyHex() string {
	return hex.EncodeToString(DaemonServiceKeyBytes())
}

// Status is the daemon-visible PTR sync status payload for API/UI polling.
type Status struct {
	Enabled                  bool   `json:"enabled"`
	Configured               bool   `json:"configured"`
	ServiceName              string `json:"service_name,omitempty"`
	ServiceKey               string `json:"service_key,omitempty"`
	Host                     string `json:"host,omitempty"`
	Port                     int    `json:"port,omitempty"`
	AccountMode              string `json:"account_mode,omitempty"`
	Phase                    string `json:"phase"`
	IsRunning                bool   `json:"is_running"`
	IsComplete               bool   `json:"is_complete"`
	MetadataSlice            int64  `json:"metadata_slice"`
	DownloadedUpdateCount    int64  `json:"downloaded_update_count"`
	ProcessedDefinitionCount int64  `json:"processed_definition_count"`
	ProcessedContentCount    int64  `json:"processed_content_count"`
	RetryAtMS                int64  `json:"retry_at_ms,omitempty"`
	RetryAttempt             int64  `json:"retry_attempt,omitempty"`
	LastError                string `json:"last_error,omitempty"`
	UnavailableReason        string `json:"unavailable_reason,omitempty"`
	UpdatedAtMS              int64  `json:"updated_at_ms,omitempty"`
}

// AccountSnapshot is the daemon-owned subset of remote Hydrus account state we
// need for PTR diagnostics and gating metadata sync.
type AccountSnapshot struct {
	AccountKey     []byte
	Created        int64
	Expires        *int64
	Message        string
	MessageCreated int64
	BannedReason   string
	BannedCreated  *int64
	BannedExpires  *int64
}

// ServiceOptions is the daemon-owned subset of repository options needed for
// update scheduling/nullification parity.
type ServiceOptions struct {
	UpdatePeriod        int64
	NullificationPeriod int64
}

// TagFilterSnapshot captures the current remote tag filter rules for a PTR.
type TagFilterSnapshot struct {
	Rules map[string]int
}

// MetadataUpdate describes one PTR update index and the update files it points
// to.
type MetadataUpdate struct {
	UpdateIndex  int64
	UpdateHashes [][]byte
	Begin        int64
	End          int64
}

// MetadataSlice is the daemon-owned subset of remote metadata needed to queue
// later /update downloads.
type MetadataSlice struct {
	Updates       []MetadataUpdate
	NextUpdateDue int64
}

// RemoteState is the durable daemon-owned snapshot of the remote PTR state we
// have fetched so far.
type RemoteState struct {
	Account        AccountSnapshot
	ServiceOptions ServiceOptions
	TagFilter      TagFilterSnapshot
	Metadata       MetadataSlice
}

// Store exposes daemon-owned PTR status and trigger operations for HTTP/UI
// callers.
type Store interface {
	Status(context.Context) (Status, error)
	Trigger(context.Context) (Status, error)
}
