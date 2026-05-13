// Package httpapi contains the bootstrap HTTP API and auth surface.
package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const expiredSessionStatusCode = 419

const sessionLifetime = 24 * time.Hour

// Permission is a Hydrus Client API basic permission identifier.
type Permission int

const (
	PermissionImportAndEditURLs       Permission = 0
	PermissionImportAndDeleteFiles    Permission = 1
	PermissionEditFileTags            Permission = 2
	PermissionSearchAndFetchFiles     Permission = 3
	PermissionManagePages             Permission = 4
	PermissionManageCookiesAndHeaders Permission = 5
	PermissionManageDatabase          Permission = 6
	PermissionEditFileNotes           Permission = 7
	PermissionEditFileRelationships   Permission = 8
	PermissionEditFileRatings         Permission = 9
	PermissionManagePopups            Permission = 10
	PermissionEditFileTimes           Permission = 11
	PermissionCommitPending           Permission = 12
	PermissionSeeLocalPaths           Permission = 13
)

// Principal describes the authenticated identity attached to a request.
type Principal struct {
	Name              string
	AccessKey         string
	PermitsEverything bool
	BasicPermissions  []Permission
}

type sessionState struct {
	accessKey string
	expiresAt time.Time
}

// AccessControl manages the bootstrap access-key and session-key auth state.
type AccessControl struct {
	mu                 sync.RWMutex
	principal          Principal
	sessions           map[string]sessionState
	generatedAccessKey bool
}

// NewAccessControl creates the in-memory bootstrap auth manager.
func NewAccessControl(
	accessKey string,
	name string,
	permissions []Permission,
) (*AccessControl, error) {
	if strings.TrimSpace(name) == "" {
		name = "hydrus-go"
	}

	normalizedAccessKey, generated, err := normalizeOrGenerateAccessKey(accessKey)
	if err != nil {
		return nil, err
	}

	storedPermissions := make([]Permission, len(permissions))
	copy(storedPermissions, permissions)

	return &AccessControl{
		principal: Principal{
			Name:              name,
			AccessKey:         normalizedAccessKey,
			PermitsEverything: false,
			BasicPermissions:  storedPermissions,
		},
		sessions:           map[string]sessionState{},
		generatedAccessKey: generated,
	}, nil
}

// AccessKey returns the configured or generated bootstrap access key.
func (a *AccessControl) AccessKey() string {
	return a.principal.AccessKey
}

// GeneratedAccessKey reports whether the access key was generated at startup
// and, if so, returns it.
func (a *AccessControl) GeneratedAccessKey() (string, bool) {
	if !a.generatedAccessKey {
		return "", false
	}

	return a.principal.AccessKey, true
}

// Authorize authenticates the request and enforces any required permissions.
func (a *AccessControl) Authorize(
	r *http.Request,
	required ...Permission,
) (Principal, int, error) {
	principal, statusCode, err := a.authenticate(r)
	if err != nil {
		return Principal{}, statusCode, err
	}

	if len(required) == 0 || principal.PermitsEverything {
		return principal, 0, nil
	}

	if hasAnyPermission(principal.BasicPermissions, required) {
		return principal, 0, nil
	}

	return Principal{}, http.StatusForbidden, errors.New("insufficient permissions")
}

// NewSession creates a new in-memory session key for a valid access key.
func (a *AccessControl) NewSession(accessKey string) (string, error) {
	normalizedAccessKey, err := normalizeCredential(accessKey)
	if err != nil {
		return "", fmt.Errorf("normalize access key: %w", err)
	}

	if subtle.ConstantTimeCompare(
		[]byte(normalizedAccessKey),
		[]byte(a.principal.AccessKey),
	) != 1 {
		return "", errors.New("invalid access key")
	}

	sessionKey, err := generateCredential()
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.pruneExpiredSessionsLocked(time.Now())

	a.sessions[sessionKey] = sessionState{
		accessKey: normalizedAccessKey,
		expiresAt: time.Now().Add(sessionLifetime),
	}

	return sessionKey, nil
}

func (a *AccessControl) authenticate(r *http.Request) (Principal, int, error) {
	accessKey := firstNonEmpty(
		r.Header.Get("Hydrus-Client-API-Access-Key"),
		r.URL.Query().Get("Hydrus-Client-API-Access-Key"),
	)

	sessionKey := firstNonEmpty(
		r.Header.Get("Hydrus-Client-API-Session-Key"),
		r.URL.Query().Get("Hydrus-Client-API-Session-Key"),
	)

	if accessKey == "" && sessionKey == "" {
		return Principal{}, http.StatusUnauthorized, errors.New("missing access or session key")
	}

	if accessKey != "" {
		return a.authenticateAccessKey(accessKey)
	}

	return a.authenticateSessionKey(sessionKey)
}

func (a *AccessControl) authenticateAccessKey(accessKey string) (Principal, int, error) {
	normalizedAccessKey, err := normalizeCredential(accessKey)
	if err != nil {
		return Principal{}, http.StatusForbidden, errors.New("invalid access key")
	}

	if subtle.ConstantTimeCompare(
		[]byte(normalizedAccessKey),
		[]byte(a.principal.AccessKey),
	) != 1 {
		return Principal{}, http.StatusForbidden, errors.New("invalid access key")
	}

	return a.principal, 0, nil
}

func (a *AccessControl) authenticateSessionKey(sessionKey string) (Principal, int, error) {
	normalizedSessionKey, err := normalizeCredential(sessionKey)
	if err != nil {
		return Principal{}, expiredSessionStatusCode, errors.New("session key has expired")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	storedSession, ok := a.sessions[normalizedSessionKey]
	if !ok {
		return Principal{}, expiredSessionStatusCode, errors.New("session key has expired")
	}

	if time.Now().After(storedSession.expiresAt) {
		delete(a.sessions, normalizedSessionKey)
		return Principal{}, expiredSessionStatusCode, errors.New("session key has expired")
	}

	storedSession.expiresAt = time.Now().Add(sessionLifetime)
	a.sessions[normalizedSessionKey] = storedSession

	return a.principal, 0, nil
}

func (a *AccessControl) pruneExpiredSessionsLocked(now time.Time) {
	for sessionKey, storedSession := range a.sessions {
		if now.After(storedSession.expiresAt) {
			delete(a.sessions, sessionKey)
		}
	}
}

func normalizeOrGenerateAccessKey(accessKey string) (string, bool, error) {
	if strings.TrimSpace(accessKey) == "" {
		generatedAccessKey, err := generateCredential()
		if err != nil {
			return "", false, err
		}

		return generatedAccessKey, true, nil
	}

	normalizedAccessKey, err := normalizeCredential(accessKey)
	if err != nil {
		return "", false, err
	}

	return normalizedAccessKey, false, nil
}

func normalizeCredential(raw string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return "", errors.New("credential is empty")
	}

	return normalized, nil
}

func generateCredential() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate credential: %w", err)
	}

	return hex.EncodeToString(buffer), nil
}

func hasAnyPermission(granted []Permission, required []Permission) bool {
	for _, requirement := range required {
		for _, permission := range granted {
			if permission == requirement {
				return true
			}
		}
	}

	return false
}

func permissionInts(permissions []Permission) []int {
	values := make([]int, 0, len(permissions))

	for _, permission := range permissions {
		values = append(values, int(permission))
	}

	return values
}

func permissionDescription(permissions []Permission) string {
	descriptions := make([]string, 0, len(permissions))

	for _, permission := range permissions {
		description, ok := permissionDescriptionLookup[permission]
		if !ok {
			continue
		}

		descriptions = append(descriptions, description)
	}

	if len(descriptions) == 0 {
		return "API Permissions: no permissions assigned"
	}

	return "API Permissions: " + strings.Join(descriptions, ", ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}

var permissionDescriptionLookup = map[Permission]string{
	PermissionImportAndEditURLs:       "import and edit URLs",
	PermissionImportAndDeleteFiles:    "import and delete files",
	PermissionEditFileTags:            "edit file tags",
	PermissionSearchAndFetchFiles:     "search for and fetch files",
	PermissionManagePages:             "manage pages",
	PermissionManageCookiesAndHeaders: "manage cookies and headers",
	PermissionManageDatabase:          "manage database",
	PermissionEditFileNotes:           "edit file notes",
	PermissionEditFileRelationships:   "edit file relationships",
	PermissionEditFileRatings:         "edit file ratings",
	PermissionManagePopups:            "manage popups",
	PermissionEditFileTimes:           "edit file times",
	PermissionCommitPending:           "commit pending",
	PermissionSeeLocalPaths:           "see local paths",
}
