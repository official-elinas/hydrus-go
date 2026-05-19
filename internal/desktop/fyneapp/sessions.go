//go:build fyne

package fyneapp

import (
	"context"
	"log/slog"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"

	"github.com/official-elinas/hydrus-go/internal/core/clientsessions"
)

func (p *prototype) loadSearchSessions(client daemonSessionClient) {
	ctx := context.Background()
	sessions, err := client.ListSearchSessions(ctx)
	if err != nil {
		slog.Warn("could not load search sessions from daemon", "err", err)
		p.ensureDefaultSession(client)
		return
	}

	if len(sessions) == 0 {
		p.ensureDefaultSession(client)
		return
	}

	fyne.Do(func() {
		p.sessions = sessions
		p.rebuildSessionTabs()
		p.activateSession(sessions[0])
	})
}

func (p *prototype) ensureDefaultSession(client daemonSessionClient) {
	ctx := context.Background()
	s, err := client.CreateSearchSession(ctx, clientsessions.CreateRequest{
		Name:     "My Files",
		SortMode: gallerySortNewest,
	})
	if err != nil {
		slog.Warn("could not create default search session", "err", err)
		return
	}
	fyne.Do(func() {
		p.sessions = []clientsessions.Session{s}
		p.rebuildSessionTabs()
		p.activateSession(s)
	})
}

func (p *prototype) rebuildSessionTabs() {
	if p.sessionTabs == nil {
		return
	}
	current := p.sessionTabs.Selected()
	var currentID int64
	if current != nil {
		for _, s := range p.sessions {
			if s.Name == current.Text {
				currentID = s.ID
				break
			}
		}
	}

	p.tabSwitching = true
	defer func() { p.tabSwitching = false }()

	items := make([]*container.TabItem, len(p.sessions))
	for i, s := range p.sessions {
		items[i] = container.NewTabItem(s.Name, nil)
	}
	p.sessionTabs.SetItems(items)

	for i, s := range p.sessions {
		if s.ID == currentID {
			p.sessionTabs.Select(items[i])
			return
		}
	}
	if len(items) > 0 {
		p.sessionTabs.Select(items[0])
	}
}

func (p *prototype) activateSession(s clientsessions.Session) {
	p.activeSessionID = s.ID

	p.tabSwitching = true
	p.searchEntry.SetText(s.Query)
	if s.SortMode != "" {
		p.gallerySortSelect.SetSelected(s.SortMode)
	}
	p.tabSwitching = false

	p.reloadGallery(0, "")
}

func (p *prototype) persistActiveSession() {
	if p.activeSessionID <= 0 || p.tabSwitching {
		return
	}
	conn := p.currentConnection()
	if !conn.connected || conn.client == nil {
		return
	}
	query := p.searchEntry.Text
	sortMode := p.gallerySortMode
	id := p.activeSessionID
	go func() {
		ctx := context.Background()
		_, err := conn.client.UpdateSearchSession(ctx, id, clientsessions.UpdateRequest{
			Query:    &query,
			SortMode: &sortMode,
		})
		if err != nil {
			slog.Warn("could not persist session", "id", id, "err", err)
		}
	}()
}

func (p *prototype) newSearchSession() {
	conn := p.currentConnection()
	if !conn.connected || conn.client == nil {
		return
	}
	go func() {
		ctx := context.Background()
		s, err := conn.client.CreateSearchSession(ctx, clientsessions.CreateRequest{
			Name:     "New Tab",
			SortMode: gallerySortNewest,
		})
		if err != nil {
			slog.Warn("could not create search session", "err", err)
			return
		}
		fyne.Do(func() {
			p.sessions = append(p.sessions, s)
			p.rebuildSessionTabs()
			p.activateSession(s)
		})
	}()
}

func (p *prototype) closeSearchSession(s clientsessions.Session) {
	conn := p.currentConnection()
	if !conn.connected || conn.client == nil {
		return
	}
	go func() {
		ctx := context.Background()
		if err := conn.client.DeleteSearchSession(ctx, s.ID); err != nil {
			slog.Warn("could not delete search session", "id", s.ID, "err", err)
		}
	}()
	fyne.Do(func() {
		remaining := make([]clientsessions.Session, 0, len(p.sessions))
		for _, existing := range p.sessions {
			if existing.ID != s.ID {
				remaining = append(remaining, existing)
			}
		}
		p.sessions = remaining
		if len(p.sessions) == 0 {
			p.newSearchSession()
			return
		}
		p.rebuildSessionTabs()
		if p.activeSessionID == s.ID {
			p.activateSession(p.sessions[0])
		}
	})
}

type daemonSessionClient interface {
	ListSearchSessions(ctx context.Context) ([]clientsessions.Session, error)
	CreateSearchSession(ctx context.Context, req clientsessions.CreateRequest) (clientsessions.Session, error)
	UpdateSearchSession(ctx context.Context, id int64, req clientsessions.UpdateRequest) (clientsessions.Session, error)
	DeleteSearchSession(ctx context.Context, id int64) error
}
