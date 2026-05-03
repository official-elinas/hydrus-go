//go:build fyne

package fyneapp

import (
	"fyne.io/fyne/v2"
	"github.com/official-elinas/hydrus-go/internal/desktop/daemonclient"
	"testing"
)

func TestNextWatcherFileID(t *testing.T) {
	items := []daemonclient.RecentItem{
		{FileID: 10},
		{FileID: 20},
		{FileID: 30},
	}

	tests := []struct {
		name      string
		current   int64
		delta     int
		want      int64
		wantIndex int
	}{
		{"next item", 20, 1, 30, 2},
		{"previous item", 20, -1, 10, 0},
		{"zero delta", 20, 0, 20, 1},
		{"clamp at start", 10, -1, 10, 0},
		{"clamp at end", 30, 1, 30, 2},
		{"missing current", 99, 1, 0, -1},
		{"zero current", 0, 1, 0, -1},
		{"negative current", -1, 1, 0, -1},
		{"empty list", 20, 1, 0, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list := items
			if tt.name == "empty list" {
				list = nil
			}
			got, gotIndex := nextWatcherFileID(list, tt.current, tt.delta)
			if got != tt.want {
				t.Errorf("nextWatcherFileID(%v, %v) = %v, want %v", tt.current, tt.delta, got, tt.want)
			}
			if gotIndex != tt.wantIndex {
				t.Errorf("nextWatcherFileID(%v, %v) index = %v, want %v", tt.current, tt.delta, gotIndex, tt.wantIndex)
			}
		})
	}
}

func TestWatcherSurfaceScrolled(t *testing.T) {
	t.Run("maps fyne scroll deltas to navigation", func(t *testing.T) {
		got := make([]int, 0, 2)
		surface := newWatcherSurface(nil, func(delta int) {
			got = append(got, delta)
		})

		surface.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: 1}})
		surface.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: -1}})
		surface.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: 0}})

		want := []int{-1, 1}
		if len(got) != len(want) {
			t.Fatalf("navigation callback count = %d, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("navigation callback[%d] = %d, want %d", i, got[i], want[i])
			}
		}
	})

	t.Run("ignores scroll when navigation callback is nil", func(t *testing.T) {
		surface := newWatcherSurface(nil, nil)
		surface.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: 1}})
	})
}
