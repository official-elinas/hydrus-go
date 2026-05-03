//go:build fyne

package fyneapp

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

type watcherSurface struct {
	widget.BaseWidget
	content fyne.CanvasObject
	onNav   func(delta int)
}

func newWatcherSurface(content fyne.CanvasObject, onNav func(delta int)) *watcherSurface {
	s := &watcherSurface{
		content: content,
		onNav:   onNav,
	}
	s.ExtendBaseWidget(s)
	return s
}

func (s *watcherSurface) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(s.content)
}

func (s *watcherSurface) Scrolled(e *fyne.ScrollEvent) {
	if s.onNav == nil {
		return
	}
	// Match Fyne's scroll semantics: positive DY behaves like scroll up,
	// negative DY behaves like scroll down.
	if e.Scrolled.DY > 0 {
		s.onNav(-1) // Up
	} else if e.Scrolled.DY < 0 {
		s.onNav(1) // Down
	}
}

// ensure watcherSurface implements fyne.Scrollable
var _ fyne.Scrollable = (*watcherSurface)(nil)
