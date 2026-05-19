package clientsessions

import (
	"context"
	"fmt"
)

type Session struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Query          string `json:"query"`
	SortMode       string `json:"sort_mode"`
	SelectedFileID int64  `json:"selected_file_id"`
	Position       int    `json:"position"`
}

type CreateRequest struct {
	Name     string `json:"name"`
	SortMode string `json:"sort_mode"`
}

type UpdateRequest struct {
	Name           *string `json:"name,omitempty"`
	Query          *string `json:"query,omitempty"`
	SortMode       *string `json:"sort_mode,omitempty"`
	SelectedFileID *int64  `json:"selected_file_id,omitempty"`
	Position       *int    `json:"position,omitempty"`
}

type Store interface {
	ListSessions(ctx context.Context) ([]Session, error)
	CreateSession(ctx context.Context, req CreateRequest) (Session, error)
	UpdateSession(ctx context.Context, id int64, req UpdateRequest) (Session, error)
	DeleteSession(ctx context.Context, id int64) error
}

type NotFoundError struct {
	ID int64
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("session %d not found", e.ID)
}
