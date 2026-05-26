// Package downloader defines the daemon-owned downloader management contract.
package downloader

import "context"

// Config controls optional external hydownloader supervision.
type Config struct {
	Enabled      bool
	Root         string
	Host         string
	Port         int
	AccessKey    string
	PublicAPIURL string
	Autoimport   bool
	DaemonBin    string
	ToolsBin     string
}

// DefaultConfig returns the conservative local-only hydownloader defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:    false,
		Host:       "127.0.0.1",
		Port:       53211,
		Autoimport: true,
		DaemonBin:  "hydownloader-daemon",
		ToolsBin:   "hydownloader-tools",
	}
}

// URLRequest queues one hydownloader single-URL job.
type URLRequest struct {
	URL               string `json:"url"`
	Priority          int64  `json:"priority,omitempty"`
	IgnoreAnchor      bool   `json:"ignore_anchor,omitempty"`
	AdditionalData    string `json:"additional_data,omitempty"`
	MetadataOnly      bool   `json:"metadata_only,omitempty"`
	OverwriteExisting bool   `json:"overwrite_existing,omitempty"`
	Filter            string `json:"filter,omitempty"`
	MaxFiles          *int64 `json:"max_files,omitempty"`
	Paused            bool   `json:"paused,omitempty"`
	Autoimport        *bool  `json:"autoimport,omitempty"`
}

// SubscriptionRequest queues one hydownloader subscription.
type SubscriptionRequest struct {
	Downloader      string `json:"downloader"`
	Keywords        string `json:"keywords"`
	AdditionalData  string `json:"additional_data,omitempty"`
	CheckInterval   int64  `json:"check_interval"`
	Priority        int64  `json:"priority,omitempty"`
	Paused          bool   `json:"paused,omitempty"`
	Filter          string `json:"filter,omitempty"`
	AbortAfter      *int64 `json:"abort_after,omitempty"`
	MaxFilesInitial *int64 `json:"max_files_initial,omitempty"`
	MaxFilesRegular *int64 `json:"max_files_regular,omitempty"`
	WorkerID        string `json:"worker_id,omitempty"`
	Autoimport      *bool  `json:"autoimport,omitempty"`
}

// Status is the daemon-visible hydownloader supervisor status payload.
type Status struct {
	Enabled                     bool              `json:"enabled"`
	Configured                  bool              `json:"configured"`
	Running                     bool              `json:"running"`
	Root                        string            `json:"root,omitempty"`
	BaseURL                     string            `json:"base_url,omitempty"`
	Autoimport                  bool              `json:"autoimport"`
	AutoimportPaused            bool              `json:"autoimport_jobs_paused,omitempty"`
	URLsQueued                  int64             `json:"urls_queued,omitempty"`
	SubscriptionsDue            int64             `json:"subscriptions_due,omitempty"`
	SubscriptionsPaused         bool              `json:"subscriptions_paused,omitempty"`
	URLsPaused                  bool              `json:"urls_paused,omitempty"`
	SubscriptionWorkerStatus    string            `json:"subscription_worker_status,omitempty"`
	URLWorkerStatus             string            `json:"url_worker_status,omitempty"`
	AutoimportWorkerStatus      string            `json:"autoimport_worker_status,omitempty"`
	SubscriptionWorkerUpdatedAt float64           `json:"subscription_worker_last_update_time,omitempty"`
	URLWorkerUpdatedAt          float64           `json:"url_worker_last_update_time,omitempty"`
	AutoimportWorkerUpdatedAt   float64           `json:"autoimport_worker_last_update_time,omitempty"`
	Downloaders                 map[string]string `json:"downloaders,omitempty"`
	LastError                   string            `json:"last_error,omitempty"`
}

type GalleryRequest struct {
	Downloader     string `json:"downloader"`
	Keywords       string `json:"keywords"`
	AdditionalData string `json:"additional_data,omitempty"`
	Priority       int64  `json:"priority,omitempty"`
	Filter         string `json:"filter,omitempty"`
	MaxFiles       *int64 `json:"max_files,omitempty"`
	Autoimport     *bool  `json:"autoimport,omitempty"`
}

// Store manages downloader lifecycle, queueing, and status.
type Store interface {
	Status(context.Context) (Status, error)
	QueueURL(context.Context, URLRequest) error
	QueueGallery(context.Context, GalleryRequest) error
	QueueSubscription(context.Context, SubscriptionRequest) error
	Downloaders(context.Context) (map[string]string, error)
	ActivateAutoimport(context.Context) error
}

// Shutdowner is implemented by stores that own background resources.
type Shutdowner interface {
	Shutdown(context.Context) error
}

// RequestError reports an invalid downloader request.
type RequestError struct {
	Message string
}

func (e *RequestError) Error() string {
	return e.Message
}

// NotConfiguredError reports that the downloader integration is disabled or incomplete.
type NotConfiguredError struct {
	Message string
}

func (e *NotConfiguredError) Error() string {
	return e.Message
}
