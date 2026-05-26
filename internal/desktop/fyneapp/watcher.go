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

func (s *watcherSurface) SwapContent(content fyne.CanvasObject) {
	s.content = content
	s.Refresh()
}

func (s *watcherSurface) CreateRenderer() fyne.WidgetRenderer {
	return &watcherSurfaceRenderer{surface: s}
}

func (s *watcherSurface) Scrolled(e *fyne.ScrollEvent) {
	if s.onNav == nil {
		return
	}
	if e.Scrolled.DY > 0 {
		s.onNav(-1)
	} else if e.Scrolled.DY < 0 {
		s.onNav(1)
	}
}

var _ fyne.Scrollable = (*watcherSurface)(nil)

type watcherSurfaceRenderer struct {
	surface *watcherSurface
}

func (r *watcherSurfaceRenderer) Layout(size fyne.Size) {
	r.surface.content.Resize(size)
	r.surface.content.Move(fyne.NewPos(0, 0))
}

func (r *watcherSurfaceRenderer) MinSize() fyne.Size {
	return r.surface.content.MinSize()
}

func (r *watcherSurfaceRenderer) Refresh() {
	r.surface.content.Refresh()
}

func (r *watcherSurfaceRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.surface.content}
}

func (r *watcherSurfaceRenderer) Destroy() {}
