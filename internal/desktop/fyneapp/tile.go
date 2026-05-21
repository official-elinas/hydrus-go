//go:build fyne

package fyneapp

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

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
	onDoubleTapped func()
}

func newMediaTile() *mediaTile {
	tile := &mediaTile{
		background:   canvas.NewRectangle(color.NRGBA{R: 28, G: 28, B: 30, A: 255}),
		previewBack:  canvas.NewRectangle(color.NRGBA{R: 18, G: 18, B: 20, A: 255}),
		selectionBox: canvas.NewRectangle(color.Transparent),
		preview:      canvas.NewImageFromImage(nil),
		overlay:      canvas.NewText("Loading", color.NRGBA{R: 170, G: 170, B: 170, A: 255}),
		title:        widget.NewLabel(""),
		subtitle:     widget.NewLabel(""),
	}

	tile.selectionBox.FillColor = color.Transparent
	tile.selectionBox.StrokeWidth = 2
	tile.selectionBox.StrokeColor = color.NRGBA{R: 58, G: 58, B: 62, A: 255}
	tile.preview.FillMode = canvas.ImageFillContain
	tile.preview.ScaleMode = canvas.ImageScaleSmooth
	tile.overlay.Alignment = fyne.TextAlignCenter
	tile.title.Wrapping = fyne.TextTruncate
	tile.subtitle.Wrapping = fyne.TextTruncate
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
	onDoubleTapped func(),
) {
	t.title.SetText(title)
	t.subtitle.SetText(subtitle)
	t.onTapped = onTapped
	t.onDoubleTapped = onDoubleTapped

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

func (t *mediaTile) DoubleTapped(*fyne.PointEvent) {
	if t.onDoubleTapped != nil {
		t.onDoubleTapped()
	}
}

func (t *mediaTile) CreateRenderer() fyne.WidgetRenderer {
	previewArea := container.NewStack(
		t.previewBack,
		t.preview,
		container.NewCenter(t.overlay),
		t.selectionBox,
	)
	textBlock := container.NewVBox(t.title, t.subtitle)
	content := container.NewBorder(
		nil,
		container.NewPadded(textBlock),
		nil,
		nil,
		container.NewPadded(previewArea),
	)

	return widget.NewSimpleRenderer(container.NewStack(t.background, content))
}

func (t *mediaTile) MinSize() fyne.Size {
	return fyne.NewSize(180, 200)
}
