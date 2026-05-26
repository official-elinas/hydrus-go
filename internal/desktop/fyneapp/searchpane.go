//go:build fyne

package fyneapp

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/official-elinas/hydrus-go/internal/desktop/daemonclient"
)

type searchPane struct {
	galleryFilterQuery string
	gallerySortMode    string

	recent        []daemonclient.RecentItem
	searchResults []daemonclient.RecentItem

	recentLimit      int
	recentNextOffset int
	recentHasMore    bool

	searchNextOffset int
	searchHasMore    bool

	recentLoadBusy   bool
	galleryRequestID uint64

	searchSuggestions []string
	selectedFileID    int64

	gridHost *fyne.Container
	gridWrap *widget.GridWrap
}

func newSearchPane() *searchPane {
	return &searchPane{
		gallerySortMode: gallerySortNewest,
		recentLimit:     recentPageLimit,
		gridHost:        container.NewMax(),
	}
}

func (p *prototype) activePane() *searchPane {
	if pane, ok := p.panes[p.activeSessionID]; ok {
		return pane
	}
	pane := newSearchPane()
	p.panes[p.activeSessionID] = pane
	return pane
}
