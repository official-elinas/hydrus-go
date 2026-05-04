//go:build fyne

package fyneapp

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"os/exec"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/official-elinas/hydrus-go/internal/desktop/daemonclient"
	"github.com/official-elinas/hydrus-go/internal/media/ffmpegutil"
)

const (
	watcherVideoByteLimit    = 512 << 20
	watcherVideoMaxDimension = 1280
)

var (
	videoPlaybackCheckOnce sync.Once
	videoPlaybackEnabled   bool
)

func supportsNativeVideoPlayback() bool {
	videoPlaybackCheckOnce.Do(func() {
		_, err := exec.LookPath("ffmpeg")
		videoPlaybackEnabled = err == nil
	})

	return videoPlaybackEnabled
}

type watcherVideoContent struct {
	root   fyne.CanvasObject
	image  *canvas.Image
	status *widget.Label
}

func newWatcherVideoContent(item daemonclient.RecentItem) *watcherVideoContent {
	viewer := canvas.NewImageFromImage(nil)
	viewer.FillMode = canvas.ImageFillContain
	viewer.ScaleMode = canvas.ImageScaleSmooth
	viewer.Hide()

	headline := widget.NewLabelWithStyle(
		fmt.Sprintf("file_id %d • %s", item.FileID, item.MIME),
		fyne.TextAlignLeading,
		fyne.TextStyle{Bold: true},
	)
	headline.Wrapping = fyne.TextTruncate

	status := widget.NewLabel("Buffering video from hydrusd...")
	status.Wrapping = fyne.TextWrapWord
	status.Alignment = fyne.TextAlignCenter

	footer := widget.NewLabel("In-app video playback via ffmpeg. Audio is muted in this prototype. Use arrow keys to navigate.")
	footer.Wrapping = fyne.TextWrapWord

	background := canvas.NewRectangle(color.NRGBA{R: 18, G: 18, B: 20, A: 255})
	content := container.NewBorder(
		container.NewPadded(container.NewVBox(headline, widget.NewSeparator())),
		container.NewPadded(footer),
		nil,
		nil,
		container.NewStack(container.NewPadded(viewer), container.NewCenter(container.NewPadded(status))),
	)

	return &watcherVideoContent{
		root:   container.NewStack(background, content),
		image:  viewer,
		status: status,
	}
}

func (c *watcherVideoContent) CanvasObject() fyne.CanvasObject {
	return c.root
}

func (c *watcherVideoContent) SetStatus(text string) {
	c.status.SetText(text)
	if text != "" {
		c.status.Show()
	}
}

func (c *watcherVideoContent) SetFrame(frame image.Image) {
	c.image.Image = frame
	c.image.Show()
	c.image.Refresh()
	c.status.Hide()
}

func (p *prototype) openNativeVideoWatcherForFile(fileID int64, item daemonclient.RecentItem, title string) {
	connection := p.currentConnection()
	if !connection.connected || connection.client == nil {
		return
	}

	content := newWatcherVideoContent(item)
	p.presentWatcherWindow(title, content.CanvasObject())
	ctx, cancel, requestID := p.beginWatcherRequest(2 * time.Hour)

	go func(connection connectionSnapshot, item daemonclient.RecentItem, ctx context.Context, cancel context.CancelFunc, requestID uint64, content *watcherVideoContent) {
		defer cancel()
		defer p.finishWatcherRequest(requestID)

		tempPath, cleanup, err := connection.client.FetchFileContentToTemp(ctx, item, watcherVideoByteLimit)
		if err != nil {
			if ctx.Err() != nil || !p.isCurrentWatcherRequest(requestID) {
				return
			}

			fyne.Do(func() {
				if !p.isCurrentOperation(connection) || !p.isCurrentWatcherRequest(requestID) {
					return
				}
				p.presentWatcherWindow(title, newWatcherMessageContent(item.MIME, "Could not load the video from hydrusd.\n\n"+err.Error()))
			})
			return
		}
		defer cleanup()

		if err := playVideoFileInWatcher(ctx, tempPath, item, content); err != nil {
			if ctx.Err() != nil || !p.isCurrentWatcherRequest(requestID) {
				return
			}

			fyne.Do(func() {
				if !p.isCurrentOperation(connection) || !p.isCurrentWatcherRequest(requestID) {
					return
				}
				p.presentWatcherWindow(title, newWatcherMessageContent(item.MIME, "In-app video playback could not start.\n\n"+err.Error()))
			})
		}
	}(connection, item, ctx, cancel, requestID, content)
}

func playVideoFileInWatcher(ctx context.Context, path string, item daemonclient.RecentItem, content *watcherVideoContent) error {
	width := item.Width
	height := item.Height
	if width == nil || height == nil {
		probedWidth, probedHeight, err := ffmpegutil.ProbeDimensions(ctx, path)
		if err != nil {
			return err
		}
		width = probedWidth
		height = probedHeight
	}

	targetWidth, targetHeight := fitVideoDimensions(*width, *height, watcherVideoMaxDimension)
	frameSize := targetWidth * targetHeight * 4
	if frameSize <= 0 {
		return fmt.Errorf("invalid video frame size %dx%d", targetWidth, targetHeight)
	}

	cmd := exec.CommandContext(
		ctx,
		"ffmpeg",
		"-nostdin",
		"-re",
		"-v", "error",
		"-i", path,
		"-an",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-vf", fmt.Sprintf("scale=w='min(iw,%d)':h='min(ih,%d)':force_original_aspect_ratio=decrease", watcherVideoMaxDimension, watcherVideoMaxDimension),
		"pipe:1",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open ffmpeg stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg playback: %w", err)
	}

	buffer := make([]byte, frameSize)
	for {
		if ctx.Err() != nil {
			_ = cmd.Wait()
			return ctx.Err()
		}

		_, err := io.ReadFull(stdout, buffer)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			_ = cmd.Wait()
			return fmt.Errorf("read ffmpeg video frame: %w", err)
		}

		frame := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))
		copy(frame.Pix, buffer)
		fyne.Do(func() {
			content.SetFrame(frame)
		})
	}

	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("wait for ffmpeg playback: %w", err)
	}

	if ctx.Err() == nil {
		fyne.Do(func() {
			content.SetStatus("Playback finished. Use arrow keys to navigate or close the watcher.")
		})
	}

	return nil
}

func fitVideoDimensions(width int64, height int64, maxDimension int) (int, int) {
	if width <= 0 || height <= 0 {
		return 1, 1
	}
	if maxDimension <= 0 {
		maxDimension = watcherVideoMaxDimension
	}

	targetWidth := int(width)
	targetHeight := int(height)
	if targetWidth > maxDimension || targetHeight > maxDimension {
		if targetWidth >= targetHeight {
			targetWidth = maxDimension
			targetHeight = max(1, int(height)*maxDimension/int(width))
		} else {
			targetHeight = maxDimension
			targetWidth = max(1, int(width)*maxDimension/int(height))
		}
	}

	return targetWidth, targetHeight
}
