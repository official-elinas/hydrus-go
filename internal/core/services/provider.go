package services

import (
	"context"
	"sort"
)

// Provider loads Hydrus services for API responses.
type Provider interface {
	List(context.Context) (Catalog, error)
	ByKey(context.Context, string) (Service, bool, error)
	ByName(context.Context, string) (Service, bool, error)
}

// StaticProvider serves a fixed in-memory service catalog.
type StaticProvider struct {
	catalog Catalog
}

// NewStaticProvider constructs an in-memory service provider.
func NewStaticProvider(catalog Catalog) StaticProvider {
	return StaticProvider{catalog: catalog}
}

// DefaultProvider constructs the bootstrap in-memory service provider.
func DefaultProvider() StaticProvider {
	return NewStaticProvider(DefaultCatalog())
}

// List returns the static catalog.
func (p StaticProvider) List(_ context.Context) (Catalog, error) {
	return p.catalog, nil
}

// ByKey finds a service by service key.
func (p StaticProvider) ByKey(_ context.Context, serviceKey string) (Service, bool, error) {
	service, ok := p.catalog.ByKey(serviceKey)
	return service, ok, nil
}

// ByName finds a service by display name.
func (p StaticProvider) ByName(_ context.Context, name string) (Service, bool, error) {
	service, ok := p.catalog.ByName(name)
	return service, ok, nil
}

var discoveryTypeOrder = []Type{
	TypeLocalTag,
	TypeTagRepository,
	TypeLocalFileDomain,
	TypeLocalFileUpdateDomain,
	TypeFileRepository,
	TypeHydrusLocalFileStorage,
	TypeCombinedLocalFileDomains,
	TypeCombinedFile,
	TypeCombinedTag,
	TypeLocalRatingLike,
	TypeLocalRatingNumerical,
	TypeLocalRatingIncDec,
	TypeLocalFileTrashDomain,
}

var discoveryTypeSet = map[Type]struct{}{
	TypeLocalTag:                 {},
	TypeTagRepository:            {},
	TypeLocalFileDomain:          {},
	TypeLocalFileUpdateDomain:    {},
	TypeFileRepository:           {},
	TypeHydrusLocalFileStorage:   {},
	TypeCombinedLocalFileDomains: {},
	TypeCombinedFile:             {},
	TypeCombinedTag:              {},
	TypeLocalRatingLike:          {},
	TypeLocalRatingNumerical:     {},
	TypeLocalRatingIncDec:        {},
	TypeLocalFileTrashDomain:     {},
}

var discoveryCategoryLookup = map[Type]string{
	TypeLocalTag:                 "local_tags",
	TypeTagRepository:            "tag_repositories",
	TypeLocalFileDomain:          "local_files",
	TypeLocalFileUpdateDomain:    "local_updates",
	TypeFileRepository:           "file_repositories",
	TypeHydrusLocalFileStorage:   "all_local_files",
	TypeCombinedLocalFileDomains: "all_local_media",
	TypeCombinedFile:             "all_known_files",
	TypeCombinedTag:              "all_known_tags",
	TypeLocalFileTrashDomain:     "trash",
}

var discoveryGroupOrder = []string{
	"local_tags",
	"tag_repositories",
	"local_files",
	"local_updates",
	"file_repositories",
	"all_local_files",
	"all_local_media",
	"all_known_files",
	"all_known_tags",
	"trash",
}

var discoveryTypeRank = map[Type]int{
	TypeLocalTag:                 0,
	TypeTagRepository:            1,
	TypeLocalFileDomain:          2,
	TypeLocalFileUpdateDomain:    3,
	TypeFileRepository:           4,
	TypeHydrusLocalFileStorage:   5,
	TypeCombinedLocalFileDomains: 6,
	TypeCombinedFile:             7,
	TypeCombinedTag:              8,
	TypeLocalRatingLike:          9,
	TypeLocalRatingNumerical:     10,
	TypeLocalRatingIncDec:        11,
	TypeLocalFileTrashDomain:     12,
}

var typePrettyLookup = map[Type]string{
	TypeTagRepository:             "hydrus tag repository",
	TypeFileRepository:            "hydrus file repository",
	TypeMessageDepot:              "hydrus message depot",
	TypeLocalFileDomain:           "local file domain",
	TypeLocalTag:                  "local tag domain",
	TypeLocalRatingNumerical:      "local numerical rating service",
	TypeLocalRatingLike:           "local like/dislike rating service",
	TypeRatingNumericalRepository: "hydrus numerical rating repository",
	TypeRatingLikeRepository:      "hydrus like/dislike rating repository",
	TypeCombinedTag:               "virtual combined tag domain",
	TypeCombinedFile:              "virtual combined file domain",
	TypeLocalBooru:                "client local booru",
	TypeIPFS:                      "ipfs daemon",
	TypeLocalFileTrashDomain:      "local trash file domain",
	TypeHydrusLocalFileStorage:    "virtual combined local file domain",
	TypeTestService:               "test service",
	TypeLocalNotes:                "local file notes service",
	TypeClientAPIService:          "client api",
	TypeCombinedDeletedFile:       "virtual deleted file service",
	TypeLocalFileUpdateDomain:     "local update file domain",
	TypeCombinedLocalFileDomains:  "virtual combined local media domain",
	TypeLocalRatingIncDec:         "local inc/dec rating service",
	TypeServerAdministration:      "hydrus server administration service",
	TypeNullService:               "null service",
}

// DiscoveryTypes returns the allowed Hydrus discovery service types in the same
// order Hydrus uses for service-list responses.
func DiscoveryTypes() []Type {
	cloned := make([]Type, len(discoveryTypeOrder))
	copy(cloned, discoveryTypeOrder)
	return cloned
}

// IsDiscoveryAllowed reports whether a service type is available through the
// current bootstrap discovery endpoints.
func IsDiscoveryAllowed(serviceType Type) bool {
	_, ok := discoveryTypeSet[serviceType]
	return ok
}

// TypePretty returns the Hydrus pretty string for a service type.
func TypePretty(serviceType Type) string {
	if pretty, ok := typePrettyLookup[serviceType]; ok {
		return pretty
	}

	return "unknown service"
}

// SortDiscoveryCatalog sorts a discovery catalog into Hydrus's service-type
// order while keeping within-type order stable.
func SortDiscoveryCatalog(catalog Catalog) {
	sort.SliceStable(catalog, func(i, j int) bool {
		leftRank, leftOK := discoveryTypeRank[catalog[i].Type]
		rightRank, rightOK := discoveryTypeRank[catalog[j].Type]

		if !leftOK && !rightOK {
			return catalog[i].Name < catalog[j].Name
		}

		if !leftOK {
			return false
		}

		if !rightOK {
			return true
		}

		return leftRank < rightRank
	})
}
