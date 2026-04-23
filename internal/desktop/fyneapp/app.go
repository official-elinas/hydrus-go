//go:build fyne

package fyneapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	coreptrsync "github.com/official-elinas/hydrus-go/internal/core/ptrsync"
	"github.com/official-elinas/hydrus-go/internal/desktop/daemonclient"
)

const (
	prefsDaemonURLKey   = "daemon_url"
	prefsAccessKeyKey   = "access_key"
	recentPageLimit     = 120
	recentLoadTick      = 200 * time.Millisecond
	ptrPollTick         = time.Second
	ptrPollErrorLimit   = 3
	previewByteLimit    = 16 << 20
	previewPixelLimit   = 16_000_000
	previewMaxDimension = 8192
	watcherByteLimit    = 64 << 20
	watcherPixelLimit   = 64_000_000
	watcherMaxDimension = 16384
	tagSuggestionLimit  = 20
	defaultDaemonURL    = "http://127.0.0.1:45869"
	defaultMetadataText = "Select a file from the grid to inspect the daemon-backed metadata state.\n\nThis prototype is focused on validating daemon-backed import/trash flows and early Hydrus-like layout work, not full UI parity yet."
	defaultPreviewText  = "Select a supported still image to\npreview the daemon-served original file."
	defaultTagsText     = "Select a file to inspect tag metadata from hydrusd."
	gallerySortNewest   = "Date: newest"
	gallerySortOldest   = "Date: oldest"
	gallerySortNameAZ   = "Name: A–Z"
	gallerySortNameZA   = "Name: Z–A"
	gallerySortSizeDesc = "Size: largest"
	gallerySortSizeAsc  = "Size: smallest"
)

var gallerySortModes = []string{
	gallerySortNewest,
	gallerySortOldest,
	gallerySortNameAZ,
	gallerySortNameZA,
	gallerySortSizeDesc,
	gallerySortSizeAsc,
}

// Run launches the Fyne thin-client prototype window.
func Run() {
	prototype := newPrototype()
	prototype.window.ShowAndRun()
}

type prototype struct {
	app             fyne.App
	window          fyne.Window
	connectWindow   fyne.Window
	ptrWindow       fyne.Window
	tagEditorWindow fyne.Window
	watcherWindow   fyne.Window
	stateMu         sync.RWMutex
	client          *daemonclient.Client

	connectButton         *widget.Button
	refreshButton         *widget.Button
	addButton             *widget.Button
	addFolderButton       *widget.Button
	searchEntry           *widget.Entry
	gallerySortSelect     *widget.Select
	searchSuggestionsList *widget.List
	ptrRefreshButton      *widget.Button
	clearQueueButton      *widget.Button
	editTagsButton        *widget.Button
	retrySelectedButton   *widget.Button
	removeSelectedButton  *widget.Button
	retryFailedButton     *widget.Button
	clearFinishedButton   *widget.Button
	trashButton           *widget.Button
	ptrSyncButton         *widget.Button

	connectionLabel   *widget.Label
	queueSummaryLabel *widget.Label
	queueDetailLabel  *widget.Label
	leftTagsRichText  *widget.RichText
	searchHintLabel   *widget.Label
	previewImage      *canvas.Image
	previewLabel      *widget.Label
	metadataLabel     *widget.Label
	tagsRichText      *widget.RichText
	activityLabel     *widget.Label
	statusBarLabel    *widget.Label
	ptrStatusLabel    *widget.Label
	ptrHeadlineLabel  *widget.Label
	ptrProgressBar    *widget.ProgressBarInfinite
	queueList         *widget.List
	gridHost          *fyne.Container
	gridWrap          *widget.GridWrap

	recent             []daemonclient.RecentItem
	recentLimit        int
	recentNextOffset   int
	recentHasMore      bool
	recentLoadBusy     bool
	galleryFilterQuery string
	gallerySortMode    string
	searchSuggestions  []string
	searchRequestID    uint64
	selectedFileID     int64
	connected          bool
	connectionGen      uint64
	connectAttemptID   uint64
	selectedPreviewCache  map[string]fyne.Resource
	selectedPreviewCacheM sync.Mutex
	thumbnailCache     map[int64]fyne.Resource
	thumbnailLoads     map[int64]struct{}
	thumbnailGen       uint64
	thumbnailCacheM    sync.Mutex
	tileMetadataCache  map[int64]daemonclient.FileMetadata
	tileMetadataLoads  map[int64]struct{}
	tileMetadataGen    uint64
	tileMetadataMu     sync.Mutex
	previewRequestID   uint64
	previewCancel      context.CancelFunc
	previewRequestM    sync.Mutex
	watcherRequestID   uint64
	watcherCancel      context.CancelFunc
	watcherRequestM    sync.Mutex
	ptrStatus          coreptrsync.Status
	ptrStatusLoaded    bool
	ptrStatusBusy      bool
	ptrStatusRequest   uint64

	queueMu            sync.Mutex
	importQueue        []importQueueEntry
	importQueueRunning bool
	selectedQueueIndex int
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
	window.Resize(fyne.NewSize(1440, 860))
	window.SetPadded(false)

	p := &prototype{
		app:                application,
		window:             window,
		client:             daemonclient.New(),
		recentLimit:        recentPageLimit,
		gallerySortMode:    gallerySortNewest,
		selectedQueueIndex: -1,
		selectedPreviewCache: map[string]fyne.Resource{},
		thumbnailCache:     map[int64]fyne.Resource{},
		thumbnailLoads:     map[int64]struct{}{},
		tileMetadataCache:  map[int64]daemonclient.FileMetadata{},
		tileMetadataLoads:  map[int64]struct{}{},
	}

	p.connectionLabel = widget.NewLabel("")
	p.connectionLabel.Wrapping = fyne.TextTruncate
	p.queueSummaryLabel = widget.NewLabel(formatImportQueueSummary(nil, false))
	p.queueSummaryLabel.Wrapping = fyne.TextTruncate
	p.queueDetailLabel = widget.NewLabel(defaultSelectedQueueText())
	p.queueDetailLabel.Wrapping = fyne.TextTruncate
	p.leftTagsRichText = widget.NewRichText()
	p.leftTagsRichText.Wrapping = fyne.TextWrapWord
	p.setLeftTagsText(defaultTagsText)
	p.searchHintLabel = widget.NewLabel("Autocomplete suggestions appear here when connected to hydrusd.")
	p.searchHintLabel.Wrapping = fyne.TextWrapWord
	p.previewImage = canvas.NewImageFromImage(nil)
	p.previewImage.FillMode = canvas.ImageFillContain
	p.previewImage.Hide()
	p.previewLabel = widget.NewLabel(defaultPreviewText)
	p.previewLabel.Wrapping = fyne.TextTruncate
	p.previewLabel.Alignment = fyne.TextAlignCenter
	p.tagsRichText = widget.NewRichText()
	p.tagsRichText.Wrapping = fyne.TextWrapWord
	p.setRightTagsText(defaultTagsText)
	p.metadataLabel = widget.NewLabel(defaultMetadataText)
	p.metadataLabel.Wrapping = fyne.TextWrapWord
	p.activityLabel = widget.NewLabel("No actions yet.")
	p.activityLabel.Wrapping = fyne.TextTruncate
	p.statusBarLabel = widget.NewLabel("Ready. Connect to hydrusd to start the prototype.")
	p.statusBarLabel.Wrapping = fyne.TextTruncate
	p.ptrHeadlineLabel = widget.NewLabelWithStyle("PTR sync: offline", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	p.ptrStatusLabel = widget.NewLabel("PTR sync status: offline")
	p.ptrStatusLabel.Wrapping = fyne.TextTruncate
	p.ptrProgressBar = widget.NewProgressBarInfinite()
	p.ptrProgressBar.Hide()
	p.queueList = widget.NewList(
		p.importQueueLength,
		makeImportQueueListItem,
		p.updateImportQueueListItem,
	)
	p.queueList.OnSelected = p.selectImportQueueEntry
	p.queueList.OnUnselected = p.unselectImportQueueEntry
	p.searchSuggestionsList = widget.NewList(
		func() int { return len(p.searchSuggestions) },
		func() fyne.CanvasObject {
			button := widget.NewButton("", nil)
			button.Alignment = widget.ButtonAlignLeading
			return button
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			button := item.(*widget.Button)
			if id < 0 || id >= len(p.searchSuggestions) {
				button.SetText("")
				button.OnTapped = nil
				button.Disable()
				return
			}

			suggestion := p.searchSuggestions[id]
			button.SetText(suggestion)
			button.OnTapped = func() {
				p.searchEntry.SetText(suggestion)
			}
			button.Enable()
		},
	)
	p.searchSuggestionsList.HideSeparators = true
	p.searchSuggestionsList.Hide()
	p.gridHost = container.NewMax()

	p.connectButton = widget.NewButton("Connect", p.showConnectDialog)
	p.refreshButton = widget.NewButton("Refresh", func() {
		p.fetchPTRStatus()
		p.reloadRecent(p.selectedFileID, "Refreshed recent files from hydrusd.")
	})
	p.ptrRefreshButton = widget.NewButton("Refresh PTR Status", p.fetchPTRStatus)
	p.addButton = widget.NewButton("Add File", p.showImportDialog)
	p.addFolderButton = widget.NewButton("Add Folder", p.showImportFolderDialog)
	p.searchEntry = widget.NewEntry()
	p.searchEntry.SetPlaceHolder("Search loaded recent files by tag, hash, or label")
	p.searchEntry.OnChanged = func(value string) {
		p.galleryFilterQuery = strings.TrimSpace(value)
		p.renderGrid()
		p.refreshSearchSuggestions()
	}
	p.gallerySortSelect = widget.NewSelect(gallerySortModes, func(value string) {
		if strings.TrimSpace(value) == "" {
			return
		}

		p.gallerySortMode = value
		p.renderGrid()
	})
	p.gallerySortSelect.SetSelected(p.gallerySortMode)
	p.retrySelectedButton = widget.NewButton("Retry Selected", p.retrySelectedQueueEntry)
	p.removeSelectedButton = widget.NewButton("Remove Selected", p.removeSelectedQueueEntry)
	p.retryFailedButton = widget.NewButton("Retry Failed", p.retryFailedQueueEntries)
	p.clearFinishedButton = widget.NewButton("Clear Finished", p.clearFinishedQueueEntries)
	p.clearQueueButton = widget.NewButton("Clear Queue", p.clearImportQueue)
	p.editTagsButton = widget.NewButton("Edit Tags", p.showEditTagsDialog)
	p.trashButton = widget.NewButton("Trash Selected", p.confirmTrashSelected)
	p.ptrSyncButton = widget.NewButton("Manual Sync", p.triggerPTRSync)
	p.ptrSyncButton.Disable()

	p.window.SetMainMenu(p.buildMainMenu())
	p.window.SetContent(p.buildContent())
	p.window.SetOnDropped(p.handleDroppedItems)
	p.loadSavedConnection()
	p.updateActionState()
	p.renderImportQueue()
	p.renderGrid()
	go p.monitorRecentGridScroll()

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

func (p *prototype) beginPTRStatusRequest() uint64 {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	p.ptrStatusBusy = true
	p.ptrStatusRequest++

	return p.ptrStatusRequest
}

func (p *prototype) cancelPTRStatusRequests() {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	p.ptrStatusBusy = false
	p.ptrStatusRequest++
}

func (p *prototype) finishPTRStatusRequest(requestID uint64) bool {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	if p.ptrStatusRequest != requestID {
		return false
	}

	p.ptrStatusBusy = false
	return true
}

func (p *prototype) isCurrentPTRStatusRequest(requestID uint64) bool {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()

	return p.ptrStatusRequest == requestID
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

func (p *prototype) resetConnectionAttemptState() {
	p.cancelPreviewRequest()
	p.cancelWatcherRequest()
	p.cancelPTRStatusRequests()

	p.thumbnailCacheM.Lock()
	p.thumbnailGen++
	p.thumbnailLoads = map[int64]struct{}{}
	p.thumbnailCacheM.Unlock()

	p.tileMetadataMu.Lock()
	p.tileMetadataGen++
	p.tileMetadataLoads = map[int64]struct{}{}
	p.tileMetadataMu.Unlock()

	p.stateMu.Lock()
	p.ptrStatusLoaded = false
	p.stateMu.Unlock()
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

func (p *prototype) beginWatcherRequest(timeout time.Duration) (context.Context, context.CancelFunc, uint64) {
	p.watcherRequestM.Lock()
	defer p.watcherRequestM.Unlock()

	if p.watcherCancel != nil {
		p.watcherCancel()
	}

	p.watcherRequestID++
	requestID := p.watcherRequestID
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	p.watcherCancel = cancel

	return ctx, cancel, requestID
}

func (p *prototype) cancelWatcherRequest() {
	p.watcherRequestM.Lock()
	defer p.watcherRequestM.Unlock()

	p.watcherRequestID++
	if p.watcherCancel != nil {
		p.watcherCancel()
		p.watcherCancel = nil
	}
}

func (p *prototype) finishWatcherRequest(requestID uint64) {
	p.watcherRequestM.Lock()
	defer p.watcherRequestM.Unlock()

	if p.watcherRequestID == requestID {
		p.watcherCancel = nil
	}
}

func (p *prototype) isCurrentWatcherRequest(requestID uint64) bool {
	p.watcherRequestM.Lock()
	defer p.watcherRequestM.Unlock()

	return p.watcherRequestID == requestID
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
	toolbar := widget.NewToolbar(
		widget.NewToolbarAction(theme.LoginIcon(), p.showConnectDialog),
		widget.NewToolbarAction(theme.ViewRefreshIcon(), func() {
			p.fetchPTRStatus()
			p.reloadRecent(p.selectedFileID, "Refreshed recent files from hydrusd.")
		}),
		widget.NewToolbarSeparator(),
		widget.NewToolbarAction(theme.ContentAddIcon(), p.showImportDialog),
		widget.NewToolbarAction(theme.FolderOpenIcon(), p.showImportFolderDialog),
		widget.NewToolbarAction(theme.DeleteIcon(), p.confirmTrashSelected),
	)

	previewPanel := container.NewStack(
		canvas.NewRectangle(color.NRGBA{R: 18, G: 18, B: 20, A: 255}),
		p.previewImage,
		container.NewPadded(p.previewLabel),
	)
	previewSection := container.NewBorder(
		widget.NewLabelWithStyle("Selected preview", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil,
		nil,
		nil,
		newMinSizeBox(container.NewPadded(previewPanel), fyne.NewSize(360, 240)),
	)

	tagsScroll := container.NewVScroll(p.tagsRichText)
	tagSection := container.NewBorder(
		widget.NewLabelWithStyle("Selection tags", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewPadded(p.editTagsButton),
		nil,
		nil,
		container.NewPadded(tagsScroll),
	)

	metadataScroll := container.NewVScroll(p.metadataLabel)
	metadataSection := container.NewBorder(
		widget.NewLabelWithStyle("Selected file", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil,
		nil,
		nil,
		container.NewPadded(metadataScroll),
	)

	tagAndMetadataPane := container.NewVSplit(tagSection, metadataSection)
	tagAndMetadataPane.SetOffset(0.40)

	detailPane := container.NewVSplit(previewSection, tagAndMetadataPane)
	detailPane.SetOffset(0.42)
	detailPaneHost := newMinSizeBox(detailPane, fyne.NewSize(360, 480))

	queueHelp := widget.NewLabel(
		"Queue files with Add File, Add Folder, or by dragging\nfiles and folders anywhere into the window.",
	)

	queueActionButtons := container.NewVBox(
		container.NewGridWithColumns(2, p.retrySelectedButton, p.removeSelectedButton),
		container.NewGridWithColumns(2, p.retryFailedButton, p.clearFinishedButton),
		p.clearQueueButton,
	)

	introLabel := widget.NewLabel("A thin Fyne shell for testing daemon-backed Hydrus\nparity work without direct DB or managed-file access.")

	queueHeader := container.NewPadded(container.NewVBox(
		widget.NewLabelWithStyle("hydrusd prototype", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		introLabel,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Connection", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.connectionLabel,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Search loaded recent files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.searchEntry,
		widget.NewLabelWithStyle("Sort grid", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.gallerySortSelect,
		p.searchHintLabel,
		p.searchSuggestionsList,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Import queue", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		queueHelp,
		p.queueSummaryLabel,
		queueActionButtons,
	))

	tagsPane := container.NewBorder(
		widget.NewLabelWithStyle("Selected file tags", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil,
		nil,
		nil,
		container.NewPadded(container.NewVScroll(p.leftTagsRichText)),
	)

	queueFooter := container.NewPadded(container.NewVBox(
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Selected queue item", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.queueDetailLabel,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Last action", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.activityLabel,
	))

	leftSplit := container.NewVSplit(
		container.NewPadded(p.queueList),
		container.NewPadded(tagsPane),
	)
	leftSplit.SetOffset(0.62)

	queuePane := container.NewBorder(
		queueHeader,
		queueFooter,
		nil,
		nil,
		leftSplit,
	)

	galleryDetailSplit := container.NewHSplit(container.NewPadded(p.gridHost), detailPaneHost)
	galleryDetailSplit.SetOffset(0.50)

	mainSplit := container.NewHSplit(queuePane, galleryDetailSplit)
	mainSplit.SetOffset(0.25)

	return container.NewBorder(
		container.NewVBox(toolbar, widget.NewSeparator()),
		container.NewPadded(p.statusBarLabel),
		nil,
		nil,
		mainSplit,
	)
}

func (p *prototype) buildMainMenu() *fyne.MainMenu {
	showPlanned := func(title string, body string) func() {
		return func() {
			dialog.ShowInformation(title, body, p.window)
		}
	}

	fileMenu := fyne.NewMenu("File",
		fyne.NewMenuItem("Connect", p.showConnectDialog),
		fyne.NewMenuItem("Refresh", func() {
			p.fetchPTRStatus()
			p.reloadRecent(p.selectedFileID, "Refreshed recent files from hydrusd.")
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Add File", p.showImportDialog),
		fyne.NewMenuItem("Add Folder", p.showImportFolderDialog),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Quit", func() {
			p.window.Close()
		}),
	)

	pagesMenu := fyne.NewMenu("Pages",
		fyne.NewMenuItem("Reload Recent", func() {
			p.reloadRecent(p.selectedFileID, "Refreshed recent files from hydrusd.")
		}),
		fyne.NewMenuItem("Focus Grid", showPlanned("Pages", "Recent files are loaded into the center grid. More page controls can grow here as the desktop gets closer to Hydrus.")),
	)

	databaseMenu := fyne.NewMenu("Database",
		fyne.NewMenuItem("Edit Selected Tags", p.showEditTagsDialog),
		fyne.NewMenuItem("Trash Selected", p.confirmTrashSelected),
		fyne.NewMenuItem("Run Integrity Check", p.triggerDBIntegrityCheck),
		fyne.NewMenuItem("Library Details", showPlanned("Database", "Database-oriented actions are still daemon-backed. This menu is the landing point for future library maintenance actions.")),
	)

	networkMenu := fyne.NewMenu("Network",
		fyne.NewMenuItem("PTR Sync", p.showPTRWindow),
		fyne.NewMenuItem("Reconnect", p.showConnectDialog),
	)

	servicesMenu := fyne.NewMenu("Services",
		fyne.NewMenuItem("Connection Summary", showPlanned("Services", p.connectionLabel.Text)),
		fyne.NewMenuItem("PTR Summary", func() {
			dialog.ShowInformation("PTR Sync", p.ptrStatusLabel.Text, p.window)
		}),
	)

	helpMenu := fyne.NewMenu("Help",
		fyne.NewMenuItem("About", showPlanned("hydrusd thin prototype", "A daemon-backed Fyne shell for testing Hydrus parity work without direct SQLite or managed-file access.")),
		fyne.NewMenuItem("Current Desktop Scope", showPlanned("Desktop scope", "This thin client focuses on connection, browse, import, trash, preview, and PTR state while the daemon remains the source of truth.")),
	)

	return fyne.NewMainMenu(
		fileMenu,
		pagesMenu,
		databaseMenu,
		networkMenu,
		servicesMenu,
		helpMenu,
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
	if p.connectWindow != nil {
		p.connectWindow.RequestFocus()
		return
	}

	baseURL := widget.NewEntry()
	baseURL.SetPlaceHolder(defaultDaemonURL)
	baseURL.SetText(p.app.Preferences().StringWithFallback(prefsDaemonURLKey, defaultDaemonURL))

	accessKey := widget.NewEntry()
	accessKey.SetPlaceHolder("64-character access key")
	accessKey.SetText(p.app.Preferences().StringWithFallback(prefsAccessKeyKey, ""))
	accessKey.Password = true

	connectWindow := p.app.NewWindow("Connect to hydrusd")
	connectWindow.Resize(fyne.NewSize(640, 240))
	connectWindow.SetPadded(true)
	connectWindow.SetOnClosed(func() {
		if p.connectWindow == connectWindow {
			p.connectWindow = nil
		}
	})

	form := widget.NewForm(
		widget.NewFormItem("Daemon URL", baseURL),
		widget.NewFormItem("Access key", accessKey),
	)
	form.SubmitText = "Connect"
	form.CancelText = "Cancel"
	form.OnSubmit = func() {
		p.connectToDaemon(baseURL.Text, accessKey.Text)
		connectWindow.Close()
	}
	form.OnCancel = func() {
		connectWindow.Close()
	}

	connectWindow.SetContent(container.NewBorder(
		widget.NewLabel("Use the daemon URL and API key from hydrusd. This window is resizable for longer hosts and credentials."),
		nil,
		nil,
		nil,
		container.NewPadded(form),
	))

	p.connectWindow = connectWindow
	connectWindow.Show()
}

func (p *prototype) showPTRWindow() {
	if p.ptrWindow != nil {
		p.ptrWindow.RequestFocus()
		return
	}

	p.updateActionState()

	ptrWindow := p.app.NewWindow("PTR Sync")
	ptrWindow.Resize(fyne.NewSize(560, 420))
	ptrWindow.SetPadded(true)
	ptrWindow.SetOnClosed(func() {
		if p.ptrWindow == ptrWindow {
			p.ptrWindow = nil
		}
	})

	content := container.NewBorder(
		widget.NewLabel("PTR sync runs in the daemon background. Use this window to refresh status or trigger a manual sync."),
		container.NewGridWithColumns(2, p.ptrRefreshButton, p.ptrSyncButton),
		nil,
		nil,
		container.NewPadded(container.NewVBox(
			p.ptrHeadlineLabel,
			p.ptrProgressBar,
			widget.NewSeparator(),
			p.ptrStatusLabel,
		)),
	)

	ptrWindow.SetContent(content)
	p.ptrWindow = ptrWindow
	ptrWindow.Show()
	p.fetchPTRStatus()
}

func (p *prototype) showEditTagsDialog() {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil || p.selectedFileID <= 0 {
		return
	}

	fileID := p.selectedFileID
	if p.tagEditorWindow != nil {
		p.tagEditorWindow.Close()
	}

	window := p.app.NewWindow(fmt.Sprintf("Edit tags • file_id %d", fileID))
	window.Resize(fyne.NewSize(720, 640))
	window.SetPadded(true)
	window.SetOnClosed(func() {
		if p.tagEditorWindow == window {
			p.tagEditorWindow = nil
		}
	})

	metadataLabel := widget.NewLabel("Loading selected-file metadata from hydrusd...")
	metadataLabel.Wrapping = fyne.TextWrapWord

	tagsLabel := widget.NewLabel("Loading tag metadata from hydrusd...")
	tagsLabel.Wrapping = fyne.TextWrapWord

	entry := widget.NewMultiLineEntry()
	entry.SetPlaceHolder("Enter one or more tags, separated by commas or newlines")
	entry.Wrapping = fyne.TextWrapWord
	entry.SetMinRowsVisible(3)

	suggestionList := widget.NewList(
		func() int { return 0 },
		func() fyne.CanvasObject {
			button := widget.NewButton("", nil)
			button.Alignment = widget.ButtonAlignLeading
			return button
		},
		func(widget.ListItemID, fyne.CanvasObject) {},
	)
	suggestionList.HideSeparators = true
	suggestionList.OnSelected = func(id widget.ListItemID) {
		suggestionList.UnselectAll()
	}

	suggestionHint := widget.NewLabel("Suggestions come from the currently loaded tags for this file.")
	suggestionHint.Wrapping = fyne.TextWrapWord

	statusLabel := widget.NewLabel("Load current metadata to inspect tags before staging pending PTR mappings.")
	statusLabel.Wrapping = fyne.TextWrapWord

	stageButton := widget.NewButton("Stage Pending", nil)
	commitButton := widget.NewButton("Commit Pending", nil)
	refreshButton := widget.NewButton("Reload Metadata", nil)
	closeButton := widget.NewButton("Close", func() {
		window.Close()
	})

	stageButton.Disable()
	commitButton.Disable()

	suggestions := []string{}
	localSuggestions := []string{}
	activeSuggestionRequestID := uint64(0)
	renderSuggestions := func(next []string) {
		suggestions = append([]string(nil), next...)
		if len(suggestions) == 0 {
			suggestionHint.SetText("No local suggestions are available for the selected file yet.")
		} else {
			suggestionHint.SetText("Click a suggestion to add it to the staging input.")
		}

		suggestionList.Length = func() int { return len(suggestions) }
		suggestionList.UpdateItem = func(id widget.ListItemID, item fyne.CanvasObject) {
			button := item.(*widget.Button)
			if id < 0 || id >= len(suggestions) {
				button.SetText("")
				button.OnTapped = nil
				button.Disable()
				return
			}

			tag := suggestions[id]
			button.SetText(tag)
			button.OnTapped = func() {
				entry.SetText(appendTagEditorInput(entry.Text, tag))
			}
			button.Enable()
		}
		suggestionList.Refresh()
	}

	refreshSuggestions := func() {
		prefix := currentTagEditorPrefix(entry.Text)
		if prefix == "" {
			renderSuggestions(localSuggestions)
			return
		}

		filteredLocal := filterTagSuggestions(localSuggestions, prefix)
		activeSuggestionRequestID++
		requestID := activeSuggestionRequestID

		go func(connection connectionSnapshot, prefix string, filteredLocal []string, requestID uint64) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			remoteSuggestions, err := connection.client.SuggestTags(ctx, prefix, tagSuggestionLimit)
			fyne.Do(func() {
				if p.tagEditorWindow != window || !p.isCurrentOperation(connection) || requestID != activeSuggestionRequestID {
					return
				}

				if err != nil {
					if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
						renderSuggestions(filteredLocal)
					}
					return
				}

				renderSuggestions(mergeTagSuggestions(filteredLocal, remoteSuggestions))
			})
		}(connection, prefix, filteredLocal, requestID)
	}

	updateStageButton := func() {
		if len(parseTagEditorInput(entry.Text)) > 0 {
			stageButton.Enable()
			return
		}

		stageButton.Disable()
	}
	entry.OnChanged = func(string) {
		updateStageButton()
		refreshSuggestions()
	}

	setBusy := func(busy bool) {
		if busy {
			stageButton.Disable()
			commitButton.Disable()
			refreshButton.Disable()
			entry.Disable()
			return
		}

		refreshButton.Enable()
		entry.Enable()
		commitButton.Enable()
		updateStageButton()
	}

	refreshMetadata := func(message string) {
		statusLabel.SetText(message)
		setBusy(true)

		go func(connection connectionSnapshot, fileID int64) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			metadata, err := connection.client.GetFileMetadata(ctx, fileID)
			fyne.Do(func() {
				if p.tagEditorWindow != window || !p.isCurrentOperation(connection) {
					return
				}

				if err != nil {
					statusLabel.SetText("Could not load metadata from hydrusd.")
					metadataLabel.SetText("Could not load selected-file metadata from hydrusd.\n\n" + err.Error())
					tagsLabel.SetText("Could not load tag metadata from hydrusd.")
					localSuggestions = nil
					renderSuggestions(nil)
					setBusy(false)
					commitButton.Disable()
					return
				}

				metadataLabel.SetText(formatMetadataDetails(metadata))
				tagsLabel.SetText(formatTagMetadata(metadata))
				localSuggestions = collectTagEditorSuggestions(metadata)
				refreshSuggestions()
				statusLabel.SetText("Loaded selected-file metadata. Stage tags to create pending PTR mappings or commit existing pending mappings.")
				setBusy(false)
			})
		}(connection, fileID)
	}

	stageButton.OnTapped = func() {
		parsedTags := parseTagEditorInput(entry.Text)
		if len(parsedTags) == 0 {
			statusLabel.SetText("Enter at least one tag before staging pending mappings.")
			updateStageButton()
			return
		}

		statusLabel.SetText("Staging pending PTR mappings through hydrusd...")
		setBusy(true)

		go func(connection connectionSnapshot, fileID int64, tags []string) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			result, err := connection.client.AddPendingMappings(ctx, coreptrsync.PendingMappingsRequest{
				FileIDs: []int64{fileID},
				Tags:    tags,
			})

			fyne.Do(func() {
				if p.tagEditorWindow != window || !p.isCurrentOperation(connection) {
					return
				}

				if err != nil {
					statusLabel.SetText("Staging pending mappings failed.")
					setBusy(false)
					dialog.ShowError(err, window)
					return
				}

				entry.SetText("")
				statusLabel.SetText(fmt.Sprintf("Staged %d pending PTR mapping(s).", result.AddedMappings))
				p.setStatus(fmt.Sprintf("Staged %d pending PTR mapping(s) for file_id %d.", result.AddedMappings, fileID))
				p.loadSelectedMetadata(fileID)
				p.fetchPTRStatus()
				refreshMetadata("Reloading metadata after staging pending mappings...")
			})
		}(connection, fileID, parsedTags)
	}

	commitButton.OnTapped = func() {
		statusLabel.SetText("Committing pending PTR mappings through hydrusd...")
		setBusy(true)

		go func(connection connectionSnapshot, fileID int64) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			result, err := connection.client.CommitPending(ctx, coreptrsync.CommitPendingRequest{})

			fyne.Do(func() {
				if p.tagEditorWindow != window || !p.isCurrentOperation(connection) {
					return
				}

				if err != nil {
					statusLabel.SetText("Commit pending failed.")
					setBusy(false)
					dialog.ShowError(err, window)
					return
				}

				statusLabel.SetText(fmt.Sprintf("Committed %d pending PTR mapping(s).", result.CommittedMappings))
				p.setStatus(fmt.Sprintf("Committed %d pending PTR mapping(s) from hydrusd.", result.CommittedMappings))
				p.loadSelectedMetadata(fileID)
				p.fetchPTRStatus()
				refreshMetadata("Reloading metadata after committing pending mappings...")
			})
		}(connection, fileID)
	}

	refreshButton.OnTapped = func() {
		refreshMetadata("Refreshing selected-file metadata from hydrusd...")
	}

	content := container.NewBorder(
		container.NewPadded(widget.NewLabel(
			"Use this popup to stage pending PTR tags for the selected file. Suggestions are local-only for now and come from the daemon metadata already loaded for this file.",
		)),
		container.NewPadded(container.NewVBox(
			statusLabel,
			widget.NewSeparator(),
			container.NewGridWithColumns(4, stageButton, commitButton, refreshButton, closeButton),
		)),
		nil,
		nil,
		container.NewPadded(container.NewVSplit(
			container.NewVSplit(
				container.NewBorder(
					widget.NewLabelWithStyle("Selected file metadata", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
					nil,
					nil,
					nil,
					container.NewVScroll(metadataLabel),
				),
				container.NewBorder(
					widget.NewLabelWithStyle("Current tags", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
					nil,
					nil,
					nil,
					container.NewVScroll(tagsLabel),
				),
			),
			container.NewBorder(
				widget.NewLabelWithStyle("Stage new pending tags", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				nil,
				nil,
				nil,
				container.NewVBox(
					entry,
					widget.NewSeparator(),
					suggestionHint,
					container.NewVScroll(suggestionList),
				),
			),
		)),
	)

	window.SetContent(content)
	p.tagEditorWindow = window
	window.Show()
	window.RequestFocus()
	refreshMetadata("Loading selected-file metadata from hydrusd...")
}

func (p *prototype) connectToDaemon(baseURL string, accessKey string) {
	candidate := daemonclient.New()
	if err := candidate.SetConnection(baseURL, accessKey); err != nil {
		dialog.ShowError(err, p.window)
		return
	}

	p.resetConnectionAttemptState()
	attemptID, wasConnected := p.beginConnectAttempt()
	p.setPTRVisualState("PTR sync: offline", false)
	p.ptrStatusLabel.SetText("PTR sync status: offline")
	p.setStatus("Connecting to hydrusd...")
	p.connectButton.Disable()
	p.refreshButton.Disable()
	p.addButton.Disable()
	p.addFolderButton.Disable()
	p.clearQueueButton.Disable()
	p.trashButton.Disable()
	p.ptrSyncButton.Disable()

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
					p.startImportQueueProcessor()
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
					p.startImportQueueProcessor()
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
					p.startImportQueueProcessor()
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
			p.applyRecentPage(page, 0)
			p.startImportQueueProcessor()
			p.setStatus(fmt.Sprintf("Connected to hydrusd and loaded %d recent files.", len(page.Items)))
			p.fetchPTRStatus()
		})
	}(attemptID, wasConnected)
}

func (p *prototype) showImportDialog() {
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
			dialog.ShowError(fmt.Errorf("only local file selections are supported by the desktop prototype"), p.window)
			return
		}

		path := filepath.Clean(uri.Path())
		if strings.TrimSpace(path) == "" {
			dialog.ShowError(fmt.Errorf("selected file path was empty"), p.window)
			return
		}

		p.queueImportSources([]string{path}, "file picker")
	}, p.window)
}

func (p *prototype) showImportFolderDialog() {
	dialog.ShowFolderOpen(func(listable fyne.ListableURI, err error) {
		if err != nil {
			dialog.ShowError(err, p.window)
			return
		}

		if listable == nil {
			return
		}

		path := filepath.Clean(listable.Path())
		if strings.TrimSpace(path) == "" {
			dialog.ShowError(fmt.Errorf("selected folder path was empty"), p.window)
			return
		}

		p.queueImportSources([]string{path}, "folder picker")
	}, p.window)
}

func (p *prototype) handleDroppedItems(_ fyne.Position, items []fyne.URI) {
	paths, rejected := droppedLocalPaths(items)
	if len(paths) == 0 {
		if len(rejected) > 0 {
			dialog.ShowInformation(
				"Nothing importable was dropped",
				formatRejectedImportItems(rejected),
				p.window,
			)
		}
		return
	}

	p.queueImportSources(paths, "drag and drop")
	if len(rejected) > 0 {
		p.setStatus(
			fmt.Sprintf(
				"Queued dropped items and skipped %d unsupported drop target(s).",
				len(rejected),
			),
		)
	}
}

func (p *prototype) queueImportSources(paths []string, source string) {
	if len(paths) == 0 {
		return
	}

	inputs := append([]string(nil), paths...)
	p.setStatus(fmt.Sprintf("Preparing %d import source(s) from %s...", len(inputs), source))

	go func(source string, inputs []string) {
		resolved, skipped, err := expandImportSelection(inputs)
		if err != nil {
			fyne.Do(func() {
				dialog.ShowError(err, p.window)
				p.setStatus("Preparing import sources failed.")
			})
			return
		}

		fyne.Do(func() {
			added, duplicates := p.appendImportQueueEntries(resolved, source)
			p.renderImportQueue()
			p.updateActionState()

			status := fmt.Sprintf("Queued %d file(s) from %s.", added, source)
			if duplicates > 0 {
				status = fmt.Sprintf(
					"Queued %d file(s) from %s and skipped %d already queued path(s).",
					added,
					source,
					duplicates,
				)
			}
			if added == 0 && len(skipped) > 0 {
				status = "No importable files were added to the queue."
			}
			if len(skipped) > 0 && added > 0 {
				status += fmt.Sprintf(" Skipped %d unsupported or empty source(s).", len(skipped))
			}
			if !p.currentConnection().connected && added > 0 {
				status += " Connect to hydrusd to process the queue."
			}

			p.setStatus(status)

			if len(skipped) > 0 && added == 0 {
				dialog.ShowInformation(
					"No importable files were found",
					formatRejectedImportItems(skipped),
					p.window,
				)
			}

			p.startImportQueueProcessor()
		})
	}(source, inputs)
}

func (p *prototype) appendImportQueueEntries(paths []string, source string) (int, int) {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()

	existing := map[string]int{}
	for index, entry := range p.importQueue {
		existing[entry.Path] = index
	}

	added := 0
	duplicates := 0
	for _, path := range paths {
		normalized := filepath.Clean(strings.TrimSpace(path))
		if normalized == "." || normalized == "" {
			continue
		}

		if existingIndex, exists := existing[normalized]; exists {
			if p.importQueue[existingIndex].Status == importQueueStatusFailed {
				p.importQueue[existingIndex].Status = importQueueStatusPending
				p.importQueue[existingIndex].Source = source
				p.importQueue[existingIndex].Detail = "Retrying after a previous failure."
				p.importQueue[existingIndex].FileID = 0
				added++
				continue
			}

			duplicates++
			continue
		}

		existing[normalized] = len(p.importQueue)
		p.importQueue = append(p.importQueue, importQueueEntry{
			Path:   normalized,
			Source: source,
			Status: importQueueStatusPending,
			Detail: "Waiting to upload through hydrusd.",
		})
		added++
	}

	return added, duplicates
}

func (p *prototype) startImportQueueProcessor() {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	p.queueMu.Lock()
	if p.importQueueRunning {
		p.queueMu.Unlock()
		return
	}

	hasPending := false
	for _, entry := range p.importQueue {
		if entry.Status == importQueueStatusPending {
			hasPending = true
			break
		}
	}
	if !hasPending {
		p.queueMu.Unlock()
		return
	}

	p.importQueueRunning = true
	p.queueMu.Unlock()

	p.renderImportQueue()
	p.updateActionState()

	go p.processImportQueue(connection)
}

func (p *prototype) processImportQueue(connection connectionSnapshot) {
	lastSuccessfulFileID := int64(0)
	importedCount := 0
	duplicateCount := 0
	failedCount := 0
	paused := false

	for {
		if !p.isCurrentOperation(connection) {
			paused = true
			p.queueMu.Lock()
			p.importQueueRunning = false
			p.queueMu.Unlock()
			break
		}

		index, path, ok := p.beginNextImportQueueEntry()
		if !ok {
			break
		}

		fyne.Do(func() {
			if p.isCurrentOperation(connection) {
				p.renderImportQueue()
				p.updateActionState()
				p.setStatus(fmt.Sprintf("Uploading %s through hydrusd...", filepath.Base(path)))
			}
		})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		result, err := connection.client.UploadFile(ctx, path)
		cancel()

		if !p.isCurrentOperation(connection) {
			paused = true
			p.requeueImportQueueEntry(index, "Waiting to resume after connection change.")
			p.queueMu.Lock()
			p.importQueueRunning = false
			p.queueMu.Unlock()
			break
		}

		if err != nil {
			failedCount++
			p.finishImportQueueEntry(index, importQueueStatusFailed, 0, err.Error())
			fyne.Do(func() {
				if p.isCurrentOperation(connection) {
					p.renderImportQueue()
					p.updateActionState()
				}
			})
			continue
		}

		status := importQueueStatusImported
		detail := fmt.Sprintf("Imported as file_id %d.", result.FileID)
		if result.AlreadyImported {
			status = importQueueStatusDuplicate
			detail = fmt.Sprintf("Already present as file_id %d.", result.FileID)
			duplicateCount++
		} else {
			importedCount++
		}

		if result.FileID > 0 {
			lastSuccessfulFileID = result.FileID
		}

		p.finishImportQueueEntry(index, status, result.FileID, detail)
		fyne.Do(func() {
			if p.isCurrentOperation(connection) {
				p.renderImportQueue()
				p.updateActionState()
			}
		})
	}

	if paused {
		fyne.Do(func() {
			p.renderImportQueue()
			p.updateActionState()
			p.setStatus("Import queue paused until the hydrusd connection is restored.")
			p.startImportQueueProcessor()
		})
		return
	}

	status := fmt.Sprintf(
		"Import queue finished: %d imported, %d duplicates, %d failed.",
		importedCount,
		duplicateCount,
		failedCount,
	)

	fyne.Do(func() {
		if !p.isCurrentOperation(connection) {
			return
		}

		p.renderImportQueue()
		p.updateActionState()
		if importedCount > 0 || duplicateCount > 0 {
			p.loadRecentPage(0, lastSuccessfulFileID, status, false)
			return
		}

		p.setStatus(status)
	})
}

func (p *prototype) beginNextImportQueueEntry() (int, string, bool) {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()

	for index, entry := range p.importQueue {
		if entry.Status != importQueueStatusPending {
			continue
		}

		p.importQueue[index].Status = importQueueStatusUploading
		p.importQueue[index].Detail = "Streaming to hydrusd..."
		return index, p.importQueue[index].Path, true
	}

	p.importQueueRunning = false
	return -1, "", false
}

func (p *prototype) finishImportQueueEntry(index int, status importQueueStatus, fileID int64, detail string) {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()

	if index < 0 || index >= len(p.importQueue) {
		return
	}

	p.importQueue[index].Status = status
	p.importQueue[index].FileID = fileID
	p.importQueue[index].Detail = strings.TrimSpace(detail)
}

func (p *prototype) requeueImportQueueEntry(index int, detail string) {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()

	if index < 0 || index >= len(p.importQueue) {
		return
	}

	p.importQueue[index].Status = importQueueStatusPending
	p.importQueue[index].FileID = 0
	p.importQueue[index].Detail = strings.TrimSpace(detail)
}

func (p *prototype) clearImportQueue() {
	p.queueMu.Lock()
	if p.importQueueRunning {
		p.queueMu.Unlock()
		return
	}

	if len(p.importQueue) == 0 {
		p.queueMu.Unlock()
		return
	}

	p.importQueue = nil
	p.selectedQueueIndex = -1
	p.queueMu.Unlock()

	p.renderImportQueue()
	p.updateActionState()
	p.setStatus("Cleared the import queue.")
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
	p.loadRecentPage(0, selectFileID, successStatus, false)
}

func (p *prototype) loadRecentPage(offset int, selectFileID int64, successStatus string, appendResults bool) {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	if p.recentLoadBusy {
		return
	}

	if offset < 0 {
		offset = 0
	}
	p.recentLoadBusy = true

	currentSelection := p.selectedFileID
	if appendResults {
		p.setStatus("Loading more recent files from hydrusd...")
	} else {
		p.setStatus("Refreshing recent files from hydrusd...")
	}

	go func(connection connectionSnapshot, currentSelection int64, offset int, appendResults bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		page, err := connection.client.ListRecent(ctx, offset, p.recentLimit)
		if err != nil {
			fyne.Do(func() {
				p.recentLoadBusy = false
				if !p.isCurrentOperation(connection) {
					return
				}

				p.setStatus("Refresh failed.")
				dialog.ShowError(err, p.window)
			})
			return
		}

		fyne.Do(func() {
			p.recentLoadBusy = false
			if !p.isCurrentOperation(connection) {
				return
			}

			preferred := selectFileID
			if preferred == 0 {
				preferred = currentSelection
			}
			if appendResults {
				p.appendRecentPage(page, preferred)
			} else {
				p.applyRecentPage(page, preferred)
			}
			p.setStatus(successStatus)
		})
	}(connection, currentSelection, offset, appendResults)
}

func (p *prototype) applyRecentPage(page daemonclient.RecentPage, preferredFileID int64) {
	p.recentLimit = page.Limit
	if p.recentLimit <= 0 {
		p.recentLimit = recentPageLimit
	}
	p.recentHasMore = page.HasMore
	p.recentNextOffset = page.Offset + len(page.Items)
	p.applyRecentItems(page.Items, preferredFileID, true)
}

func (p *prototype) appendRecentPage(page daemonclient.RecentPage, preferredFileID int64) {
	if page.Limit > 0 {
		p.recentLimit = page.Limit
	}
	p.recentHasMore = page.HasMore
	p.recentNextOffset = page.Offset + len(page.Items)

	if len(page.Items) == 0 {
		p.updateActionState()
		return
	}

	combined := make([]daemonclient.RecentItem, 0, len(p.recent)+len(page.Items))
	combined = append(combined, p.recent...)
	seen := make(map[int64]struct{}, len(p.recent))
	for _, item := range p.recent {
		seen[item.FileID] = struct{}{}
	}
	for _, item := range page.Items {
		if _, ok := seen[item.FileID]; ok {
			continue
		}
		seen[item.FileID] = struct{}{}
		combined = append(combined, item)
	}

	p.recent = combined
	if preferredFileID > 0 && !p.hasRecentFile(preferredFileID) {
		preferredFileID = 0
	}
	p.selectedFileID = preferredFileID
	p.renderGrid()
	p.refreshSearchSuggestions()
	p.updateActionState()
}

func (p *prototype) applyRecentItems(items []daemonclient.RecentItem, preferredFileID int64, resetThumbnails bool) {
	if resetThumbnails {
		p.thumbnailCacheM.Lock()
		p.thumbnailGen++
		p.thumbnailCache = map[int64]fyne.Resource{}
		p.thumbnailLoads = map[int64]struct{}{}
		p.thumbnailCacheM.Unlock()

		p.tileMetadataMu.Lock()
		p.tileMetadataGen++
		p.tileMetadataCache = map[int64]daemonclient.FileMetadata{}
		p.tileMetadataLoads = map[int64]struct{}{}
		p.tileMetadataMu.Unlock()
	}

	p.recent = items
	if preferredFileID > 0 && !p.hasRecentFile(preferredFileID) {
		preferredFileID = 0
	}

	p.selectedFileID = preferredFileID
	p.renderGrid()
	p.refreshSearchSuggestions()
	p.updateActionState()

	if p.selectedFileID > 0 {
		p.metadataLabel.SetText("Loading selected-file metadata from hydrusd...")
		p.setRightTagsText("Loading tag metadata from hydrusd...")
		p.setLeftTagsText("Loading selected-file tags from hydrusd...")
		p.loadSelectedPreview(p.selectedFileID)
		p.loadSelectedMetadata(p.selectedFileID)
		return
	}

	p.metadataLabel.SetText(defaultMetadataText)
	p.setRightTagsText(defaultTagsText)
	p.setLeftTagsText(defaultTagsText)
	p.cancelPreviewRequest()
	p.clearSelectedPreview(defaultPreviewText)
}

func (p *prototype) renderGrid() {
	p.ensureGridWrap()

	if len(p.filteredRecentItems()) == 0 {
		p.gridHost.Objects = nil
		p.gridHost.Refresh()
		return
	}

	p.gridHost.Objects = []fyne.CanvasObject{
		p.gridWrap,
	}
	p.gridWrap.Refresh()
	p.gridHost.Refresh()
}

func (p *prototype) ensureGridWrap() {
	if p.gridWrap != nil {
		return
	}

	p.gridWrap = widget.NewGridWrap(
		func() int {
			return len(p.filteredRecentItems())
		},
		func() fyne.CanvasObject {
			return newMediaTile()
		},
		func(id widget.GridWrapItemID, item fyne.CanvasObject) {
			items := p.filteredRecentItems()
			if id < 0 || id >= len(items) {
				return
			}

			recentItem := items[id]
			tile := item.(*mediaTile)
			resource, overlay := p.lookupPreviewResource(recentItem.FileID)
			if resource == nil && overlay == "" {
				overlay = "Loading"
			}

			metadata, hasMetadata := p.lookupTileMetadata(recentItem.FileID)
			title, subtitle := formatRecentTileText(recentItem, metadata, hasMetadata)

			tile.SetData(
				title,
				subtitle,
				resource,
				overlay,
				recentItem.FileID == p.selectedFileID,
				nil,
				func() {
					p.selectFile(recentItem.FileID)
					p.openNativeWatcherForFile(recentItem.FileID)
				},
			)

			if resource == nil {
				p.ensurePreviewResource(recentItem)
			}

			if !hasMetadata {
				p.ensureTileMetadata(recentItem)
			}
		},
	)
	p.gridWrap.OnSelected = func(id widget.GridWrapItemID) {
		items := p.filteredRecentItems()
		if id < 0 || id >= len(items) {
			return
		}
		p.selectFile(items[id].FileID)
	}
}

func (p *prototype) monitorRecentGridScroll() {
	ticker := time.NewTicker(recentLoadTick)
	defer ticker.Stop()

	for range ticker.C {
		fyne.Do(func() {
			p.maybeLoadMoreRecent()
		})
	}
}

func (p *prototype) maybeLoadMoreRecent() {
	if p.gridWrap == nil || !p.recentHasMore || p.recentLoadBusy || len(p.recent) == 0 {
		return
	}

	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	columns := p.gridWrap.ColumnCount()
	if columns <= 0 {
		return
	}
	if p.gridWrap.Size().Height <= 0 || p.gridWrap.Size().Width <= 0 {
		return
	}

	padding := theme.Padding()
	rowHeight := float32(newMediaTile().MinSize().Height) + padding
	visibleBottom := p.gridWrap.GetScrollOffset() + p.gridWrap.Size().Height
	if visibleBottom <= 0 {
		return
	}
	lastVisibleRow := int(visibleBottom / rowHeight)
	lastVisibleIndex := ((lastVisibleRow + 1) * columns) - 1
	threshold := len(p.recent) - (columns * 2)
	if threshold < 0 {
		threshold = 0
	}

	if lastVisibleIndex >= threshold {
		p.loadRecentPage(p.recentNextOffset, p.selectedFileID, "Loaded more recent files from hydrusd.", true)
	}
}

func (p *prototype) selectFile(fileID int64) {
	if !p.currentConnection().connected {
		return
	}
	if p.selectedFileID == fileID {
		return
	}

	p.selectedFileID = fileID
	p.renderGrid()
	p.updateActionState()
	p.metadataLabel.SetText("Loading selected-file metadata from hydrusd...")
	p.setRightTagsText("Loading tag metadata from hydrusd...")
	p.setLeftTagsText("Loading selected-file tags from hydrusd...")
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

	if resource, ok := p.lookupSelectedPreview(item); ok {
		p.cancelPreviewRequest()
		p.setSelectedPreview(resource, "")
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
		p.storeSelectedPreview(item, resource)

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
				p.setRightTagsText("Could not load tag metadata from hydrusd.")
				p.setLeftTagsText("Could not load selected-file tags from hydrusd.")
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

			p.tileMetadataMu.Lock()
			p.tileMetadataCache[selectedFileID] = metadata
			p.tileMetadataMu.Unlock()

			p.metadataLabel.SetText(formatMetadataDetails(metadata))
			p.setRightTagsMetadata(metadata)
			p.setLeftTagsMetadata(metadata)
			p.renderGrid()
			p.refreshSearchSuggestions()
		})
	}(connection, fileID)
}

func (p *prototype) openNativeWatcherForFile(fileID int64) {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	item, ok := p.lookupRecentItem(fileID)
	if !ok {
		return
	}

	title := fmt.Sprintf("Watcher • file_id %d", item.FileID)
	if message := nativeWatcherFallbackMessage(item.MIME); message != "" {
		p.presentWatcherWindow(title, newWatcherMessageContent(item.MIME, message))
		return
	}

	p.presentWatcherWindow(title, newWatcherLoadingContent(item.MIME))
	ctx, cancel, requestID := p.beginWatcherRequest(30 * time.Second)

	go func(connection connectionSnapshot, item daemonclient.RecentItem, ctx context.Context, cancel context.CancelFunc, requestID uint64) {
		defer cancel()
		defer p.finishWatcherRequest(requestID)

		payload, err := connection.client.FetchFileContent(ctx, item, watcherByteLimit)
		if err != nil {
			if ctx.Err() != nil || !p.isCurrentWatcherRequest(requestID) {
				return
			}

			fyne.Do(func() {
				if !p.isCurrentOperation(connection) || !p.isCurrentWatcherRequest(requestID) {
					return
				}

				p.presentWatcherWindow(title, newWatcherMessageContent(item.MIME, "Could not load the original from hydrusd.\n\n"+err.Error()))
			})
			return
		}

		if len(payload) == 0 {
			fyne.Do(func() {
				if !p.isCurrentOperation(connection) || !p.isCurrentWatcherRequest(requestID) {
					return
				}

				p.presentWatcherWindow(title, newWatcherMessageContent(item.MIME, fmt.Sprintf("Daemon returned an empty original for file_id %d.", item.FileID)))
			})
			return
		}

		if err := validateNativeWatcherPayload(payload); err != nil {
			fyne.Do(func() {
				if !p.isCurrentOperation(connection) || !p.isCurrentWatcherRequest(requestID) {
					return
				}

				p.presentWatcherWindow(title, newWatcherMessageContent(item.MIME, "Viewer could not prepare the original for display.\n\n"+err.Error()))
			})
			return
		}

		resource := fyne.NewStaticResource(
			fmt.Sprintf("file-%d-watcher", item.FileID),
			payload,
		)

		fyne.Do(func() {
			if !p.isCurrentOperation(connection) || !p.isCurrentWatcherRequest(requestID) {
				return
			}

			p.presentWatcherWindow(title, newWatcherImageContent(item, resource))
		})
	}(connection, item, ctx, cancel, requestID)
}

func (p *prototype) presentWatcherWindow(title string, content fyne.CanvasObject) {
	if p.watcherWindow == nil {
		watcherWindow := p.app.NewWindow(title)
		watcherWindow.Resize(fyne.NewSize(980, 720))
		watcherWindow.SetPadded(false)
		watcherWindow.SetOnClosed(func() {
			p.cancelWatcherRequest()
			if p.watcherWindow == watcherWindow {
				p.watcherWindow = nil
			}
		})
		p.watcherWindow = watcherWindow
	}

	p.watcherWindow.SetTitle(title)
	p.watcherWindow.SetContent(content)
	p.watcherWindow.Show()
	p.watcherWindow.RequestFocus()
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
			p.refreshSearchSuggestions()
		})
	}(connection, item, generation)
}

func (p *prototype) nextSearchSuggestionRequestID() uint64 {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	p.searchRequestID++
	return p.searchRequestID
}

func (p *prototype) isCurrentSearchSuggestionRequest(requestID uint64) bool {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()

	return p.searchRequestID == requestID
}

func (p *prototype) renderSearchSuggestions(next []string, hint string) {
	p.searchSuggestions = append([]string(nil), next...)
	if len(p.searchSuggestions) == 0 {
		p.searchSuggestionsList.Hide()
	} else {
		p.searchSuggestionsList.Show()
	}

	p.searchHintLabel.SetText(strings.TrimSpace(hint))
	p.searchSuggestionsList.Refresh()
	p.searchHintLabel.Refresh()
}

func richTextSegmentsFromText(text string) []widget.RichTextSegment {
	return []widget.RichTextSegment{
		&widget.TextSegment{Text: strings.TrimSpace(text)},
	}
}

func (p *prototype) setLeftTagsText(text string) {
	p.leftTagsRichText.Segments = richTextSegmentsFromText(text)
	p.leftTagsRichText.Refresh()
}

func (p *prototype) setRightTagsText(text string) {
	p.tagsRichText.Segments = richTextSegmentsFromText(text)
	p.tagsRichText.Refresh()
}

func (p *prototype) setLeftTagsMetadata(metadata daemonclient.FileMetadata) {
	p.leftTagsRichText.Segments = formatTagMetadataSegments(metadata)
	p.leftTagsRichText.Refresh()
}

func (p *prototype) setRightTagsMetadata(metadata daemonclient.FileMetadata) {
	p.tagsRichText.Segments = formatTagMetadataSegments(metadata)
	p.tagsRichText.Refresh()
}

func (p *prototype) collectLoadedSearchSuggestions() []string {
	p.tileMetadataMu.Lock()
	metadataRows := make([]daemonclient.FileMetadata, 0, len(p.tileMetadataCache))
	for _, metadata := range p.tileMetadataCache {
		metadataRows = append(metadataRows, metadata)
	}
	p.tileMetadataMu.Unlock()

	seen := map[string]struct{}{}
	suggestions := []string{}
	for _, metadata := range metadataRows {
		for _, suggestion := range collectTagEditorSuggestions(metadata) {
			normalized := strings.TrimSpace(suggestion)
			if normalized == "" {
				continue
			}

			if _, ok := seen[normalized]; ok {
				continue
			}

			seen[normalized] = struct{}{}
			suggestions = append(suggestions, normalized)
		}
	}

	sort.Strings(suggestions)
	return suggestions
}

func searchSuggestionsHint(prefix string, connected bool, suggestions []string, remoteErr error) string {
	trimmedPrefix := strings.TrimSpace(prefix)
	if trimmedPrefix == "" {
		if connected {
			return "Type a tag prefix to load autocomplete suggestions from hydrusd."
		}

		return "Type a tag prefix to filter the loaded grid. Connect to hydrusd for daemon-backed autocomplete."
	}

	if len(suggestions) > 0 {
		if remoteErr != nil {
			return "Showing loaded-tag suggestions while hydrusd autocomplete is unavailable."
		}

		return "Click a suggestion to filter the loaded recent files."
	}

	if remoteErr != nil {
		return "No matching loaded tags. hydrusd autocomplete is temporarily unavailable."
	}

	if connected {
		return "No matching tags yet. Keep typing to narrow the hydrusd autocomplete results."
	}

	return "No matching loaded tags. Connect to hydrusd for daemon-backed autocomplete."
}

func (p *prototype) refreshSearchSuggestions() {
	prefix := strings.TrimSpace(p.searchEntry.Text)
	connection := p.currentConnection()
	localSuggestions := filterTagSuggestions(p.collectLoadedSearchSuggestions(), prefix)
	requestID := p.nextSearchSuggestionRequestID()

	if prefix == "" {
		p.renderSearchSuggestions(nil, searchSuggestionsHint(prefix, connection.connected, nil, nil))
		return
	}

	if !connection.connected || connection.client == nil {
		p.renderSearchSuggestions(localSuggestions, searchSuggestionsHint(prefix, false, localSuggestions, nil))
		return
	}

	p.renderSearchSuggestions(localSuggestions, searchSuggestionsHint(prefix, true, localSuggestions, nil))

	go func(connection connectionSnapshot, prefix string, localSuggestions []string, requestID uint64) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		remoteSuggestions, err := connection.client.SuggestTags(ctx, prefix, tagSuggestionLimit)
		fyne.Do(func() {
			if !p.isCurrentOperation(connection) || !p.isCurrentSearchSuggestionRequest(requestID) {
				return
			}

			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}

				p.renderSearchSuggestions(localSuggestions, searchSuggestionsHint(prefix, true, localSuggestions, err))
				return
			}

			merged := mergeTagSuggestions(localSuggestions, remoteSuggestions)
			p.renderSearchSuggestions(merged, searchSuggestionsHint(prefix, true, merged, nil))
		})
	}(connection, prefix, localSuggestions, requestID)
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

func (p *prototype) lookupTileMetadata(fileID int64) (daemonclient.FileMetadata, bool) {
	p.tileMetadataMu.Lock()
	defer p.tileMetadataMu.Unlock()

	metadata, ok := p.tileMetadataCache[fileID]
	return metadata, ok
}

func (p *prototype) clearTileMetadataLoad(fileID int64, generation uint64) {
	p.tileMetadataMu.Lock()
	defer p.tileMetadataMu.Unlock()

	if generation != p.tileMetadataGen {
		return
	}

	delete(p.tileMetadataLoads, fileID)
}

func (p *prototype) ensureTileMetadata(item daemonclient.RecentItem) {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	p.tileMetadataMu.Lock()
	generation := p.tileMetadataGen
	if _, ok := p.tileMetadataCache[item.FileID]; ok {
		p.tileMetadataMu.Unlock()
		return
	}

	if _, loading := p.tileMetadataLoads[item.FileID]; loading {
		p.tileMetadataMu.Unlock()
		return
	}

	p.tileMetadataLoads[item.FileID] = struct{}{}
	p.tileMetadataMu.Unlock()

	go func(connection connectionSnapshot, item daemonclient.RecentItem, generation uint64) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		metadata, err := connection.client.GetFileMetadata(ctx, item.FileID)
		if err != nil {
			p.clearTileMetadataLoad(item.FileID, generation)
			return
		}

		if !p.isCurrentOperation(connection) {
			p.clearTileMetadataLoad(item.FileID, generation)
			return
		}

		p.tileMetadataMu.Lock()
		if generation != p.tileMetadataGen {
			p.tileMetadataMu.Unlock()
			return
		}

		delete(p.tileMetadataLoads, item.FileID)
		p.tileMetadataCache[item.FileID] = metadata
		p.tileMetadataMu.Unlock()

		fyne.Do(func() {
			if !p.isCurrentOperation(connection) {
				return
			}

			p.renderGrid()
		})
	}(connection, item, generation)
}

func (p *prototype) updateActionState() {
	connection := p.currentConnection()
	connected := connection.connected
	p.stateMu.RLock()
	ptrStatus := p.ptrStatus
	ptrStatusLoaded := p.ptrStatusLoaded
	ptrStatusBusy := p.ptrStatusBusy
	p.stateMu.RUnlock()
	p.queueMu.Lock()
	queueRunning := p.importQueueRunning
	hasQueuedItems := len(p.importQueue) > 0
	hasFailedItems := false
	hasFinishedItems := false
	selectedQueueIndex := normalizeImportQueueSelection(p.selectedQueueIndex, len(p.importQueue))
	selectedQueueEntry := importQueueEntry{}
	hasSelectedQueueEntry := false
	if selectedQueueIndex >= 0 {
		selectedQueueEntry = p.importQueue[selectedQueueIndex]
		hasSelectedQueueEntry = true
	}
	for _, entry := range p.importQueue {
		if canRetryImportQueueEntry(entry) {
			hasFailedItems = true
		}
		if isFinishedImportQueueEntry(entry) {
			hasFinishedItems = true
		}
	}
	p.queueMu.Unlock()

	p.addButton.Enable()
	p.addFolderButton.Enable()

	if connected {
		p.refreshButton.Enable()
		if p.ptrRefreshButton != nil {
			p.ptrRefreshButton.Enable()
		}
	} else {
		p.refreshButton.Disable()
		if p.ptrRefreshButton != nil {
			p.ptrRefreshButton.Disable()
		}
	}

	if !queueRunning && hasQueuedItems {
		p.clearQueueButton.Enable()
	} else {
		p.clearQueueButton.Disable()
	}

	if hasFailedItems {
		p.retryFailedButton.Enable()
	} else {
		p.retryFailedButton.Disable()
	}

	if hasFinishedItems && !queueRunning {
		p.clearFinishedButton.Enable()
	} else {
		p.clearFinishedButton.Disable()
	}

	if hasSelectedQueueEntry && canRetryImportQueueEntry(selectedQueueEntry) {
		p.retrySelectedButton.Enable()
	} else {
		p.retrySelectedButton.Disable()
	}

	if hasSelectedQueueEntry && !queueRunning && canRemoveImportQueueEntry(selectedQueueEntry) {
		p.removeSelectedButton.Enable()
	} else {
		p.removeSelectedButton.Disable()
	}

	if connected && p.selectedFileID > 0 {
		p.trashButton.Enable()
		p.editTagsButton.Enable()
	} else {
		p.trashButton.Disable()
		p.editTagsButton.Disable()
	}

	if connected && !ptrStatusBusy && ptrStatusLoaded && ptrStatus.Enabled && ptrStatus.Phase != coreptrsync.PhaseSyncing && ptrStatus.Phase != coreptrsync.PhaseUnavailable {
		p.ptrSyncButton.Enable()
	} else {
		p.ptrSyncButton.Disable()
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
	if resource != nil {
		p.previewImage.Image = nil
		p.previewImage.Resource = resource
		p.previewImage.Show()
	} else {
		p.previewImage.Resource = nil
		p.previewImage.Hide()
	}
	p.previewImage.Refresh()
	p.previewLabel.SetText(message)
}

func (p *prototype) lookupSelectedPreview(item daemonclient.RecentItem) (fyne.Resource, bool) {
	cacheKey := selectedPreviewCacheKey(item)
	if cacheKey == "" {
		return nil, false
	}

	p.selectedPreviewCacheM.Lock()
	defer p.selectedPreviewCacheM.Unlock()

	resource, ok := p.selectedPreviewCache[cacheKey]
	return resource, ok && resource != nil
}

func (p *prototype) storeSelectedPreview(item daemonclient.RecentItem, resource fyne.Resource) {
	cacheKey := selectedPreviewCacheKey(item)
	if cacheKey == "" || resource == nil {
		return
	}

	p.selectedPreviewCacheM.Lock()
	defer p.selectedPreviewCacheM.Unlock()

	p.selectedPreviewCache[cacheKey] = resource
}

func selectedPreviewCacheKey(item daemonclient.RecentItem) string {
	if normalizedHash := strings.TrimSpace(strings.ToLower(item.Hash)); normalizedHash != "" {
		return normalizedHash
	}

	if item.FileID <= 0 {
		return ""
	}

	return fmt.Sprintf("file-id:%d", item.FileID)
}

func newWatcherLoadingContent(mime string) fyne.CanvasObject {
	message := "Loading original from hydrusd..."
	if strings.TrimSpace(mime) != "" {
		message = fmt.Sprintf("Loading %s from hydrusd...", mime)
	}

	return newWatcherMessageContent(mime, message)
}

func newWatcherImageContent(item daemonclient.RecentItem, resource fyne.Resource) fyne.CanvasObject {
	imageViewer := canvas.NewImageFromResource(resource)
	imageViewer.FillMode = canvas.ImageFillOriginal
	imageViewer.ScaleMode = canvas.ImageScaleSmooth

	headline := widget.NewLabelWithStyle(
		fmt.Sprintf("file_id %d • %s", item.FileID, item.MIME),
		fyne.TextAlignLeading,
		fyne.TextStyle{Bold: true},
	)
	headline.Wrapping = fyne.TextTruncate

	footerText := "Original-size image viewer. Resize the window or scroll to inspect large media."
	if item.Width != nil && item.Height != nil {
		footerText = fmt.Sprintf("Original-size image viewer • %dx%d • scroll to inspect large media.", *item.Width, *item.Height)
	}
	footer := widget.NewLabel(footerText)
	footer.Wrapping = fyne.TextWrapWord

	background := canvas.NewRectangle(color.NRGBA{R: 18, G: 18, B: 20, A: 255})
	scroll := container.NewScroll(imageViewer)
	scroll.SetMinSize(fyne.NewSize(640, 480))

	content := container.NewBorder(
		container.NewPadded(container.NewVBox(headline, widget.NewSeparator())),
		container.NewPadded(footer),
		nil,
		nil,
		container.NewPadded(scroll),
	)

	return container.NewStack(background, content)
}

func newWatcherMessageContent(mime string, message string) fyne.CanvasObject {
	headlineText := "Native watcher"
	if normalized := strings.TrimSpace(mime); normalized != "" {
		headlineText = fmt.Sprintf("Native watcher • %s", normalized)
	}

	headline := widget.NewLabelWithStyle(
		headlineText,
		fyne.TextAlignLeading,
		fyne.TextStyle{Bold: true},
	)
	headline.Wrapping = fyne.TextTruncate

	body := widget.NewLabel(message)
	body.Wrapping = fyne.TextWrapWord
	body.Alignment = fyne.TextAlignCenter

	background := canvas.NewRectangle(color.NRGBA{R: 18, G: 18, B: 20, A: 255})
	content := container.NewBorder(
		container.NewPadded(container.NewVBox(headline, widget.NewSeparator())),
		nil,
		nil,
		nil,
		container.NewCenter(container.NewPadded(body)),
	)

	return container.NewStack(background, content)
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

func nativeWatcherFallbackMessage(mime string) string {
	normalized := strings.TrimSpace(strings.ToLower(mime))
	if supportsSelectedPreviewMime(normalized) {
		return ""
	}

	if strings.HasPrefix(normalized, "video/") {
		return "Native video playback is not yet bundled in this prototype.\n\nThis build keeps viewing inside the app, but core Fyne does not provide a built-in in-app video player."
	}

	return fmt.Sprintf("Viewer not available for %s.", mime)
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

func validateNativeWatcherPayload(payload []byte) error {
	config, _, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("decode image config: %w", err)
	}

	if config.Width <= 0 || config.Height <= 0 {
		return fmt.Errorf("decoded image dimensions were invalid")
	}

	if config.Width > watcherMaxDimension || config.Height > watcherMaxDimension {
		return fmt.Errorf(
			"decoded image dimensions %dx%d exceed the watcher limit of %dx%d",
			config.Width,
			config.Height,
			watcherMaxDimension,
			watcherMaxDimension,
		)
	}

	pixels := int64(config.Width) * int64(config.Height)
	if pixels > watcherPixelLimit {
		return fmt.Errorf(
			"decoded image dimensions %dx%d exceed the watcher limit of %d pixels",
			config.Width,
			config.Height,
			watcherPixelLimit,
		)
	}

	return nil
}

func formatMetadataDetails(metadata daemonclient.FileMetadata) string {
	dimensions := "unknown"
	if metadata.Width != nil && metadata.Height != nil {
		dimensions = fmt.Sprintf("%dx%d", *metadata.Width, *metadata.Height)
	}

	lines := []string{
		fmt.Sprintf("file_id: %d", metadata.FileID),
		fmt.Sprintf("hash: %s", metadata.Hash),
		fmt.Sprintf("mime: %s", metadata.MIME),
		fmt.Sprintf("size: %d bytes", metadata.Size),
		fmt.Sprintf("dimensions: %s", dimensions),
		fmt.Sprintf("local: %t", metadata.IsLocal),
		fmt.Sprintf("trashed: %t", metadata.IsTrashed),
		fmt.Sprintf("deleted: %t", metadata.IsDeleted),
	}

	return strings.Join(lines, "\n")
}

func formatTagMetadata(metadata daemonclient.FileMetadata) string {
	tagLines := formatMetadataTags(metadata.Tags)
	if len(tagLines) == 0 {
		return "No tag metadata is available for the selected file."
	}

	return strings.Join(tagLines, "\n")
}

func formatMetadata(metadata daemonclient.FileMetadata) string {
	lines := []string{formatMetadataDetails(metadata)}

	tagLines := formatMetadataTags(metadata.Tags)
	if len(tagLines) > 0 {
		lines = append(lines, "", "tags:")
		lines = append(lines, tagLines...)
	}

	return strings.Join(lines, "\n")
}

func formatTagMetadataSegments(metadata daemonclient.FileMetadata) []widget.RichTextSegment {
	serviceKeys := visibleMetadataTagServiceKeys(metadata.Tags)
	if len(serviceKeys) == 0 {
		return richTextSegmentsFromText("No tag metadata is available for the selected file.")
	}

	segments := make([]widget.RichTextSegment, 0, len(serviceKeys)*2)
	for index, serviceKey := range serviceKeys {
		service := metadata.Tags[serviceKey]
		if index > 0 {
			segments = append(segments, &widget.TextSegment{Text: "\n"})
		}

		segments = append(
			segments,
			&widget.TextSegment{
				Text:  metadataTagServiceHeading(serviceKey, service),
				Style: widget.RichTextStyleStrong,
			},
			&widget.TextSegment{Text: "\n"},
		)

		tagsByStatus := metadataTagPreferredTagsByStatus(service)
		for _, statusKey := range metadataTagStatusKeys(tagsByStatus) {
			serviceTags := tagsByStatus[statusKey]
			if len(serviceTags) == 0 {
				continue
			}

			segments = append(
				segments,
				&widget.TextSegment{
					Text:  metadataTagStatusLabel(statusKey) + ": ",
					Style: widget.RichTextStyleInline,
				},
			)

			for tagIndex, tag := range serviceTags {
				if tagIndex > 0 {
					segments = append(
						segments,
						&widget.TextSegment{
							Text:  ", ",
							Style: widget.RichTextStyleInline,
						},
					)
				}

				segments = append(
					segments,
					&widget.TextSegment{
						Text: tag,
						Style: widget.RichTextStyle{
							Inline:    true,
							ColorName: metadataTagColorName(tag),
						},
					},
				)
			}

			segments = append(
				segments,
				&widget.TextSegment{
					Text:  "\n",
					Style: widget.RichTextStyleInline,
				},
			)
		}
	}

	return segments
}

func formatMetadataTags(tags map[string]daemonclient.FileMetadataTagService) []string {
	serviceKeys := visibleMetadataTagServiceKeys(tags)
	if len(serviceKeys) == 0 {
		return nil
	}

	lines := []string{}
	for _, serviceKey := range serviceKeys {
		lines = append(lines, formatMetadataTagService(serviceKey, tags[serviceKey])...)
	}

	return lines
}

func visibleMetadataTagServiceKeys(tags map[string]daemonclient.FileMetadataTagService) []string {
	if len(tags) == 0 {
		return nil
	}

	serviceKeys := []string{}
	for serviceKey, service := range tags {
		if !metadataTagServiceHasVisibleTags(service) {
			continue
		}

		serviceKeys = append(serviceKeys, serviceKey)
	}

	sort.Slice(serviceKeys, func(i, j int) bool {
		leftLabel := metadataTagServiceHeading(serviceKeys[i], tags[serviceKeys[i]])
		rightLabel := metadataTagServiceHeading(serviceKeys[j], tags[serviceKeys[j]])
		if leftLabel == rightLabel {
			return serviceKeys[i] < serviceKeys[j]
		}

		return leftLabel < rightLabel
	})

	return serviceKeys
}

func formatMetadataTagService(
	serviceKey string,
	service daemonclient.FileMetadataTagService,
) []string {
	tagsByStatus := metadataTagPreferredTagsByStatus(service)

	statusKeys := metadataTagStatusKeys(tagsByStatus)
	if len(statusKeys) == 0 {
		return nil
	}

	lines := []string{fmt.Sprintf("- %s", metadataTagServiceHeading(serviceKey, service))}
	for _, statusKey := range statusKeys {
		serviceTags := tagsByStatus[statusKey]
		if len(serviceTags) == 0 {
			continue
		}

		lines = append(
			lines,
			fmt.Sprintf(
				"  %s: %s",
				metadataTagStatusLabel(statusKey),
				strings.Join(serviceTags, ", "),
			),
		)
	}

	return lines
}

func metadataTagServiceHasVisibleTags(service daemonclient.FileMetadataTagService) bool {
	return len(metadataTagStatusKeys(metadataTagPreferredTagsByStatus(service))) > 0
}

func metadataTagPreferredTagsByStatus(
	service daemonclient.FileMetadataTagService,
) map[string][]string {
	preferred := map[string][]string{}
	for statusKey, tags := range service.StorageTags {
		if len(tags) == 0 {
			continue
		}

		preferred[statusKey] = tags
	}

	for statusKey, tags := range service.DisplayTags {
		if len(tags) == 0 {
			continue
		}

		preferred[statusKey] = tags
	}

	return preferred
}

func metadataTagServiceHeading(
	serviceKey string,
	service daemonclient.FileMetadataTagService,
) string {
	name := strings.TrimSpace(service.Name)
	if name == "" {
		name = serviceKey
	}

	typePretty := strings.TrimSpace(service.TypePretty)
	if typePretty == "" {
		return name
	}

	return name + " (" + typePretty + ")"
}

func metadataTagStatusKeys(tagsByStatus map[string][]string) []string {
	statusKeys := []string{}
	for statusKey, tags := range tagsByStatus {
		if len(tags) == 0 {
			continue
		}

		statusKeys = append(statusKeys, statusKey)
	}

	sort.Slice(statusKeys, func(i, j int) bool {
		leftRank := metadataTagStatusRank(statusKeys[i])
		rightRank := metadataTagStatusRank(statusKeys[j])
		if leftRank == rightRank {
			return statusKeys[i] < statusKeys[j]
		}

		return leftRank < rightRank
	})

	return statusKeys
}

func metadataTagStatusRank(statusKey string) int {
	switch statusKey {
	case "0":
		return 0
	case "1":
		return 1
	case "2":
		return 2
	case "3":
		return 3
	default:
		return 100
	}
}

func metadataTagColorName(tag string) fyne.ThemeColorName {
	namespace, _ := splitGalleryTag(tag)
	switch namespace {
	case "":
		if strings.Contains(strings.TrimSpace(tag), ":") {
			return hydrusTagColorNamespacedFallback
		}

		return hydrusTagColorUnnamespaced
	case "creator":
		return hydrusTagColorCreator
	case "series":
		return hydrusTagColorSeries
	case "character":
		return hydrusTagColorCharacter
	default:
		return hydrusTagColorNamespacedFallback
	}
}

func (p *prototype) fetchPTRStatus() {
	connection := p.currentConnection()
	if !connection.connected {
		p.cancelPTRStatusRequests()
		p.stateMu.Lock()
		p.ptrStatusLoaded = false
		p.stateMu.Unlock()
		p.setPTRVisualState("PTR sync: offline", false)
		p.ptrStatusLabel.SetText("PTR sync status: offline")
		p.updateActionState()
		return
	}

	requestID := p.beginPTRStatusRequest()
	p.setPTRVisualState("PTR sync: checking status…", false)
	p.ptrStatusLabel.SetText("Fetching PTR status...")
	p.setStatus("Fetching PTR sync status from hydrusd...")
	p.updateActionState()

	go func(connection connectionSnapshot, requestID uint64) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		status, err := connection.client.GetPTRStatus(ctx)

		fyne.Do(func() {
			if !p.isCurrentOperation(connection) || !p.finishPTRStatusRequest(requestID) {
				return
			}

			if err != nil {
				p.setPTRVisualState("PTR sync: status fetch failed", false)
				p.ptrStatusLabel.SetText(fmt.Sprintf("Status fetch failed: %v", err))
				p.setStatus("PTR status fetch failed.")
				p.updateActionState()
				return
			}

			p.stateMu.Lock()
			p.ptrStatus = status.PTR
			p.ptrStatusLoaded = true
			p.stateMu.Unlock()
			p.renderPTRStatus(status.PTR)
			p.setStatus("PTR status refreshed from hydrusd.")
			if shouldPollPTRStatus(status.PTR) {
				p.pollPTRStatusUntilSettled(connection, requestID)
			}
		})
	}(connection, requestID)
}

func (p *prototype) triggerPTRSync() {
	connection := p.currentConnection()
	if !connection.connected {
		return
	}

	requestID := p.beginPTRStatusRequest()
	p.setPTRVisualState("PTR sync: requesting manual run…", true)
	p.ptrStatusLabel.SetText("Triggering manual sync...")
	p.setStatus("Requesting PTR sync from hydrusd...")
	p.updateActionState()

	go func(connection connectionSnapshot, requestID uint64) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		status, err := connection.client.TriggerPTRSync(ctx)

		fyne.Do(func() {
			if !p.isCurrentOperation(connection) || !p.finishPTRStatusRequest(requestID) {
				return
			}

			if err != nil {
				p.setPTRVisualState("PTR sync: request failed", false)
				p.ptrStatusLabel.SetText(fmt.Sprintf("Sync trigger failed: %v", err))
				p.setStatus("PTR sync trigger failed.")
				p.updateActionState()
				p.fetchPTRStatus()
				return
			}

			p.stateMu.Lock()
			p.ptrStatus = status.PTR
			p.ptrStatusLoaded = true
			p.stateMu.Unlock()
			p.renderPTRStatus(status.PTR)
			if shouldPollPTRStatus(status.PTR) {
				p.setStatus("PTR sync request accepted by hydrusd.")
				p.pollPTRStatusUntilSettled(connection, requestID)
			} else {
				p.setStatus(ptrCompletionStatusText(status.PTR))
			}
		})
	}(connection, requestID)
}

func (p *prototype) triggerDBIntegrityCheck() {
	connection := p.currentConnection()
	if !connection.connected {
		return
	}

	p.setStatus("Requesting database integrity check from hydrusd...")

	go func(connection connectionSnapshot) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		response, err := connection.client.TriggerDBIntegrityCheck(ctx)

		fyne.Do(func() {
			if !p.isCurrentOperation(connection) {
				return
			}

			if err != nil {
				p.setStatus("Database integrity check failed.")
				dialog.ShowError(err, p.window)
				return
			}

			message := strings.Join(response.Integrity.Results, "\n")
			if strings.TrimSpace(message) == "" {
				message = "No SQLite integrity-check output was returned by hydrusd."
			}

			if response.Integrity.Passed {
				p.setStatus("Database integrity check passed.")
			} else {
				p.setStatus("Database integrity check reported issues.")
			}

			dialog.ShowInformation("Database Integrity Check", message, p.window)
		})
	}(connection)
}

func (p *prototype) pollPTRStatusUntilSettled(connection connectionSnapshot, requestID uint64) {
	go func(connection connectionSnapshot, requestID uint64) {
		ticker := time.NewTicker(ptrPollTick)
		defer ticker.Stop()
		consecutiveErrors := 0

		for {
			<-ticker.C

			if !p.isCurrentOperation(connection) || !p.isCurrentPTRStatusRequest(requestID) {
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			status, err := connection.client.GetPTRStatus(ctx)
			cancel()
			if err != nil {
				consecutiveErrors++
				shouldContinue := shouldContinuePTRPollingAfterError(consecutiveErrors)
				fyne.Do(func() {
					if !p.isCurrentOperation(connection) || !p.isCurrentPTRStatusRequest(requestID) {
						return
					}

					if shouldContinue {
						p.ptrStatusLabel.SetText(ptrPollingErrorStatusText(err, consecutiveErrors))
						p.setStatus("PTR status refresh hit a transient error. Retrying...")
						return
					}

					p.setPTRVisualState("PTR sync: status poll failed", false)
					p.ptrStatusLabel.SetText(fmt.Sprintf("Polling stopped after repeated status errors: %v", err))
					p.setStatus("PTR status polling stopped after repeated errors.")
					p.updateActionState()
				})
				if shouldContinue {
					continue
				}

				return
			}
			consecutiveErrors = 0

			stillPolling := shouldPollPTRStatus(status.PTR)
			fyne.Do(func() {
				if !p.isCurrentOperation(connection) || !p.isCurrentPTRStatusRequest(requestID) {
					return
				}

				p.stateMu.Lock()
				p.ptrStatus = status.PTR
				p.ptrStatusLoaded = true
				p.stateMu.Unlock()
				p.renderPTRStatus(status.PTR)
				if status.PTR.IsRunning || status.PTR.Phase == coreptrsync.PhaseSyncing {
					p.setStatus("PTR sync is running in hydrusd...")
				} else if status.PTR.Phase == coreptrsync.PhaseRetrying {
					p.setStatus(ptrCompletionStatusText(status.PTR))
				} else {
					p.setStatus(ptrCompletionStatusText(status.PTR))
				}
			})

			if !stillPolling {
				return
			}
		}
	}(connection, requestID)
}

func (p *prototype) renderPTRStatus(status coreptrsync.Status) {
	p.setPTRVisualState(ptrHeadlineText(status), status.IsRunning || status.Phase == coreptrsync.PhaseSyncing)
	p.ptrStatusLabel.SetText(formatPTRStatus(status))
	p.updateActionState()
}

func (p *prototype) filteredRecentItems() []daemonclient.RecentItem {
	query := strings.TrimSpace(strings.ToLower(p.galleryFilterQuery))
	filtered := make([]daemonclient.RecentItem, 0, len(p.recent))
	needsMetadata := query != "" || gallerySortRequiresMetadata(p.gallerySortMode)
	for _, item := range p.recent {
		metadata, hasMetadata := p.lookupTileMetadata(item.FileID)
		if query == "" || recentItemMatchesQuery(query, item, metadata, hasMetadata) {
			filtered = append(filtered, item)
		}

		if needsMetadata && !hasMetadata {
			p.ensureTileMetadata(item)
		}
	}

	sortRecentItemsForDisplay(filtered, p.gallerySortMode, p.lookupTileMetadata)

	return filtered
}

func (p *prototype) setPTRVisualState(headline string, running bool) {
	p.ptrHeadlineLabel.SetText(headline)
	if running {
		p.ptrProgressBar.Show()
		p.ptrProgressBar.Start()
		return
	}

	p.ptrProgressBar.Stop()
	p.ptrProgressBar.Hide()
}

func ptrHeadlineText(status coreptrsync.Status) string {
	switch {
	case status.IsRunning || status.Phase == coreptrsync.PhaseSyncing:
		return "PTR sync: running"
	case status.Phase == coreptrsync.PhaseRetrying:
		return "PTR sync: retrying"
	case status.LastError != "":
		return "PTR sync: last run failed"
	case status.UnavailableReason != "":
		return "PTR sync: unavailable"
	case !status.Enabled:
		return "PTR sync: disabled"
	case status.IsComplete:
		return "PTR sync: ✓ complete"
	default:
		return "PTR sync: idle"
	}
}

func shouldContinuePTRPollingAfterError(consecutiveErrors int) bool {
	return consecutiveErrors < ptrPollErrorLimit
}

func ptrPollingErrorStatusText(err error, consecutiveErrors int) string {
	if err == nil {
		return "PTR status refresh hit a transient error."
	}

	return fmt.Sprintf(
		"PTR status refresh hit a transient error (%d/%d): %v",
		consecutiveErrors,
		ptrPollErrorLimit,
		err,
	)
}

func ptrCompletionStatusText(status coreptrsync.Status) string {
	if status.Phase == coreptrsync.PhaseRetrying {
		if countdown := ptrThrottleCountdown(status); countdown != "" {
			return fmt.Sprintf("PTR server is busy. Retrying in %s.", countdown)
		}

		return "PTR server is busy."
	}

	if status.LastError != "" {
		return "PTR sync finished with an error in hydrusd."
	}

	if !status.IsComplete {
		return "PTR sync is idle in hydrusd."
	}

	return fmt.Sprintf(
		"PTR sync completed in hydrusd. Definitions %d • content %d • update files %d.",
		status.ProcessedDefinitionCount,
		status.ProcessedContentCount,
		status.DownloadedUpdateCount,
	)
}

func formatPTRStatus(status coreptrsync.Status) string {
	var buf strings.Builder
	if strings.TrimSpace(status.ServiceName) != "" {
		buf.WriteString(fmt.Sprintf("Service: %s\n", status.ServiceName))
	}
	if strings.TrimSpace(status.Host) != "" && status.Port > 0 {
		buf.WriteString(fmt.Sprintf("Endpoint: %s:%d\n", status.Host, status.Port))
	}
	if strings.TrimSpace(status.AccountMode) != "" {
		buf.WriteString(fmt.Sprintf("Account: %s\n", status.AccountMode))
	}
	buf.WriteString(fmt.Sprintf("Phase: %s\n", status.Phase))
	if status.IsRunning {
		buf.WriteString("Status: Sync is currently running\n")
	} else if status.Phase == coreptrsync.PhaseRetrying {
		if countdown := ptrThrottleCountdown(status); countdown != "" {
			buf.WriteString(fmt.Sprintf("Status: Remote PTR busy; retrying in %s\n", countdown))
		} else {
			buf.WriteString("Status: Remote PTR busy; retrying\n")
		}
	} else if status.IsComplete {
		buf.WriteString("Status: Complete\n")
	} else {
		buf.WriteString("Status: Idle\n")
	}
	buf.WriteString(fmt.Sprintf("Metadata Slice: %d\n", status.MetadataSlice))

	if status.LastError != "" {
		buf.WriteString(fmt.Sprintf("Last error: %s\n", status.LastError))
	} else if status.UnavailableReason != "" {
		buf.WriteString(fmt.Sprintf("Unavailable: %s\n", status.UnavailableReason))
	} else if !status.Enabled {
		buf.WriteString("PTR sync is disabled in daemon.\n")
	}

	buf.WriteString(fmt.Sprintf("Processed Definitions: %d\n", status.ProcessedDefinitionCount))
	buf.WriteString(fmt.Sprintf("Processed Content: %d\n", status.ProcessedContentCount))
	buf.WriteString(fmt.Sprintf("Downloaded Update Files: %d", status.DownloadedUpdateCount))

	return buf.String()
}

func shouldPollPTRStatus(status coreptrsync.Status) bool {
	if status.IsRunning || status.Phase == coreptrsync.PhaseSyncing {
		return true
	}

	return status.Phase == coreptrsync.PhaseRetrying && status.RetryAtMS > time.Now().UTC().UnixMilli()
}

func ptrThrottleCountdown(status coreptrsync.Status) string {
	if status.RetryAtMS <= 0 {
		return ""
	}

	remaining := time.Until(time.UnixMilli(status.RetryAtMS))
	if remaining <= 0 {
		return "0s"
	}

	seconds := int64((remaining + time.Second - 1) / time.Second)
	if seconds >= 60 {
		minutes := (seconds + 59) / 60
		return fmt.Sprintf("%dm", minutes)
	}

	return fmt.Sprintf("%ds", seconds)
}

func metadataTagStatusLabel(statusKey string) string {
	switch statusKey {
	case "0":
		return "current"
	case "1":
		return "pending"
	case "2":
		return "deleted"
	case "3":
		return "petitioned"
	default:
		return "status " + statusKey
	}
}

func formatRecentTileText(
	item daemonclient.RecentItem,
	metadata daemonclient.FileMetadata,
	hasMetadata bool,
) (string, string) {
	title := shortRecentTileHash(item.Hash)
	if hasMetadata {
		title = galleryLabelForMetadata(metadata, title)
	}

	subtitleParts := []string{}
	if hasMetadata {
		if seriesLabel := gallerySeriesLabel(metadata); seriesLabel != "" && seriesLabel != title {
			subtitleParts = append(subtitleParts, seriesLabel)
		}
	}

	mimeLabel := item.MIME
	if item.Width != nil && item.Height != nil {
		mimeLabel = fmt.Sprintf("%s • %dx%d", item.MIME, *item.Width, *item.Height)
	}
	if strings.TrimSpace(mimeLabel) != "" {
		subtitleParts = append(subtitleParts, mimeLabel)
	}

	return title, strings.Join(subtitleParts, " • ")
}

func recentItemMatchesQuery(
	query string,
	item daemonclient.RecentItem,
	metadata daemonclient.FileMetadata,
	hasMetadata bool,
) bool {
	if query == "" {
		return true
	}

	for _, term := range strings.Fields(query) {
		if !recentItemMatchesTerm(term, item, metadata, hasMetadata) {
			return false
		}
	}

	return true
}

func recentItemMatchesTerm(
	term string,
	item daemonclient.RecentItem,
	metadata daemonclient.FileMetadata,
	hasMetadata bool,
) bool {
	normalizedTerm := strings.TrimSpace(strings.ToLower(term))
	if normalizedTerm == "" {
		return true
	}

	if strings.HasPrefix(normalizedTerm, "system:") {
		return recentItemMatchesSystemPredicate(strings.TrimSpace(normalizedTerm[len("system:"):]), metadata, hasMetadata)
	}

	return recentItemMatchesFreeTextTerm(normalizedTerm, item, metadata, hasMetadata)
}

func recentItemMatchesFreeTextTerm(
	term string,
	item daemonclient.RecentItem,
	metadata daemonclient.FileMetadata,
	hasMetadata bool,
) bool {
	if term == "" {
		return true
	}

	for _, candidate := range []string{item.Hash, item.MIME, shortRecentTileHash(item.Hash)} {
		if strings.Contains(strings.ToLower(strings.TrimSpace(candidate)), term) {
			return true
		}
	}

	if !hasMetadata {
		return false
	}

	title, subtitle := formatRecentTileText(item, metadata, true)
	for _, candidate := range []string{title, subtitle, metadata.Hash, metadata.MIME} {
		if strings.Contains(strings.ToLower(strings.TrimSpace(candidate)), term) {
			return true
		}
	}

	for _, tag := range orderedGalleryTags(metadata) {
		if strings.Contains(strings.ToLower(strings.TrimSpace(tag)), term) {
			return true
		}

		_, subtag := splitGalleryTag(tag)
		if strings.Contains(strings.ToLower(strings.TrimSpace(subtag)), term) {
			return true
		}
	}

	return false
}

func recentItemMatchesSystemPredicate(predicate string, metadata daemonclient.FileMetadata, hasMetadata bool) bool {
	if !hasMetadata {
		return false
	}

	predicate = strings.TrimSpace(strings.ToLower(predicate))
	if predicate == "" {
		return false
	}

	switch {
	case predicate == "local":
		return metadata.IsLocal
	case predicate == "trashed":
		return metadata.IsTrashed
	case predicate == "deleted":
		return metadata.IsDeleted
	case predicate == "favorite", predicate == "favourite":
		return metadataHasFavorite(metadata)
	case strings.HasPrefix(predicate, "local="):
		return compareBoolPredicate(metadata.IsLocal, predicate[len("local="):])
	case strings.HasPrefix(predicate, "trashed="):
		return compareBoolPredicate(metadata.IsTrashed, predicate[len("trashed="):])
	case strings.HasPrefix(predicate, "deleted="):
		return compareBoolPredicate(metadata.IsDeleted, predicate[len("deleted="):])
	case strings.HasPrefix(predicate, "favorite="):
		return compareBoolPredicate(metadataHasFavorite(metadata), predicate[len("favorite="):])
	case strings.HasPrefix(predicate, "favourite="):
		return compareBoolPredicate(metadataHasFavorite(metadata), predicate[len("favourite="):])
	case strings.HasPrefix(predicate, "size"):
		operator, value, ok := parseIntPredicate(predicate, "size")
		return ok && compareInt64Predicate(metadata.Size, operator, value)
	case strings.HasPrefix(predicate, "width"):
		if metadata.Width == nil {
			return false
		}

		operator, value, ok := parseIntPredicate(predicate, "width")
		return ok && compareInt64Predicate(*metadata.Width, operator, value)
	case strings.HasPrefix(predicate, "height"):
		if metadata.Height == nil {
			return false
		}

		operator, value, ok := parseIntPredicate(predicate, "height")
		return ok && compareInt64Predicate(*metadata.Height, operator, value)
	case strings.HasPrefix(predicate, "resolution"):
		if metadata.Width == nil || metadata.Height == nil {
			return false
		}

		operator, width, height, ok := parseResolutionPredicate(predicate)
		return ok && compareResolutionPredicate(*metadata.Width, *metadata.Height, operator, width, height)
	default:
		return false
	}
}

func metadataHasFavorite(metadata daemonclient.FileMetadata) bool {
	for _, value := range metadata.Ratings {
		rating, ok := value.(bool)
		if ok && rating {
			return true
		}
	}

	return false
}

func compareBoolPredicate(actual bool, value string) bool {
	want, ok := parseBoolPredicateValue(value)
	return ok && actual == want
}

func parseBoolPredicateValue(value string) (bool, bool) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "true", "1", "yes":
		return true, true
	case "false", "0", "no":
		return false, true
	default:
		return false, false
	}
}

func parseIntPredicate(predicate string, field string) (string, int64, bool) {
	operator, valueText, ok := parsePredicateOperatorValue(predicate[len(field):])
	if !ok {
		return "", 0, false
	}

	value, err := strconv.ParseInt(valueText, 10, 64)
	if err != nil {
		return "", 0, false
	}

	return operator, value, true
}

func parseResolutionPredicate(predicate string) (string, int64, int64, bool) {
	operator, valueText, ok := parsePredicateOperatorValue(predicate[len("resolution"):])
	if !ok {
		return "", 0, 0, false
	}

	parts := strings.SplitN(strings.TrimSpace(valueText), "x", 2)
	if len(parts) != 2 {
		return "", 0, 0, false
	}

	width, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return "", 0, 0, false
	}

	height, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return "", 0, 0, false
	}

	return operator, width, height, true
}

func parsePredicateOperatorValue(expression string) (string, string, bool) {
	trimmed := strings.TrimSpace(expression)
	for _, operator := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(trimmed, operator) {
			value := strings.TrimSpace(trimmed[len(operator):])
			if value == "" {
				return "", "", false
			}

			return operator, value, true
		}
	}

	return "", "", false
}

func compareInt64Predicate(actual int64, operator string, want int64) bool {
	switch operator {
	case ">=":
		return actual >= want
	case "<=":
		return actual <= want
	case ">":
		return actual > want
	case "<":
		return actual < want
	case "=":
		return actual == want
	default:
		return false
	}
}

func compareResolutionPredicate(actualWidth int64, actualHeight int64, operator string, wantWidth int64, wantHeight int64) bool {
	switch operator {
	case ">=":
		return actualWidth >= wantWidth && actualHeight >= wantHeight
	case "<=":
		return actualWidth <= wantWidth && actualHeight <= wantHeight
	case ">":
		return actualWidth > wantWidth && actualHeight > wantHeight
	case "<":
		return actualWidth < wantWidth && actualHeight < wantHeight
	case "=":
		return actualWidth == wantWidth && actualHeight == wantHeight
	default:
		return false
	}
}

func gallerySortRequiresMetadata(mode string) bool {
	switch mode {
	case gallerySortNameAZ, gallerySortNameZA, gallerySortSizeDesc, gallerySortSizeAsc:
		return true
	default:
		return false
	}
}

func sortRecentItemsForDisplay(
	items []daemonclient.RecentItem,
	mode string,
	lookup func(int64) (daemonclient.FileMetadata, bool),
) {
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		leftMetadata, leftHasMetadata := lookup(left.FileID)
		rightMetadata, rightHasMetadata := lookup(right.FileID)

		switch mode {
		case gallerySortOldest:
			return compareRecentImportedAt(left, right, true)
		case gallerySortNameAZ:
			return compareRecentNames(left, leftMetadata, leftHasMetadata, right, rightMetadata, rightHasMetadata, true)
		case gallerySortNameZA:
			return compareRecentNames(left, leftMetadata, leftHasMetadata, right, rightMetadata, rightHasMetadata, false)
		case gallerySortSizeDesc:
			return compareRecentSizes(left, leftMetadata, leftHasMetadata, right, rightMetadata, rightHasMetadata, false)
		case gallerySortSizeAsc:
			return compareRecentSizes(left, leftMetadata, leftHasMetadata, right, rightMetadata, rightHasMetadata, true)
		case gallerySortNewest:
			fallthrough
		default:
			return compareRecentImportedAt(left, right, false)
		}
	})
}

func compareRecentImportedAt(left daemonclient.RecentItem, right daemonclient.RecentItem, ascending bool) bool {
	leftImportedAt := recentImportedAtValue(left)
	rightImportedAt := recentImportedAtValue(right)
	if leftImportedAt != rightImportedAt {
		if ascending {
			return leftImportedAt < rightImportedAt
		}

		return leftImportedAt > rightImportedAt
	}

	return left.FileID < right.FileID
}

func recentImportedAtValue(item daemonclient.RecentItem) int64 {
	if item.ImportedAtMS != nil {
		return *item.ImportedAtMS
	}

	return 0
}

func compareRecentNames(
	left daemonclient.RecentItem,
	leftMetadata daemonclient.FileMetadata,
	leftHasMetadata bool,
	right daemonclient.RecentItem,
	rightMetadata daemonclient.FileMetadata,
	rightHasMetadata bool,
	ascending bool,
) bool {
	leftLabel := recentSortLabel(left, leftMetadata, leftHasMetadata)
	rightLabel := recentSortLabel(right, rightMetadata, rightHasMetadata)
	if leftLabel != rightLabel {
		if ascending {
			return leftLabel < rightLabel
		}

		return leftLabel > rightLabel
	}

	return compareRecentImportedAt(left, right, false)
}

func recentSortLabel(item daemonclient.RecentItem, metadata daemonclient.FileMetadata, hasMetadata bool) string {
	if hasMetadata {
		title, _ := formatRecentTileText(item, metadata, true)
		return strings.ToLower(strings.TrimSpace(title))
	}

	return strings.ToLower(shortRecentTileHash(item.Hash))
}

func compareRecentSizes(
	left daemonclient.RecentItem,
	leftMetadata daemonclient.FileMetadata,
	leftHasMetadata bool,
	right daemonclient.RecentItem,
	rightMetadata daemonclient.FileMetadata,
	rightHasMetadata bool,
	ascending bool,
) bool {
	if leftHasMetadata != rightHasMetadata {
		return leftHasMetadata
	}

	leftSize := int64(0)
	if leftHasMetadata {
		leftSize = leftMetadata.Size
	}

	rightSize := int64(0)
	if rightHasMetadata {
		rightSize = rightMetadata.Size
	}

	if leftSize != rightSize {
		if ascending {
			return leftSize < rightSize
		}

		return leftSize > rightSize
	}

	return compareRecentNames(left, leftMetadata, leftHasMetadata, right, rightMetadata, rightHasMetadata, true)
}

func shortRecentTileHash(hash string) string {
	normalized := strings.TrimSpace(hash)
	if normalized == "" {
		return "unknown"
	}

	if len(normalized) <= 12 {
		return normalized
	}

	return normalized[:12]
}

func galleryLabelForMetadata(metadata daemonclient.FileMetadata, fallback string) string {
	preferredTags := orderedGalleryTags(metadata)
	for _, prefix := range []string{"creator:", "artist:", "person:", "studio:"} {
		if label := firstGalleryTagWithPrefix(preferredTags, prefix); label != "" {
			return label
		}
	}

	for _, prefix := range []string{"series:", "character:"} {
		if label := firstGalleryTagWithPrefix(preferredTags, prefix); label != "" {
			return label
		}
	}

	if len(preferredTags) > 0 {
		_, subtag := splitGalleryTag(preferredTags[0])
		if subtag != "" {
			return subtag
		}
	}

	return fallback
}

func gallerySeriesLabel(metadata daemonclient.FileMetadata) string {
	preferredTags := orderedGalleryTags(metadata)
	for _, prefix := range []string{"series:", "character:"} {
		if label := firstGalleryTagWithPrefix(preferredTags, prefix); label != "" {
			return label
		}
	}

	return ""
}

func orderedGalleryTags(metadata daemonclient.FileMetadata) []string {
	tags := []string{}
	seen := map[string]struct{}{}
	for _, service := range metadata.Tags {
		tagsByStatus := metadataTagPreferredTagsByStatus(service)
		for _, statusKey := range metadataTagStatusKeys(tagsByStatus) {
			for _, tag := range tagsByStatus[statusKey] {
				normalized := strings.TrimSpace(tag)
				if normalized == "" {
					continue
				}

				if _, ok := seen[normalized]; ok {
					continue
				}

				seen[normalized] = struct{}{}
				tags = append(tags, normalized)
			}
		}
	}

	return tags
}

func firstGalleryTagWithPrefix(tags []string, prefix string) string {
	normalizedPrefix := strings.ToLower(strings.TrimSpace(prefix))
	for _, tag := range tags {
		normalizedTag := strings.ToLower(strings.TrimSpace(tag))
		if !strings.HasPrefix(normalizedTag, normalizedPrefix) {
			continue
		}

		_, subtag := splitGalleryTag(tag)
		if subtag != "" {
			return subtag
		}
	}

	return ""
}

func splitGalleryTag(tag string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(tag), ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}

	return "", strings.TrimSpace(tag)
}

func parseTagEditorInput(input string) []string {
	parts := strings.FieldsFunc(input, func(r rune) bool {
		switch r {
		case ',', '\n', '\r', '\t':
			return true
		default:
			return false
		}
	})

	tags := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		if tag == "" {
			continue
		}

		if _, ok := seen[tag]; ok {
			continue
		}

		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}

	return tags
}

func appendTagEditorInput(existing string, tag string) string {
	normalizedTag := strings.TrimSpace(tag)
	if normalizedTag == "" {
		return existing
	}

	existingTags := parseTagEditorInput(existing)
	for _, existingTag := range existingTags {
		if existingTag == normalizedTag {
			return strings.Join(existingTags, "\n")
		}
	}

	existingTags = append(existingTags, normalizedTag)
	return strings.Join(existingTags, "\n")
}

func currentTagEditorPrefix(input string) string {
	trimmedInput := strings.TrimRight(input, " \t")
	if trimmedInput == "" {
		return ""
	}

	if strings.HasSuffix(trimmedInput, ",") || strings.HasSuffix(trimmedInput, "\n") || strings.HasSuffix(trimmedInput, "\r") {
		return ""
	}

	trimmed := strings.TrimRight(trimmedInput, "\n\r\t,")
	if trimmed == "" {
		return ""
	}

	start := strings.LastIndexAny(trimmed, ",\n\r\t")
	if start >= 0 {
		trimmed = trimmed[start+1:]
	}

	return strings.TrimSpace(trimmed)
}

func filterTagSuggestions(suggestions []string, prefix string) []string {
	normalizedPrefix := strings.TrimSpace(strings.ToLower(prefix))
	if normalizedPrefix == "" {
		return append([]string(nil), suggestions...)
	}

	filtered := []string{}
	for _, suggestion := range suggestions {
		normalizedSuggestion := strings.TrimSpace(strings.ToLower(suggestion))
		if strings.HasPrefix(normalizedSuggestion, normalizedPrefix) {
			filtered = append(filtered, suggestion)
		}
	}

	return filtered
}

func mergeTagSuggestions(groups ...[]string) []string {
	merged := []string{}
	seen := map[string]struct{}{}
	for _, group := range groups {
		for _, suggestion := range group {
			normalized := strings.TrimSpace(suggestion)
			if normalized == "" {
				continue
			}

			if _, ok := seen[normalized]; ok {
				continue
			}

			seen[normalized] = struct{}{}
			merged = append(merged, normalized)
		}
	}

	return merged
}

func collectTagEditorSuggestions(metadata daemonclient.FileMetadata) []string {
	seen := map[string]struct{}{}
	suggestions := []string{}
	for _, service := range metadata.Tags {
		for _, tags := range metadataTagPreferredTagsByStatus(service) {
			for _, tag := range tags {
				normalized := strings.TrimSpace(tag)
				if normalized == "" {
					continue
				}

				if _, ok := seen[normalized]; ok {
					continue
				}

				seen[normalized] = struct{}{}
				suggestions = append(suggestions, normalized)
			}
		}
	}

	sort.Strings(suggestions)
	return suggestions
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
