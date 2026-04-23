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
	"sort"
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
	previewByteLimit    = 16 << 20
	previewPixelLimit   = 16_000_000
	previewMaxDimension = 8192
	defaultDaemonURL    = "http://127.0.0.1:45869"
	defaultMetadataText = "Select a file from the grid to inspect the daemon-backed metadata state.\n\nThis prototype is focused on validating daemon-backed import/trash flows and early Hydrus-like layout work, not full UI parity yet."
	defaultPreviewText  = "Select a supported still image to\npreview the daemon-served original file."
	defaultTagsText     = "Select a file to inspect tag metadata from hydrusd."
)

// Run launches the Fyne thin-client prototype window.
func Run() {
	prototype := newPrototype()
	prototype.window.ShowAndRun()
}

type prototype struct {
	app           fyne.App
	window        fyne.Window
	connectWindow fyne.Window
	ptrWindow     fyne.Window
	stateMu       sync.RWMutex
	client        *daemonclient.Client

	connectButton        *widget.Button
	refreshButton        *widget.Button
	addButton            *widget.Button
	addFolderButton      *widget.Button
	ptrRefreshButton     *widget.Button
	clearQueueButton     *widget.Button
	retrySelectedButton  *widget.Button
	removeSelectedButton *widget.Button
	retryFailedButton    *widget.Button
	clearFinishedButton  *widget.Button
	trashButton          *widget.Button
	ptrSyncButton        *widget.Button

	connectionLabel   *widget.Label
	queueSummaryLabel *widget.Label
	queueDetailLabel  *widget.Label
	previewImage      *canvas.Image
	previewLabel      *widget.Label
	metadataLabel     *widget.Label
	tagsLabel         *widget.Label
	activityLabel     *widget.Label
	statusBarLabel    *widget.Label
	ptrStatusLabel    *widget.Label
	ptrHeadlineLabel  *widget.Label
	ptrProgressBar    *widget.ProgressBarInfinite
	queueList         *widget.List
	gridHost          *fyne.Container
	gridWrap          *widget.GridWrap

	recent           []daemonclient.RecentItem
	recentLimit      int
	recentNextOffset int
	recentHasMore    bool
	recentLoadBusy   bool
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
	ptrStatus        coreptrsync.Status
	ptrStatusLoaded  bool
	ptrStatusBusy    bool
	ptrStatusRequest uint64

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
		selectedQueueIndex: -1,
		thumbnailCache:     map[int64]fyne.Resource{},
		thumbnailLoads:     map[int64]struct{}{},
	}

	p.connectionLabel = widget.NewLabel("")
	p.connectionLabel.Wrapping = fyne.TextTruncate
	p.queueSummaryLabel = widget.NewLabel(formatImportQueueSummary(nil, false))
	p.queueSummaryLabel.Wrapping = fyne.TextTruncate
	p.queueDetailLabel = widget.NewLabel(defaultSelectedQueueText())
	p.queueDetailLabel.Wrapping = fyne.TextTruncate
	p.previewImage = canvas.NewImageFromImage(nil)
	p.previewImage.FillMode = canvas.ImageFillContain
	p.previewImage.Hide()
	p.previewLabel = widget.NewLabel(defaultPreviewText)
	p.previewLabel.Wrapping = fyne.TextTruncate
	p.previewLabel.Alignment = fyne.TextAlignCenter
	p.tagsLabel = widget.NewLabel(defaultTagsText)
	p.tagsLabel.Wrapping = fyne.TextWrapWord
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
	p.gridHost = container.NewMax()

	p.connectButton = widget.NewButton("Connect", p.showConnectDialog)
	p.refreshButton = widget.NewButton("Refresh", func() {
		p.fetchPTRStatus()
		p.reloadRecent(p.selectedFileID, "Refreshed recent files from hydrusd.")
	})
	p.ptrRefreshButton = widget.NewButton("Refresh PTR Status", p.fetchPTRStatus)
	p.addButton = widget.NewButton("Add File", p.showImportDialog)
	p.addFolderButton = widget.NewButton("Add Folder", p.showImportFolderDialog)
	p.retrySelectedButton = widget.NewButton("Retry Selected", p.retrySelectedQueueEntry)
	p.removeSelectedButton = widget.NewButton("Remove Selected", p.removeSelectedQueueEntry)
	p.retryFailedButton = widget.NewButton("Retry Failed", p.retryFailedQueueEntries)
	p.clearFinishedButton = widget.NewButton("Clear Finished", p.clearFinishedQueueEntries)
	p.clearQueueButton = widget.NewButton("Clear Queue", p.clearImportQueue)
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

	tagsScroll := container.NewVScroll(p.tagsLabel)
	tagSection := container.NewBorder(
		widget.NewLabelWithStyle("Selection tags", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil,
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
		widget.NewLabelWithStyle("Import queue", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		queueHelp,
		p.queueSummaryLabel,
		queueActionButtons,
	))

	queueFooter := container.NewPadded(container.NewVBox(
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Selected queue item", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.queueDetailLabel,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Last action", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.activityLabel,
	))

	queuePane := container.NewBorder(
		queueHeader,
		queueFooter,
		nil,
		nil,
		container.NewPadded(p.queueList),
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
		fyne.NewMenuItem("Trash Selected", p.confirmTrashSelected),
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

func (p *prototype) connectToDaemon(baseURL string, accessKey string) {
	candidate := daemonclient.New()
	if err := candidate.SetConnection(baseURL, accessKey); err != nil {
		dialog.ShowError(err, p.window)
		return
	}

	p.cancelPreviewRequest()
	attemptID, wasConnected := p.beginConnectAttempt()
	p.cancelPTRStatusRequests()
	p.stateMu.Lock()
	p.ptrStatusLoaded = false
	p.stateMu.Unlock()
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
	p.updateActionState()
}

func (p *prototype) applyRecentItems(items []daemonclient.RecentItem, preferredFileID int64, resetThumbnails bool) {
	if resetThumbnails {
		p.thumbnailCacheM.Lock()
		p.thumbnailGen++
		p.thumbnailCache = map[int64]fyne.Resource{}
		p.thumbnailLoads = map[int64]struct{}{}
		p.thumbnailCacheM.Unlock()
	}

	p.recent = items
	if preferredFileID > 0 && !p.hasRecentFile(preferredFileID) {
		preferredFileID = 0
	}

	p.selectedFileID = preferredFileID
	p.renderGrid()
	p.updateActionState()

	if p.selectedFileID > 0 {
		p.metadataLabel.SetText("Loading selected-file metadata from hydrusd...")
		p.tagsLabel.SetText("Loading tag metadata from hydrusd...")
		p.loadSelectedPreview(p.selectedFileID)
		p.loadSelectedMetadata(p.selectedFileID)
		return
	}

	p.metadataLabel.SetText(defaultMetadataText)
	p.tagsLabel.SetText(defaultTagsText)
	p.cancelPreviewRequest()
	p.clearSelectedPreview(defaultPreviewText)
}

func (p *prototype) renderGrid() {
	p.ensureGridWrap()

	if len(p.recent) == 0 {
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
			return len(p.recent)
		},
		func() fyne.CanvasObject {
			return newMediaTile()
		},
		func(id widget.GridWrapItemID, item fyne.CanvasObject) {
			if id < 0 || id >= len(p.recent) {
				return
			}

			recentItem := p.recent[id]
			tile := item.(*mediaTile)
			resource, overlay := p.lookupPreviewResource(recentItem.FileID)
			if resource == nil && overlay == "" {
				overlay = "Loading"
			}

			title := fmt.Sprintf("file_id %d", recentItem.FileID)
			subtitle := recentItem.MIME
			if recentItem.Width != nil && recentItem.Height != nil {
				subtitle = fmt.Sprintf("%s • %dx%d", recentItem.MIME, *recentItem.Width, *recentItem.Height)
			}

			tile.SetData(
				title,
				subtitle,
				resource,
				overlay,
				recentItem.FileID == p.selectedFileID,
				nil,
			)

			if resource == nil {
				p.ensurePreviewResource(recentItem)
			}
		},
	)
	p.gridWrap.OnSelected = func(id widget.GridWrapItemID) {
		if id < 0 || id >= len(p.recent) {
			return
		}
		p.selectFile(p.recent[id].FileID)
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
	p.tagsLabel.SetText("Loading tag metadata from hydrusd...")
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
				p.tagsLabel.SetText("Could not load tag metadata from hydrusd.")
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

			p.metadataLabel.SetText(formatMetadataDetails(metadata))
			p.tagsLabel.SetText(formatTagMetadata(metadata))
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
	} else {
		p.trashButton.Disable()
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

func formatMetadataTags(tags map[string]daemonclient.FileMetadataTagService) []string {
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

	if len(serviceKeys) == 0 {
		return nil
	}

	sort.Slice(serviceKeys, func(i, j int) bool {
		leftLabel := metadataTagServiceHeading(serviceKeys[i], tags[serviceKeys[i]])
		rightLabel := metadataTagServiceHeading(serviceKeys[j], tags[serviceKeys[j]])
		if leftLabel == rightLabel {
			return serviceKeys[i] < serviceKeys[j]
		}

		return leftLabel < rightLabel
	})

	lines := []string{}
	for _, serviceKey := range serviceKeys {
		lines = append(lines, formatMetadataTagService(serviceKey, tags[serviceKey])...)
	}

	return lines
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

func (p *prototype) pollPTRStatusUntilSettled(connection connectionSnapshot, requestID uint64) {
	go func(connection connectionSnapshot, requestID uint64) {
		ticker := time.NewTicker(ptrPollTick)
		defer ticker.Stop()

		for {
			<-ticker.C

			if !p.isCurrentOperation(connection) || !p.isCurrentPTRStatusRequest(requestID) {
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			status, err := connection.client.GetPTRStatus(ctx)
			cancel()
			if err != nil {
				return
			}

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
	case status.DownloadedUpdateCount > 0 || status.ProcessedDefinitionCount > 0 || status.ProcessedContentCount > 0 || status.MetadataSlice > 0:
		return "PTR sync: ✓ complete"
	default:
		return "PTR sync: idle"
	}
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
