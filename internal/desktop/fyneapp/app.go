//go:build fyne

package fyneapp

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"github.com/official-elinas/hydrus-go/internal/desktop/daemonclient"
)

const (
	prefsDaemonURLKey   = "daemon_url"
	prefsAccessKeyKey   = "access_key"
	recentPageLimit     = 120
	previewByteLimit    = 16 << 20
	previewPixelLimit   = 16_000_000
	previewMaxDimension = 8192
	defaultDaemonURL    = "http://127.0.0.1:45869"
	defaultMetadataText = "Select a file from the grid to inspect the daemon-backed metadata state.\n\nThis prototype is focused on validating hydrusd add/trash flows, not full Hydrus UI parity."
	defaultPreviewText  = "Select a supported still image to preview the daemon-served original file."
)

// Run launches the Fyne thin-client prototype window.
func Run() {
	prototype := newPrototype()
	prototype.window.ShowAndRun()
}

type prototype struct {
	app     fyne.App
	window  fyne.Window
	stateMu sync.RWMutex
	client  *daemonclient.Client

	connectButton *widget.Button
	refreshButton *widget.Button
	addButton     *widget.Button
	trashButton   *widget.Button

	connectionLabel *widget.Label
	previewImage    *canvas.Image
	previewLabel    *widget.Label
	metadataLabel   *widget.Label
	activityLabel   *widget.Label
	statusBarLabel  *widget.Label
	gridHost        *fyne.Container

	recent           []daemonclient.RecentItem
	selectedFileID   int64
	connected        bool
	connectionGen    uint64
	connectAttemptID uint64
	thumbnailCache   map[int64]fyne.Resource
	thumbnailLoads   map[int64]struct{}
	thumbnailGen     uint64
	thumbnailCacheM  sync.Mutex
	previewRequestID uint64
	previewCancel    context.CancelFunc
	previewRequestM  sync.Mutex
}

type connectionSnapshot struct {
	client     *daemonclient.Client
	connected  bool
	generation uint64
	attemptID  uint64
}

func newPrototype() *prototype {
	application := app.NewWithID("github.com.official-elinas.hydrus-go.desktop")
	application.Settings().SetTheme(forcedDarkTheme{})

	window := application.NewWindow("hydrusd thin prototype")
	window.Resize(fyne.NewSize(1500, 920))

	p := &prototype{
		app:            application,
		window:         window,
		client:         daemonclient.New(),
		thumbnailCache: map[int64]fyne.Resource{},
		thumbnailLoads: map[int64]struct{}{},
	}

	p.connectionLabel = widget.NewLabel("")
	p.connectionLabel.Wrapping = fyne.TextWrapWord
	p.previewImage = canvas.NewImageFromImage(tilePlaceholderImage)
	p.previewImage.FillMode = canvas.ImageFillContain
	p.previewLabel = widget.NewLabel(defaultPreviewText)
	p.previewLabel.Wrapping = fyne.TextWrapWord
	p.previewLabel.Alignment = fyne.TextAlignCenter
	p.metadataLabel = widget.NewLabel(defaultMetadataText)
	p.metadataLabel.Wrapping = fyne.TextWrapWord
	p.activityLabel = widget.NewLabel("No actions yet.")
	p.activityLabel.Wrapping = fyne.TextWrapWord
	p.statusBarLabel = widget.NewLabel("Ready. Connect to hydrusd to start the prototype.")
	p.statusBarLabel.Wrapping = fyne.TextWrapWord
	p.gridHost = container.NewMax()

	p.connectButton = widget.NewButton("Connect", p.showConnectDialog)
	p.refreshButton = widget.NewButton("Refresh", func() {
		p.reloadRecent(p.selectedFileID, "Refreshed recent files from hydrusd.")
	})
	p.addButton = widget.NewButton("Add File", p.showImportDialog)
	p.trashButton = widget.NewButton("Trash Selected", p.confirmTrashSelected)

	p.window.SetContent(p.buildContent())
	p.loadSavedConnection()
	p.updateActionState()
	p.renderGrid()

	return p
}

func (p *prototype) currentConnection() connectionSnapshot {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()

	return connectionSnapshot{
		client:     p.client,
		connected:  p.connected,
		generation: p.connectionGen,
		attemptID:  p.connectAttemptID,
	}
}

func (p *prototype) beginConnectAttempt() (uint64, bool) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	wasConnected := p.connected
	p.connectAttemptID++
	p.connected = false
	return p.connectAttemptID, wasConnected
}

func (p *prototype) isCurrentConnectAttempt(attemptID uint64) bool {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()

	return p.connectAttemptID == attemptID
}

func (p *prototype) isCurrentOperation(connection connectionSnapshot) bool {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()

	return p.connectionGen == connection.generation && p.connectAttemptID == connection.attemptID
}

func (p *prototype) restoreConnectedState(connected bool) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	p.connected = connected
}

func (p *prototype) installConnection(client *daemonclient.Client) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	p.client = client
	p.connected = true
	p.connectionGen++
}

func (p *prototype) clearThumbnailLoad(fileID int64, generation uint64) {
	p.thumbnailCacheM.Lock()
	defer p.thumbnailCacheM.Unlock()

	if generation != p.thumbnailGen {
		return
	}

	delete(p.thumbnailLoads, fileID)
}

func (p *prototype) beginPreviewRequest(timeout time.Duration) (context.Context, context.CancelFunc, uint64) {
	p.previewRequestM.Lock()
	defer p.previewRequestM.Unlock()

	if p.previewCancel != nil {
		p.previewCancel()
	}

	p.previewRequestID++
	requestID := p.previewRequestID
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	p.previewCancel = cancel

	return ctx, cancel, requestID
}

func (p *prototype) cancelPreviewRequest() {
	p.previewRequestM.Lock()
	defer p.previewRequestM.Unlock()

	p.previewRequestID++
	if p.previewCancel != nil {
		p.previewCancel()
		p.previewCancel = nil
	}
}

func (p *prototype) finishPreviewRequest(requestID uint64) {
	p.previewRequestM.Lock()
	defer p.previewRequestM.Unlock()

	if p.previewRequestID == requestID {
		p.previewCancel = nil
	}
}

func (p *prototype) isCurrentPreviewRequest(requestID uint64) bool {
	p.previewRequestM.Lock()
	defer p.previewRequestM.Unlock()

	return p.previewRequestID == requestID
}

func (p *prototype) buildContent() fyne.CanvasObject {
	toolbar := container.NewHBox(
		p.connectButton,
		p.refreshButton,
		p.addButton,
		p.trashButton,
	)

	previewPanel := container.NewGridWrap(
		fyne.NewSize(280, 220),
		container.NewStack(
			canvas.NewRectangle(color.NRGBA{R: 18, G: 18, B: 20, A: 255}),
			p.previewImage,
			container.NewPadded(container.NewCenter(p.previewLabel)),
		),
	)

	sidebar := container.NewVScroll(container.NewPadded(container.NewVBox(
		widget.NewLabelWithStyle("hydrusd prototype", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("A thin Fyne shell for testing add/trash workflows against the Go daemon."),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Connection", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.connectionLabel,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Selected preview", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		previewPanel,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Selected file", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.metadataLabel,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Last action", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.activityLabel,
	)))

	split := container.NewHSplit(sidebar, container.NewPadded(p.gridHost))
	split.SetOffset(0.22)

	return container.NewBorder(
		container.NewPadded(toolbar),
		container.NewPadded(p.statusBarLabel),
		nil,
		nil,
		split,
	)
}

func (p *prototype) loadSavedConnection() {
	baseURL := p.app.Preferences().StringWithFallback(prefsDaemonURLKey, defaultDaemonURL)
	accessKey := p.app.Preferences().StringWithFallback(prefsAccessKeyKey, "")

	p.connectionLabel.SetText(
		fmt.Sprintf(
			"Daemon: %s\nAccess key: %s\nStatus: not connected",
			baseURL,
			shortCredential(accessKey),
		),
	)
}

func (p *prototype) showConnectDialog() {
	baseURL := widget.NewEntry()
	baseURL.SetPlaceHolder(defaultDaemonURL)
	baseURL.SetText(p.app.Preferences().StringWithFallback(prefsDaemonURLKey, defaultDaemonURL))

	accessKey := widget.NewEntry()
	accessKey.SetPlaceHolder("64-character access key")
	accessKey.SetText(p.app.Preferences().StringWithFallback(prefsAccessKeyKey, ""))

	dialog.ShowForm(
		"Connect to hydrusd",
		"Connect",
		"Cancel",
		[]*widget.FormItem{
			{Text: "Daemon URL", Widget: baseURL},
			{Text: "Access key", Widget: accessKey},
		},
		func(ok bool) {
			if !ok {
				return
			}

			p.connectToDaemon(baseURL.Text, accessKey.Text)
		},
		p.window,
	)
}

func (p *prototype) connectToDaemon(baseURL string, accessKey string) {
	candidate := daemonclient.New()
	if err := candidate.SetConnection(baseURL, accessKey); err != nil {
		dialog.ShowError(err, p.window)
		return
	}

	p.cancelPreviewRequest()
	attemptID, wasConnected := p.beginConnectAttempt()
	p.setStatus("Connecting to hydrusd...")
	p.connectButton.Disable()
	p.refreshButton.Disable()
	p.addButton.Disable()
	p.trashButton.Disable()

	go func(attemptID uint64, wasConnected bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		verification, err := candidate.VerifyAccessKey(ctx)
		if err != nil {
			fyne.Do(func() {
				if !p.isCurrentConnectAttempt(attemptID) {
					return
				}

				p.restoreConnectedState(wasConnected)
				p.updateActionState()
				if wasConnected {
					p.renderGrid()
					if p.selectedFileID > 0 {
						p.loadSelectedPreview(p.selectedFileID)
					}
				}
				p.setStatus("Connection failed.")
				dialog.ShowError(err, p.window)
			})
			return
		}

		if !p.isCurrentConnectAttempt(attemptID) {
			return
		}

		sessionKey, err := candidate.CreateSession(ctx)
		if err != nil {
			fyne.Do(func() {
				if !p.isCurrentConnectAttempt(attemptID) {
					return
				}

				p.restoreConnectedState(wasConnected)
				p.updateActionState()
				if wasConnected {
					p.renderGrid()
					if p.selectedFileID > 0 {
						p.loadSelectedPreview(p.selectedFileID)
					}
				}
				p.setStatus("Session creation failed.")
				dialog.ShowError(err, p.window)
			})
			return
		}

		if !p.isCurrentConnectAttempt(attemptID) {
			return
		}

		page, err := candidate.ListRecent(ctx, 0, recentPageLimit)
		if err != nil {
			fyne.Do(func() {
				if !p.isCurrentConnectAttempt(attemptID) {
					return
				}

				p.restoreConnectedState(wasConnected)
				p.updateActionState()
				if wasConnected {
					p.renderGrid()
					if p.selectedFileID > 0 {
						p.loadSelectedPreview(p.selectedFileID)
					}
				}
				p.setStatus("Connected, but loading recent files failed.")
				dialog.ShowError(err, p.window)
			})
			return
		}

		if !p.isCurrentConnectAttempt(attemptID) {
			return
		}

		fyne.Do(func() {
			if !p.isCurrentConnectAttempt(attemptID) {
				return
			}

			p.installConnection(candidate)
			p.app.Preferences().SetString(prefsDaemonURLKey, candidate.BaseURL())
			p.app.Preferences().SetString(prefsAccessKeyKey, candidate.AccessKey())
			p.connectionLabel.SetText(
				fmt.Sprintf(
					"Daemon: %s\nClient: %s\nSession: %s\nStatus: connected",
					candidate.BaseURL(),
					verification.Name,
					shortCredential(sessionKey),
				),
			)
			p.applyRecentItems(page.Items, 0)
			p.setStatus(fmt.Sprintf("Connected to hydrusd and loaded %d recent files.", len(page.Items)))
		})
	}(attemptID, wasConnected)
}

func (p *prototype) showImportDialog() {
	if !p.currentConnection().connected {
		return
	}

	dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, p.window)
			return
		}

		if reader == nil {
			return
		}
		defer reader.Close()

		uri, err := storage.ParseURI(reader.URI().String())
		if err != nil {
			dialog.ShowError(err, p.window)
			return
		}

		if uri.Scheme() != "file" {
			dialog.ShowError(fmt.Errorf("only local file paths are supported by the current hydrusd import prototype"), p.window)
			return
		}

		path := filepath.Clean(uri.Path())
		if strings.TrimSpace(path) == "" {
			dialog.ShowError(fmt.Errorf("selected file path was empty"), p.window)
			return
		}

		p.importFile(path)
	}, p.window)
}

func (p *prototype) importFile(path string) {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	p.setStatus(fmt.Sprintf("Adding %s through hydrusd...", filepath.Base(path)))

	go func(connection connectionSnapshot) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		result, err := connection.client.ImportLocalFile(ctx, path)
		if err != nil {
			fyne.Do(func() {
				if !p.isCurrentOperation(connection) {
					return
				}

				p.setStatus("Add file failed.")
				dialog.ShowError(err, p.window)
			})
			return
		}

		status := fmt.Sprintf("Added file_id %d through hydrusd.", result.FileID)
		if result.AlreadyImported {
			status = fmt.Sprintf("File already existed as file_id %d; hydrusd confirmed it.", result.FileID)
		}

		fyne.Do(func() {
			if !p.isCurrentOperation(connection) {
				return
			}

			p.reloadRecent(result.FileID, status)
		})
	}(connection)
}

func (p *prototype) confirmTrashSelected() {
	if p.selectedFileID <= 0 {
		return
	}

	fileID := p.selectedFileID
	dialog.ShowConfirm(
		"Trash selected file",
		fmt.Sprintf("Send file_id %d to hydrusd trash?", fileID),
		func(ok bool) {
			if ok {
				p.trashSelected(fileID)
			}
		},
		p.window,
	)
}

func (p *prototype) trashSelected(fileID int64) {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	p.setStatus(fmt.Sprintf("Trashing file_id %d through hydrusd...", fileID))

	go func(connection connectionSnapshot) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		result, err := connection.client.TrashFile(ctx, fileID)
		if err != nil {
			fyne.Do(func() {
				if !p.isCurrentOperation(connection) {
					return
				}

				p.setStatus("Trash file failed.")
				dialog.ShowError(err, p.window)
			})
			return
		}

		status := fmt.Sprintf("Trashed file_id %d through hydrusd.", result.FileID)
		if !result.RemovedFromRecent {
			status = fmt.Sprintf("file_id %d was already in hydrusd trash.", result.FileID)
		}

		fyne.Do(func() {
			if !p.isCurrentOperation(connection) {
				return
			}

			p.reloadRecent(0, status)
		})
	}(connection)
}

func (p *prototype) reloadRecent(selectFileID int64, successStatus string) {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	currentSelection := p.selectedFileID
	p.setStatus("Refreshing recent files from hydrusd...")

	go func(connection connectionSnapshot, currentSelection int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		page, err := connection.client.ListRecent(ctx, 0, recentPageLimit)
		if err != nil {
			fyne.Do(func() {
				if !p.isCurrentOperation(connection) {
					return
				}

				p.setStatus("Refresh failed.")
				dialog.ShowError(err, p.window)
			})
			return
		}

		fyne.Do(func() {
			if !p.isCurrentOperation(connection) {
				return
			}

			preferred := selectFileID
			if preferred == 0 {
				preferred = currentSelection
			}
			p.applyRecentItems(page.Items, preferred)
			p.setStatus(successStatus)
		})
	}(connection, currentSelection)
}

func (p *prototype) applyRecentItems(items []daemonclient.RecentItem, preferredFileID int64) {
	p.thumbnailCacheM.Lock()
	p.thumbnailGen++
	p.thumbnailCache = map[int64]fyne.Resource{}
	p.thumbnailLoads = map[int64]struct{}{}
	p.thumbnailCacheM.Unlock()

	p.recent = items
	if preferredFileID > 0 && !p.hasRecentFile(preferredFileID) {
		preferredFileID = 0
	}

	p.selectedFileID = preferredFileID
	p.renderGrid()
	p.updateActionState()

	if p.selectedFileID > 0 {
		p.metadataLabel.SetText("Loading selected-file metadata from hydrusd...")
		p.loadSelectedPreview(p.selectedFileID)
		p.loadSelectedMetadata(p.selectedFileID)
		return
	}

	p.metadataLabel.SetText(defaultMetadataText)
	p.cancelPreviewRequest()
	p.clearSelectedPreview(defaultPreviewText)
}

func (p *prototype) renderGrid() {
	if len(p.recent) == 0 {
		p.gridHost.Objects = []fyne.CanvasObject{
			container.NewCenter(widget.NewLabel("No recent local files are loaded. Use Add File to exercise hydrusd.")),
		}
		p.gridHost.Refresh()
		return
	}

	tiles := make([]fyne.CanvasObject, 0, len(p.recent))
	for _, item := range p.recent {
		item := item
		tile := newMediaTile()

		resource, overlay := p.lookupPreviewResource(item.FileID)
		if resource == nil && overlay == "" {
			overlay = "Loading"
		}

		title := fmt.Sprintf("file_id %d", item.FileID)
		subtitle := item.MIME
		if item.Width != nil && item.Height != nil {
			subtitle = fmt.Sprintf("%s • %dx%d", item.MIME, *item.Width, *item.Height)
		}

		tile.SetData(
			title,
			subtitle,
			resource,
			overlay,
			item.FileID == p.selectedFileID,
			func() {
				p.selectFile(item.FileID)
			},
		)

		tiles = append(tiles, tile)
		if resource == nil {
			p.ensurePreviewResource(item)
		}
	}

	p.gridHost.Objects = []fyne.CanvasObject{
		container.NewVScroll(container.NewGridWrap(fyne.NewSize(180, 220), tiles...)),
	}
	p.gridHost.Refresh()
}

func (p *prototype) selectFile(fileID int64) {
	if !p.currentConnection().connected {
		return
	}

	p.selectedFileID = fileID
	p.renderGrid()
	p.updateActionState()
	p.metadataLabel.SetText("Loading selected-file metadata from hydrusd...")
	p.loadSelectedPreview(fileID)
	p.loadSelectedMetadata(fileID)
}

func (p *prototype) loadSelectedPreview(fileID int64) {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	item, ok := p.lookupRecentItem(fileID)
	if !ok {
		p.cancelPreviewRequest()
		p.clearSelectedPreview(defaultPreviewText)
		return
	}

	if !supportsSelectedPreviewMime(item.MIME) {
		p.cancelPreviewRequest()
		p.clearSelectedPreview(fmt.Sprintf("Preview not available for %s.", item.MIME))
		return
	}

	ctx, cancel, requestID := p.beginPreviewRequest(20 * time.Second)
	p.clearSelectedPreview("Loading selected-file preview from hydrusd...")

	go func(connection connectionSnapshot, item daemonclient.RecentItem, ctx context.Context, cancel context.CancelFunc, requestID uint64) {
		defer cancel()
		defer p.finishPreviewRequest(requestID)

		payload, err := connection.client.FetchFileContent(ctx, item, previewByteLimit)
		if err != nil {
			if ctx.Err() != nil || !p.isCurrentPreviewRequest(requestID) {
				return
			}

			fyne.Do(func() {
				if !p.isCurrentOperation(connection) || !p.isCurrentPreviewRequest(requestID) || p.selectedFileID != item.FileID {
					return
				}

				p.clearSelectedPreview("Could not load preview from hydrusd.\n\n" + err.Error())
			})
			return
		}

		if len(payload) == 0 {
			fyne.Do(func() {
				if !p.isCurrentOperation(connection) || !p.isCurrentPreviewRequest(requestID) || p.selectedFileID != item.FileID {
					return
				}

				p.clearSelectedPreview(fmt.Sprintf("Daemon returned an empty original for file_id %d.", item.FileID))
			})
			return
		}

		if err := validateSelectedPreviewPayload(payload); err != nil {
			fyne.Do(func() {
				if !p.isCurrentOperation(connection) || !p.isCurrentPreviewRequest(requestID) || p.selectedFileID != item.FileID {
					return
				}

				p.clearSelectedPreview("Preview could not be prepared for display.\n\n" + err.Error())
			})
			return
		}

		resource := fyne.NewStaticResource(
			fmt.Sprintf("file-%d-original", item.FileID),
			payload,
		)

		fyne.Do(func() {
			if !p.isCurrentOperation(connection) || !p.isCurrentPreviewRequest(requestID) || p.selectedFileID != item.FileID {
				return
			}

			p.setSelectedPreview(resource, "")
		})
	}(connection, item, ctx, cancel, requestID)
}

func (p *prototype) loadSelectedMetadata(fileID int64) {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	go func(connection connectionSnapshot, selectedFileID int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		metadata, err := connection.client.GetFileMetadata(ctx, selectedFileID)
		if err != nil {
			fyne.Do(func() {
				if !p.isCurrentOperation(connection) {
					return
				}

				if p.selectedFileID != selectedFileID {
					return
				}

				p.metadataLabel.SetText("Could not load metadata from hydrusd.\n\n" + err.Error())
			})
			return
		}

		fyne.Do(func() {
			if !p.isCurrentOperation(connection) {
				return
			}

			if p.selectedFileID != selectedFileID {
				return
			}

			p.metadataLabel.SetText(formatMetadata(metadata))
		})
	}(connection, fileID)
}

func (p *prototype) ensurePreviewResource(item daemonclient.RecentItem) {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	p.thumbnailCacheM.Lock()
	generation := p.thumbnailGen
	if _, ok := p.thumbnailCache[item.FileID]; ok {
		p.thumbnailCacheM.Unlock()
		return
	}

	if _, loading := p.thumbnailLoads[item.FileID]; loading {
		p.thumbnailCacheM.Unlock()
		return
	}

	p.thumbnailLoads[item.FileID] = struct{}{}
	p.thumbnailCacheM.Unlock()

	go func(connection connectionSnapshot, item daemonclient.RecentItem, generation uint64) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		payload, err := connection.client.FetchGridImage(ctx, item)
		resource := fyne.Resource(nil)
		overlay := "No preview"
		if err == nil && len(payload) > 0 {
			resource = fyne.NewStaticResource(
				fmt.Sprintf("file-%d-preview", item.FileID),
				payload,
			)
			overlay = ""
		}

		if !p.isCurrentOperation(connection) {
			p.clearThumbnailLoad(item.FileID, generation)
			return
		}

		p.thumbnailCacheM.Lock()
		if generation != p.thumbnailGen {
			p.thumbnailCacheM.Unlock()
			return
		}

		delete(p.thumbnailLoads, item.FileID)
		if resource != nil {
			p.thumbnailCache[item.FileID] = resource
		} else {
			p.thumbnailCache[item.FileID] = nil
		}
		p.thumbnailCacheM.Unlock()

		_ = overlay
		fyne.Do(func() {
			if !p.isCurrentOperation(connection) {
				return
			}

			p.renderGrid()
		})
	}(connection, item, generation)
}

func (p *prototype) lookupPreviewResource(fileID int64) (fyne.Resource, string) {
	p.thumbnailCacheM.Lock()
	defer p.thumbnailCacheM.Unlock()

	resource, ok := p.thumbnailCache[fileID]
	if !ok {
		return nil, "Loading"
	}

	if resource == nil {
		return nil, "No preview"
	}

	return resource, ""
}

func (p *prototype) updateActionState() {
	connected := p.currentConnection().connected
	if connected {
		p.refreshButton.Enable()
		p.addButton.Enable()
	} else {
		p.refreshButton.Disable()
		p.addButton.Disable()
	}

	if connected && p.selectedFileID > 0 {
		p.trashButton.Enable()
	} else {
		p.trashButton.Disable()
	}

	p.connectButton.Enable()
}

func (p *prototype) hasRecentFile(fileID int64) bool {
	for _, item := range p.recent {
		if item.FileID == fileID {
			return true
		}
	}

	return false
}

func (p *prototype) lookupRecentItem(fileID int64) (daemonclient.RecentItem, bool) {
	for _, item := range p.recent {
		if item.FileID == fileID {
			return item, true
		}
	}

	return daemonclient.RecentItem{}, false
}

func (p *prototype) clearSelectedPreview(message string) {
	p.setSelectedPreview(nil, message)
}

func (p *prototype) setSelectedPreview(resource fyne.Resource, message string) {
	p.previewImage.Image = tilePlaceholderImage
	p.previewImage.Resource = nil
	if resource != nil {
		p.previewImage.Image = nil
		p.previewImage.Resource = resource
	}
	p.previewImage.Refresh()
	p.previewLabel.SetText(message)
}

func (p *prototype) setStatus(text string) {
	p.activityLabel.SetText(text)
	p.statusBarLabel.SetText(text)
}

func supportsSelectedPreviewMime(mime string) bool {
	switch strings.TrimSpace(strings.ToLower(mime)) {
	case "image/gif", "image/jpeg", "image/png":
		return true
	default:
		return false
	}
}

func validateSelectedPreviewPayload(payload []byte) error {
	config, _, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("decode image config: %w", err)
	}

	if config.Width <= 0 || config.Height <= 0 {
		return fmt.Errorf("decoded image dimensions were invalid")
	}

	if config.Width > previewMaxDimension || config.Height > previewMaxDimension {
		return fmt.Errorf(
			"decoded image dimensions %dx%d exceed the preview limit of %dx%d",
			config.Width,
			config.Height,
			previewMaxDimension,
			previewMaxDimension,
		)
	}

	pixels := int64(config.Width) * int64(config.Height)
	if pixels > previewPixelLimit {
		return fmt.Errorf(
			"decoded image dimensions %dx%d exceed the preview limit of %d pixels",
			config.Width,
			config.Height,
			previewPixelLimit,
		)
	}

	return nil
}

func formatMetadata(metadata daemonclient.FileMetadata) string {
	dimensions := "unknown"
	if metadata.Width != nil && metadata.Height != nil {
		dimensions = fmt.Sprintf("%dx%d", *metadata.Width, *metadata.Height)
	}

	return strings.Join([]string{
		fmt.Sprintf("file_id: %d", metadata.FileID),
		fmt.Sprintf("hash: %s", metadata.Hash),
		fmt.Sprintf("mime: %s", metadata.MIME),
		fmt.Sprintf("size: %d bytes", metadata.Size),
		fmt.Sprintf("dimensions: %s", dimensions),
		fmt.Sprintf("local: %t", metadata.IsLocal),
		fmt.Sprintf("trashed: %t", metadata.IsTrashed),
		fmt.Sprintf("deleted: %t", metadata.IsDeleted),
	}, "\n")
}

func shortCredential(value string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "not set"
	}

	if len(normalized) <= 12 {
		return normalized
	}

	return normalized[:8] + "…" + normalized[len(normalized)-4:]
}
