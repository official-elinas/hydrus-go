// Package services defines the bootstrap Hydrus service catalog model.
package services

import "encoding/hex"

// Type describes a Hydrus service type identifier.
type Type int

const (
	TypeTagRepository            Type = 0
	TypeFileRepository           Type = 1
	TypeLocalFileDomain          Type = 2
	TypeLocalTag                 Type = 5
	TypeLocalRatingNumerical     Type = 6
	TypeLocalRatingLike          Type = 7
	TypeCombinedTag              Type = 10
	TypeCombinedFile             Type = 11
	TypeIPFS                     Type = 13
	TypeLocalFileTrashDomain     Type = 14
	TypeHydrusLocalFileStorage   Type = 15
	TypeLocalNotes               Type = 17
	TypeClientAPIService         Type = 18
	TypeLocalFileUpdateDomain    Type = 20
	TypeCombinedLocalFileDomains Type = 21
	TypeLocalRatingIncDec        Type = 22
	TypeServerAdministration     Type = 99
)

// Service describes a single Hydrus service for API responses.
type Service struct {
	Name       string `json:"name"`
	ServiceKey string `json:"service_key"`
	Type       Type   `json:"type"`
	TypePretty string `json:"type_pretty"`
}

// LegacyService is the older service-object shape keyed by service key.
type LegacyService struct {
	Name       string `json:"name"`
	Type       Type   `json:"type"`
	TypePretty string `json:"type_pretty"`
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
			TypePretty: "local tag domain",
		},
		{
			Name:       "my files",
			ServiceKey: keyHex("local files"),
			Type:       TypeLocalFileDomain,
			TypePretty: "local file domain",
		},
		{
			Name:       "repository updates",
			ServiceKey: keyHex("repository updates"),
			Type:       TypeLocalFileUpdateDomain,
			TypePretty: "local update file domain",
		},
		{
			Name:       "hydrus local file storage",
			ServiceKey: keyHex("all local files"),
			Type:       TypeHydrusLocalFileStorage,
			TypePretty: "virtual combined local file domain",
		},
		{
			Name:       "combined local file domains",
			ServiceKey: keyHex("all local media"),
			Type:       TypeCombinedLocalFileDomains,
			TypePretty: "virtual combined local media domain",
		},
		{
			Name:       "all known files",
			ServiceKey: keyHex("all known files"),
			Type:       TypeCombinedFile,
			TypePretty: "virtual combined file domain",
		},
		{
			Name:       "all known tags",
			ServiceKey: keyHex("all known tags"),
			Type:       TypeCombinedTag,
			TypePretty: "virtual combined tag domain",
		},
		{
			Name:       "trash",
			ServiceKey: keyHex("trash"),
			Type:       TypeLocalFileTrashDomain,
			TypePretty: "local trash file domain",
		},
	}
}

// LegacyMap converts the catalog into the older service-keyed response shape.
func (c Catalog) LegacyMap() map[string]LegacyService {
	services := map[string]LegacyService{}

	for _, service := range c {
		services[service.ServiceKey] = LegacyService{
			Name:       service.Name,
			Type:       service.Type,
			TypePretty: service.TypePretty,
		}
	}

	return services
}

// Grouped groups services by the category names used by Hydrus service
// discovery responses.
func (c Catalog) Grouped() map[string][]Service {
	grouped := map[string][]Service{}

	for _, service := range c {
		category, ok := typeCategoryLookup[service.Type]
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

var typeCategoryLookup = map[Type]string{
	TypeLocalTag:                 "local_tags",
	TypeTagRepository:            "tag_repositories",
	TypeLocalFileDomain:          "local_files",
	TypeLocalFileUpdateDomain:    "local_updates",
	TypeFileRepository:           "file_repositories",
	TypeHydrusLocalFileStorage:   "all_local_files",
	TypeCombinedLocalFileDomains: "all_local_media",
	TypeCombinedFile:             "all_known_files",
	TypeCombinedTag:              "all_known_tags",
	TypeLocalRatingLike:          "local_rating_like",
	TypeLocalRatingNumerical:     "local_rating_numerical",
	TypeLocalRatingIncDec:        "local_rating_incdec",
	TypeLocalFileTrashDomain:     "trash",
}
