package services

import (
	"context"
	"strings"
	"testing"
)

func TestCatalogByName(t *testing.T) {
	t.Run("exact match wins over case insensitive fallback", func(t *testing.T) {
		catalog := Catalog{
			{
				Name:       "client api",
				ServiceKey: "lower",
			},
			{
				Name:       "Client API",
				ServiceKey: "exact",
			},
		}

		service, ok := catalog.ByName("Client API")
		if !ok {
			t.Fatal("ByName() ok = false, want true")
		}

		if service.ServiceKey != "exact" {
			t.Fatalf("service.ServiceKey = %q, want %q", service.ServiceKey, "exact")
		}
	})

	t.Run("case insensitive fallback uses catalog order", func(t *testing.T) {
		catalog := Catalog{
			{
				Name:       "client api",
				ServiceKey: "lower",
			},
			{
				Name:       "Client API",
				ServiceKey: "exact",
			},
		}

		service, ok := catalog.ByName("CLIENT API")
		if !ok {
			t.Fatal("ByName() ok = false, want true")
		}

		if service.ServiceKey != "lower" {
			t.Fatalf("service.ServiceKey = %q, want %q", service.ServiceKey, "lower")
		}
	})
}

func TestDefaultProvider(t *testing.T) {
	ctx := context.Background()
	provider := DefaultProvider()

	catalog, err := provider.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(catalog) != len(DefaultCatalog()) {
		t.Fatalf("len(catalog) = %d, want %d", len(catalog), len(DefaultCatalog()))
	}

	if _, ok := catalog.ByName("client api"); ok {
		t.Fatal("client api unexpectedly appeared in discovery list")
	}

	visibleService, ok, err := provider.ByName(ctx, "REPOSITORY UPDATES")
	if err != nil {
		t.Fatalf("ByName(visible) error = %v", err)
	}

	if !ok {
		t.Fatal("ByName(visible) ok = false, want true")
	}

	if visibleService.Name != "repository updates" {
		t.Fatalf("visibleService.Name = %q, want %q", visibleService.Name, "repository updates")
	}

	hiddenServices := []struct {
		name string
		key  string
	}{
		{name: "deleted from anywhere", key: keyHex("all deleted files")},
		{name: "local notes", key: keyHex("local notes")},
		{name: "client api", key: keyHex("client api")},
	}

	for _, tt := range hiddenServices {
		t.Run(tt.name+" direct lookups stay available", func(t *testing.T) {
			serviceByKey, ok, err := provider.ByKey(ctx, tt.key)
			if err != nil {
				t.Fatalf("ByKey() error = %v", err)
			}

			if !ok {
				t.Fatal("ByKey() ok = false, want true")
			}

			if serviceByKey.Name != tt.name {
				t.Fatalf("serviceByKey.Name = %q, want %q", serviceByKey.Name, tt.name)
			}

			serviceByName, ok, err := provider.ByName(ctx, strings.ToUpper(tt.name))
			if err != nil {
				t.Fatalf("ByName() error = %v", err)
			}

			if !ok {
				t.Fatal("ByName() ok = false, want true")
			}

			if serviceByName.ServiceKey != tt.key {
				t.Fatalf("serviceByName.ServiceKey = %q, want %q", serviceByName.ServiceKey, tt.key)
			}
		})
	}
}
