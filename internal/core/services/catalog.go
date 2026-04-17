// Package services defines the bootstrap Hydrus service catalog model.
package services

import "encoding/hex"

// Type describes a Hydrus service type identifier.
type Type int

const (
	TypeTagRepository             Type = 0
	TypeFileRepository            Type = 1
	TypeLocalFileDomain           Type = 2
	TypeMessageDepot              Type = 3
	TypeLocalTag                  Type = 5
	TypeLocalRatingNumerical      Type = 6
	TypeLocalRatingLike           Type = 7
	TypeRatingNumericalRepository Type = 8
	TypeRatingLikeRepository      Type = 9
	TypeCombinedTag               Type = 10
	TypeCombinedFile              Type = 11
	TypeLocalBooru                Type = 12
	TypeIPFS                      Type = 13
	TypeLocalFileTrashDomain      Type = 14
	TypeHydrusLocalFileStorage    Type = 15
	TypeTestService               Type = 16
	TypeLocalNotes                Type = 17
	TypeClientAPIService          Type = 18
	TypeCombinedDeletedFile       Type = 19
	TypeLocalFileUpdateDomain     Type = 20
	TypeCombinedLocalFileDomains  Type = 21
	TypeLocalRatingIncDec         Type = 22
	TypeServerAdministration      Type = 99
	TypeNullService               Type = 100
)

// Service describes a single Hydrus service for API responses.
type Service struct {
	Name                        string                  `json:"name"`
	ServiceKey                  string                  `json:"service_key"`
	Type                        Type                    `json:"type"`
	TypePretty                  string                  `json:"type_pretty"`
	ShowInThumbnail             *bool                   `json:"show_in_thumbnail,omitempty"`
	ShowInThumbnailEvenWhenNull *bool                   `json:"show_in_thumbnail_even_when_null,omitempty"`
	StarShape                   string                  `json:"star_shape,omitempty"`
	Colours                     map[string]RatingColour `json:"colours,omitempty"`
	AllowsZero                  *bool                   `json:"allows_zero,omitempty"`
	MinStars                    *int                    `json:"min_stars,omitempty"`
	MaxStars                    *int                    `json:"max_stars,omitempty"`
}

// RatingColour is the Hydrus API colour payload for rating services.
type RatingColour struct {
	Pen   string `json:"pen"`
	Brush string `json:"brush"`
}

// LegacyService is the older service-object shape keyed by service key.
type LegacyService struct {
	Name                        string                  `json:"name"`
	Type                        Type                    `json:"type"`
	TypePretty                  string                  `json:"type_pretty"`
	ShowInThumbnail             *bool                   `json:"show_in_thumbnail,omitempty"`
	ShowInThumbnailEvenWhenNull *bool                   `json:"show_in_thumbnail_even_when_null,omitempty"`
	StarShape                   string                  `json:"star_shape,omitempty"`
	Colours                     map[string]RatingColour `json:"colours,omitempty"`
	AllowsZero                  *bool                   `json:"allows_zero,omitempty"`
	MinStars                    *int                    `json:"min_stars,omitempty"`
	MaxStars                    *int                    `json:"max_stars,omitempty"`
}

// Catalog is an ordered collection of Hydrus services.
type Catalog []Service

// DefaultCatalog returns the fixed bootstrap service catalog used before a real
// database-backed service manager exists.
func DefaultCatalog() Catalog {
	return Catalog{
		{
			Name:       "my tags",
			ServiceKey: keyHex("local tags"),
			Type:       TypeLocalTag,
			TypePretty: TypePretty(TypeLocalTag),
		},
		{
			Name:       "my files",
			ServiceKey: keyHex("local files"),
			Type:       TypeLocalFileDomain,
			TypePretty: TypePretty(TypeLocalFileDomain),
		},
		{
			Name:       "repository updates",
			ServiceKey: keyHex("repository updates"),
			Type:       TypeLocalFileUpdateDomain,
			TypePretty: TypePretty(TypeLocalFileUpdateDomain),
		},
		{
			Name:       "hydrus local file storage",
			ServiceKey: keyHex("all local files"),
			Type:       TypeHydrusLocalFileStorage,
			TypePretty: TypePretty(TypeHydrusLocalFileStorage),
		},
		{
			Name:       "combined local file domains",
			ServiceKey: keyHex("all local media"),
			Type:       TypeCombinedLocalFileDomains,
			TypePretty: TypePretty(TypeCombinedLocalFileDomains),
		},
		{
			Name:       "all known files",
			ServiceKey: keyHex("all known files"),
			Type:       TypeCombinedFile,
			TypePretty: TypePretty(TypeCombinedFile),
		},
		{
			Name:       "all known tags",
			ServiceKey: keyHex("all known tags"),
			Type:       TypeCombinedTag,
			TypePretty: TypePretty(TypeCombinedTag),
		},
		{
			Name:       "trash",
			ServiceKey: keyHex("trash"),
			Type:       TypeLocalFileTrashDomain,
			TypePretty: TypePretty(TypeLocalFileTrashDomain),
		},
	}
}

// LegacyMap converts the catalog into the older service-keyed response shape.
func (c Catalog) LegacyMap() map[string]LegacyService {
	services := map[string]LegacyService{}

	for _, service := range c {
		services[service.ServiceKey] = service.LegacyService()
	}

	return services
}

// Grouped groups services by the category names used by Hydrus service
// discovery responses.
func (c Catalog) Grouped() map[string][]Service {
	grouped := map[string][]Service{}
	for _, category := range discoveryGroupOrder {
		grouped[category] = []Service{}
	}

	for _, service := range c {
		category, ok := discoveryCategoryLookup[service.Type]
		if !ok {
			continue
		}

		grouped[category] = append(grouped[category], service)
	}

	return grouped
}

// ByKey finds a service by its service key.
func (c Catalog) ByKey(serviceKey string) (Service, bool) {
	for _, service := range c {
		if service.ServiceKey == serviceKey {
			return service, true
		}
	}

	return Service{}, false
}

// ByName finds the first service with the supplied display name.
func (c Catalog) ByName(name string) (Service, bool) {
	for _, service := range c {
		if service.Name == name {
			return service, true
		}
	}

	return Service{}, false
}

func keyHex(text string) string {
	return hex.EncodeToString([]byte(text))
}

// LegacyService returns the older service-object shape keyed by service key.
func (s Service) LegacyService() LegacyService {
	return LegacyService{
		Name:                        s.Name,
		Type:                        s.Type,
		TypePretty:                  s.TypePretty,
		ShowInThumbnail:             s.ShowInThumbnail,
		ShowInThumbnailEvenWhenNull: s.ShowInThumbnailEvenWhenNull,
		StarShape:                   s.StarShape,
		Colours:                     s.Colours,
		AllowsZero:                  s.AllowsZero,
		MinStars:                    s.MinStars,
		MaxStars:                    s.MaxStars,
	}
}
