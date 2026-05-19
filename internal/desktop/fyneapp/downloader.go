//go:build fyne

package fyneapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	coredownloader "github.com/official-elinas/hydrus-go/internal/core/downloader"
)

func (p *prototype) showDownloaderWindow() {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		dialog.ShowInformation("Downloader", "Connect to hydrusd before using the downloader.", p.window)
		return
	}

	if p.downloaderWindow != nil {
		p.downloaderWindow.RequestFocus()
		return
	}

	win := p.app.NewWindow("Downloader")
	win.Resize(fyne.NewSize(600, 420))
	win.SetPadded(true)
	win.SetOnClosed(func() { p.downloaderWindow = nil })
	p.downloaderWindow = win

	tabs := container.NewAppTabs(
		container.NewTabItem("URL", p.buildDownloaderURLTab(win, connection)),
		container.NewTabItem("Gallery", p.buildDownloaderGalleryTab(win, connection)),
		container.NewTabItem("Subscription", p.buildDownloaderSubscriptionTab(win, connection)),
	)
	tabs.SetTabLocation(container.TabLocationTop)

	win.SetContent(container.NewPadded(tabs))
	win.Show()
}

func (p *prototype) buildDownloaderURLTab(win fyne.Window, connection connectionSnapshot) fyne.CanvasObject {
	urlEntry := widget.NewEntry()
	urlEntry.SetPlaceHolder("https://example.com/post/123")

	statusLabel := widget.NewLabel("Queue a single URL through hydownloader.")
	statusLabel.Wrapping = fyne.TextWrapWord

	submitBtn := widget.NewButton("Queue URL", func() {
		rawURL := strings.TrimSpace(urlEntry.Text)
		if rawURL == "" {
			statusLabel.SetText("Enter a URL.")
			return
		}
		statusLabel.SetText("Queueing...")
		urlEntry.Disable()

		go func(conn connectionSnapshot, u string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err := conn.client.QueueDownloaderURL(ctx, coredownloader.URLRequest{URL: u})
			fyne.Do(func() {
				urlEntry.Enable()
				if err != nil {
					statusLabel.SetText(fmt.Sprintf("Failed: %v", err))
					return
				}
				statusLabel.SetText("Queued. hydownloader will download and autoimport the file.")
				urlEntry.SetText("")
			})
		}(connection, rawURL)
	})

	return container.NewVBox(
		widget.NewLabelWithStyle("Queue a single URL", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(widget.NewFormItem("URL", urlEntry)),
		submitBtn,
		statusLabel,
	)
}

func (p *prototype) buildDownloaderGalleryTab(win fyne.Window, connection connectionSnapshot) fyne.CanvasObject {
	downloaderSelect := widget.NewSelect(nil, nil)
	downloaderSelect.SetSelected("")

	keywordsEntry := widget.NewEntry()
	keywordsEntry.SetPlaceHolder("e.g. huke, artist:huke")

	statusLabel := widget.NewLabel("Loading available downloaders...")
	statusLabel.Wrapping = fyne.TextWrapWord

	var downloaderMap map[string]string

	go func(conn connectionSnapshot) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		m, err := conn.client.GetDownloaderDownloaders(ctx)
		fyne.Do(func() {
			if err != nil || len(m) == 0 {
				statusLabel.SetText("Could not load downloaders from hydrusd.")
				return
			}
			downloaderMap = m
			names := make([]string, 0, len(m))
			for name := range m {
				names = append(names, name)
			}
			downloaderSelect.Options = names
			if len(names) > 0 {
				downloaderSelect.SetSelected(names[0])
			}
			statusLabel.SetText("Select a downloader and enter keywords for a one-shot gallery fetch.")
		})
	}(connection)

	submitBtn := widget.NewButton("Queue Gallery", func() {
		name := strings.TrimSpace(downloaderSelect.Selected)
		keywords := strings.TrimSpace(keywordsEntry.Text)
		if name == "" {
			statusLabel.SetText("Select a downloader.")
			return
		}
		if keywords == "" {
			statusLabel.SetText("Enter keywords.")
			return
		}
		if downloaderMap == nil {
			statusLabel.SetText("Downloaders not yet loaded.")
			return
		}
		statusLabel.SetText("Queueing gallery...")
		downloaderSelect.Disable()
		keywordsEntry.Disable()

		go func(conn connectionSnapshot, n, kw string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err := conn.client.QueueDownloaderGallery(ctx, coredownloader.GalleryRequest{
				Downloader: n,
				Keywords:   kw,
			})
			fyne.Do(func() {
				downloaderSelect.Enable()
				keywordsEntry.Enable()
				if err != nil {
					statusLabel.SetText(fmt.Sprintf("Failed: %v", err))
					return
				}
				statusLabel.SetText("Gallery queued. Files will appear after hydownloader autoimports them.")
				keywordsEntry.SetText("")
			})
		}(connection, name, keywords)
	})

	return container.NewVBox(
		widget.NewLabelWithStyle("One-shot gallery fetch", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			widget.NewFormItem("Downloader", downloaderSelect),
			widget.NewFormItem("Keywords", keywordsEntry),
		),
		submitBtn,
		statusLabel,
	)
}

func (p *prototype) buildDownloaderSubscriptionTab(win fyne.Window, connection connectionSnapshot) fyne.CanvasObject {
	downloaderSelect := widget.NewSelect(nil, nil)
	downloaderSelect.SetSelected("")

	keywordsEntry := widget.NewEntry()
	keywordsEntry.SetPlaceHolder("e.g. huke")

	intervalEntry := widget.NewEntry()
	intervalEntry.SetPlaceHolder("86400")
	intervalEntry.SetText("86400")

	statusLabel := widget.NewLabel("Loading available downloaders...")
	statusLabel.Wrapping = fyne.TextWrapWord

	var downloaderMap map[string]string

	go func(conn connectionSnapshot) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		m, err := conn.client.GetDownloaderDownloaders(ctx)
		fyne.Do(func() {
			if err != nil || len(m) == 0 {
				statusLabel.SetText("Could not load downloaders from hydrusd.")
				return
			}
			downloaderMap = m
			names := make([]string, 0, len(m))
			for name := range m {
				names = append(names, name)
			}
			downloaderSelect.Options = names
			if len(names) > 0 {
				downloaderSelect.SetSelected(names[0])
			}
			statusLabel.SetText("Create a recurring subscription that checks for new files on a schedule.")
		})
	}(connection)

	submitBtn := widget.NewButton("Create Subscription", func() {
		name := strings.TrimSpace(downloaderSelect.Selected)
		keywords := strings.TrimSpace(keywordsEntry.Text)
		intervalStr := strings.TrimSpace(intervalEntry.Text)
		if name == "" {
			statusLabel.SetText("Select a downloader.")
			return
		}
		if keywords == "" {
			statusLabel.SetText("Enter keywords.")
			return
		}
		if downloaderMap == nil {
			statusLabel.SetText("Downloaders not yet loaded.")
			return
		}

		var interval int64 = 86400
		if intervalStr != "" {
			if _, err := fmt.Sscanf(intervalStr, "%d", &interval); err != nil || interval <= 0 {
				statusLabel.SetText("Check interval must be a positive number of seconds.")
				return
			}
		}

		statusLabel.SetText("Creating subscription...")
		downloaderSelect.Disable()
		keywordsEntry.Disable()
		intervalEntry.Disable()

		go func(conn connectionSnapshot, n, kw string, iv int64) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err := conn.client.QueueDownloaderSubscription(ctx, coredownloader.SubscriptionRequest{
				Downloader:    n,
				Keywords:      kw,
				CheckInterval: iv,
			})
			fyne.Do(func() {
				downloaderSelect.Enable()
				keywordsEntry.Enable()
				intervalEntry.Enable()
				if err != nil {
					statusLabel.SetText(fmt.Sprintf("Failed: %v", err))
					return
				}
				statusLabel.SetText(fmt.Sprintf("Subscription created. hydownloader will check every %d seconds.", iv))
				keywordsEntry.SetText("")
			})
		}(connection, name, keywords, interval)
	})

	return container.NewVBox(
		widget.NewLabelWithStyle("Recurring subscription", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			widget.NewFormItem("Downloader", downloaderSelect),
			widget.NewFormItem("Keywords", keywordsEntry),
			widget.NewFormItem("Check interval (s)", intervalEntry),
		),
		submitBtn,
		statusLabel,
	)
}
