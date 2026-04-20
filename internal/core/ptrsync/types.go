// Package ptrsync defines the daemon-owned Public Tag Repository sync contract.
package ptrsync

import (
	"context"
	"encoding/hex"
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
	MetadataSlice            int64  `json:"metadata_slice"`
	DownloadedUpdateCount    int64  `json:"downloaded_update_count"`
	ProcessedDefinitionCount int64  `json:"processed_definition_count"`
	ProcessedContentCount    int64  `json:"processed_content_count"`
	LastError                string `json:"last_error,omitempty"`
	UnavailableReason        string `json:"unavailable_reason,omitempty"`
	UpdatedAtMS              int64  `json:"updated_at_ms,omitempty"`
}

// Store loads PTR sync status for daemon HTTP endpoints.
type Store interface {
	Status(context.Context) (Status, error)
}
