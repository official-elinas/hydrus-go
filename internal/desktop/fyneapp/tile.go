//go:build fyne

package fyneapp

import (
	"image"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

var tilePlaceholderImage = image.NewNRGBA(image.Rect(0, 0, 1, 1))

type mediaTile struct {
	widget.BaseWidget

	background   *canvas.Rectangle
	previewBack  *canvas.Rectangle
	selectionBox *canvas.Rectangle
	preview      *canvas.Image
	overlay      *canvas.Text
	title        *widget.Label
	subtitle     *widget.Label
	onTapped     func()
}

func newMediaTile() *mediaTile {
	tile := &mediaTile{
		background:   canvas.NewRectangle(color.NRGBA{R: 28, G: 28, B: 30, A: 255}),
		previewBack:  canvas.NewRectangle(color.NRGBA{R: 18, G: 18, B: 20, A: 255}),
		selectionBox: canvas.NewRectangle(color.Transparent),
		preview:      canvas.NewImageFromImage(tilePlaceholderImage),
		overlay:      canvas.NewText("Loading", color.NRGBA{R: 170, G: 170, B: 170, A: 255}),
		title:        widget.NewLabel(""),
		subtitle:     widget.NewLabel(""),
	}

	tile.selectionBox.FillColor = color.Transparent
	tile.selectionBox.StrokeWidth = 2
	tile.selectionBox.StrokeColor = color.NRGBA{R: 58, G: 58, B: 62, A: 255}
	tile.preview.FillMode = canvas.ImageFillContain
	tile.overlay.Alignment = fyne.TextAlignCenter
	tile.title.Wrapping = fyne.TextWrapWord
	tile.subtitle.Wrapping = fyne.TextWrapWord
	tile.subtitle.TextStyle = fyne.TextStyle{Italic: true}

	tile.ExtendBaseWidget(tile)
	return tile
}

func (t *mediaTile) SetData(
	title string,
	subtitle string,
	preview fyne.Resource,
	overlay string,
	selected bool,
	onTapped func(),
) {
	t.title.SetText(title)
	t.subtitle.SetText(subtitle)
	t.onTapped = onTapped

	t.preview.Image = nil
	t.preview.Resource = preview
	t.preview.Refresh()

	t.overlay.Text = overlay
	t.overlay.Refresh()

	if selected {
		t.selectionBox.StrokeColor = color.NRGBA{R: 87, G: 201, B: 119, A: 255}

	} else {
		t.selectionBox.StrokeColor = color.NRGBA{R: 58, G: 58, B: 62, A: 255}
	}
	t.selectionBox.Refresh()
}

func (t *mediaTile) Tapped(*fyne.PointEvent) {
	if t.onTapped != nil {
		t.onTapped()
	}
}

func (t *mediaTile) CreateRenderer() fyne.WidgetRenderer {
	previewArea := container.NewStack(
		t.previewBack,
		t.preview,
		container.NewCenter(t.overlay),
		t.selectionBox,
	)
	content := container.NewVBox(
		container.NewPadded(previewArea),
		container.NewPadded(t.title),
		container.NewPadded(t.subtitle),
	)

	return widget.NewSimpleRenderer(container.NewStack(t.background, content))
}

func (t *mediaTile) MinSize() fyne.Size {
	return fyne.NewSize(180, 220)
}
