//go:build fyne

package fyneapp

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

type minSizeBox struct {
	widget.BaseWidget

	child   fyne.CanvasObject
	minSize fyne.Size
}

func newMinSizeBox(child fyne.CanvasObject, minSize fyne.Size) *minSizeBox {
	box := &minSizeBox{
		child:   child,
		minSize: minSize,
	}
	box.ExtendBaseWidget(box)
	return box
}

func (b *minSizeBox) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(b.child)
}

func (b *minSizeBox) MinSize() fyne.Size {
	childSize := b.child.MinSize()
	if childSize.Width < b.minSize.Width {
		childSize.Width = b.minSize.Width
	}
	if childSize.Height < b.minSize.Height {
		childSize.Height = b.minSize.Height
	}
	return childSize
}
