//go:build fyne

package fyneapp

import (
	"testing"

	"fyne.io/fyne/v2"
)

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
