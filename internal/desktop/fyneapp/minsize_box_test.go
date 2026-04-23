//go:build fyne

package fyneapp

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

func TestMinSizeBox(t *testing.T) {
	t.Run("enforces minimum size over smaller child", func(t *testing.T) {
		child := widget.NewLabel("x")
		box := newMinSizeBox(child, fyne.NewSize(360, 240))

		got := box.MinSize()
		if got.Width < 360 || got.Height < 240 {
			t.Fatalf("box.MinSize() = %+v, want width >= 360 and height >= 240", got)
		}
	})

	t.Run("preserves larger child size", func(t *testing.T) {
		child := newMinSizeBox(widget.NewLabel("child"), fyne.NewSize(500, 300))
		box := newMinSizeBox(child, fyne.NewSize(360, 240))

		got := box.MinSize()
		if got.Width < 500 || got.Height < 300 {
			t.Fatalf("box.MinSize() = %+v, want width >= 500 and height >= 300", got)
		}
	})
}
