//go:build fyne

package fyneapp

import (
	"fmt"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (p *prototype) importQueueLength() int {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()

	return len(p.importQueue)
}

func makeImportQueueListItem() fyne.CanvasObject {
	title := widget.NewLabel("")
	title.Wrapping = fyne.TextWrapOff
	title.Truncation = fyne.TextTruncateEllipsis

	subtitle := widget.NewLabel("")
	subtitle.Wrapping = fyne.TextWrapOff
	subtitle.Truncation = fyne.TextTruncateEllipsis
	subtitle.TextStyle = fyne.TextStyle{Italic: true}

	return container.NewVBox(title, subtitle)
}

func (p *prototype) updateImportQueueListItem(id widget.ListItemID, item fyne.CanvasObject) {
	p.queueMu.Lock()
	if id < 0 || id >= len(p.importQueue) {
		p.queueMu.Unlock()
		return
	}
	entry := p.importQueue[id]
	p.queueMu.Unlock()

	containerItem, ok := item.(*fyne.Container)
	if !ok || len(containerItem.Objects) < 2 {
		return
	}

	title, _ := containerItem.Objects[0].(*widget.Label)
	subtitle, _ := containerItem.Objects[1].(*widget.Label)
	if title == nil || subtitle == nil {
		return
	}

	title.SetText(compactImportQueueTitle(entry))
	subtitle.SetText(compactImportQueueSubtitle(entry))
}

func (p *prototype) selectImportQueueEntry(id widget.ListItemID) {
	p.queueMu.Lock()
	p.selectedQueueIndex = int(id)
	p.queueMu.Unlock()

	p.renderSelectedImportQueueEntry()
	p.updateActionState()
}

func (p *prototype) unselectImportQueueEntry(id widget.ListItemID) {
	p.queueMu.Lock()
	if p.selectedQueueIndex == int(id) {
		p.selectedQueueIndex = -1
	}
	p.queueMu.Unlock()

	p.renderSelectedImportQueueEntry()
	p.updateActionState()
}

func (p *prototype) renderSelectedImportQueueEntry() {
	p.queueMu.Lock()
	selected := normalizeImportQueueSelection(p.selectedQueueIndex, len(p.importQueue))
	p.selectedQueueIndex = selected
	if selected < 0 {
		p.queueMu.Unlock()
		p.queueDetailLabel.SetText(defaultSelectedQueueText())
		return
	}

	entry := p.importQueue[selected]
	p.queueMu.Unlock()
	p.queueDetailLabel.SetText(formatSelectedImportQueueEntry(entry))
}

func (p *prototype) renderImportQueue() {
	p.queueMu.Lock()
	entries := append([]importQueueEntry(nil), p.importQueue...)
	running := p.importQueueRunning
	selected := normalizeImportQueueSelection(p.selectedQueueIndex, len(entries))
	p.selectedQueueIndex = selected
	p.queueMu.Unlock()

	p.queueSummaryLabel.SetText(formatImportQueueSummary(entries, running))
	p.queueList.Refresh()
	if selected >= 0 {
		p.queueList.Select(selected)
	} else {
		p.queueList.UnselectAll()
	}
	p.renderSelectedImportQueueEntry()
}

func (p *prototype) retrySelectedQueueEntry() {
	p.queueMu.Lock()
	selected := normalizeImportQueueSelection(p.selectedQueueIndex, len(p.importQueue))
	if selected < 0 {
		p.queueMu.Unlock()
		return
	}

	if !retryImportQueueEntry(&p.importQueue[selected], "Retrying selected queue item.") {
		p.queueMu.Unlock()
		return
	}
	path := p.importQueue[selected].Path
	p.queueMu.Unlock()

	p.renderImportQueue()
	p.updateActionState()
	p.startImportQueueProcessor()
	p.setStatus(fmt.Sprintf("Queued retry for %s.", filepath.Base(path)))
}

func (p *prototype) removeSelectedQueueEntry() {
	p.queueMu.Lock()
	if p.importQueueRunning {
		p.queueMu.Unlock()
		return
	}

	selected := normalizeImportQueueSelection(p.selectedQueueIndex, len(p.importQueue))
	if selected < 0 {
		p.queueMu.Unlock()
		return
	}

	removedPath := p.importQueue[selected].Path
	updated, removed := removeImportQueueEntry(p.importQueue, selected)
	if !removed {
		p.queueMu.Unlock()
		return
	}

	p.importQueue = updated
	p.selectedQueueIndex = normalizeImportQueueSelection(selected, len(updated))
	p.queueMu.Unlock()

	p.renderImportQueue()
	p.updateActionState()
	p.setStatus(fmt.Sprintf("Removed %s from the import queue.", filepath.Base(removedPath)))
}

func (p *prototype) retryFailedQueueEntries() {
	p.queueMu.Lock()
	retried := retryFailedImportQueueEntries(p.importQueue, "Retrying after queue review.")
	p.queueMu.Unlock()
	if retried == 0 {
		return
	}

	p.renderImportQueue()
	p.updateActionState()
	p.startImportQueueProcessor()
	p.setStatus(fmt.Sprintf("Re-queued %d failed import(s).", retried))
}

func (p *prototype) clearFinishedQueueEntries() {
	p.queueMu.Lock()
	if p.importQueueRunning {
		p.queueMu.Unlock()
		return
	}

	selectedPath := ""
	selected := normalizeImportQueueSelection(p.selectedQueueIndex, len(p.importQueue))
	if selected >= 0 {
		selectedPath = p.importQueue[selected].Path
	}

	updated, removed := clearFinishedImportQueueEntries(p.importQueue)
	if removed == 0 {
		p.queueMu.Unlock()
		return
	}

	p.importQueue = updated
	p.selectedQueueIndex = selectedImportQueueIndex(updated, selectedPath, selected)
	p.queueMu.Unlock()

	p.renderImportQueue()
	p.updateActionState()
	p.setStatus(fmt.Sprintf("Cleared %d finished import queue item(s).", removed))
}

func selectedImportQueueIndex(entries []importQueueEntry, selectedPath string, fallback int) int {
	if path := strings.TrimSpace(selectedPath); path != "" {
		if index := findImportQueueEntryByPath(entries, path); index >= 0 {
			return index
		}
	}

	return normalizeImportQueueSelection(fallback, len(entries))
}
