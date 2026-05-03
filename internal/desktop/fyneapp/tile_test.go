//go:build fyne

package fyneapp

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
)

func TestMediaTileSetDataSelection(t *testing.T) {
	tile := newMediaTile()
	selectedColor := color.NRGBA{R: 87, G: 201, B: 119, A: 255}
	defaultColor := color.NRGBA{R: 58, G: 58, B: 62, A: 255}

	tile.SetData(
		"title",
		"subtitle",
		nil,
		"Loading",
		true,
		nil,
		nil,
	)

	if got := color.NRGBAModel.Convert(tile.selectionBox.StrokeColor).(color.NRGBA); got != selectedColor {
		t.Fatalf("selected stroke color = %#v, want %#v", got, selectedColor)
	}

	tile.SetData(
		"title",
		"subtitle",
		nil,
		"Loading",
		false,
		nil,
		nil,
	)

	if got := color.NRGBAModel.Convert(tile.selectionBox.StrokeColor).(color.NRGBA); got != defaultColor {
		t.Fatalf("default stroke color = %#v, want %#v", got, defaultColor)
	}
}

func TestMediaTileDoubleTapped(t *testing.T) {
	tile := newMediaTile()
	called := 0

	tile.SetData(
		"title",
		"subtitle",
		nil,
		"Loading",
		false,
		nil,
		func() {
			called++
		},
	)

	tile.DoubleTapped(&fyne.PointEvent{})

	if called != 1 {
		t.Fatalf("double tap callback count = %d, want 1", called)
	}
}
