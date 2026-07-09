package vast_test

import (
	"testing"

	vast "github.com/cozy-creator/vast-ai-go-sdk"
)

// TestSKUSlugVectors must stay byte-compatible with
// gen_worker.compile_cache.sku_slug and tensorhub compilecache.SKUSlug.
func TestSKUSlugVectors(t *testing.T) {
	vectors := map[string]string{
		"NVIDIA GeForce RTX 4090":        "rtx-4090",
		"NVIDIA GeForce RTX 3090":        "rtx-3090",
		"NVIDIA GeForce RTX 4070 SUPER":  "rtx-4070-super",
		"NVIDIA H100 80GB HBM3":          "h100-80gb-hbm3",
		"NVIDIA H100 PCIe":               "h100-pcie",
		"NVIDIA A100-SXM4-80GB":          "a100-sxm4-80gb",
		"NVIDIA RTX 6000 Ada Generation": "rtx-6000-ada-generation",
		"NVIDIA H200":                    "h200",
		"Tesla V100-SXM2-16GB":           "tesla-v100-sxm2-16gb",
		"RTX 4090":                       "rtx-4090", // vast gpu_name for consumer cards
		"":                               "",
	}
	for in, want := range vectors {
		if got := vast.SKUSlug(in); got != want {
			t.Errorf("SKUSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCatalogBridge(t *testing.T) {
	// Forward: vast gpu_name -> slug (incl. underscore + case laxity).
	for _, tc := range []struct{ name, slug string }{
		{"RTX 4090", "rtx-4090"},
		{"RTX_4090", "rtx-4090"},
		{"rtx 3090", "rtx-3090"},
		{"RTX 4070S", "rtx-4070-super"}, // vast abbreviation != naive slug
		{"H100 SXM", "h100-80gb-hbm3"},  // marketing name != device name
		{"A100 SXM4", "a100-sxm4-80gb"},
	} {
		spec, ok := vast.GPUSpecByName(tc.name)
		if !ok || spec.Slug != tc.slug {
			t.Errorf("GPUSpecByName(%q) = %+v ok=%v, want slug %q", tc.name, spec, ok, tc.slug)
		}
	}

	// Reverse: slug -> vast gpu_name.
	if name, ok := vast.GPUNameForSlug("rtx-5090"); !ok || name != "RTX 5090" {
		t.Errorf("GPUNameForSlug(rtx-5090) = %q ok=%v", name, ok)
	}
	if _, ok := vast.GPUNameForSlug("no-such-sku"); ok {
		t.Error("GPUNameForSlug must miss on unknown slugs")
	}
}

func TestCatalogSlugsSelfConsistent(t *testing.T) {
	// Every consumer entry's slug must equal SKUSlug of its own gpu_name
	// UNLESS it is a documented vast abbreviation (4070S/4080S style) —
	// those must still be valid slugs derivable from a device name.
	for _, spec := range vast.GPUCatalog() {
		if spec.Slug == "" {
			t.Errorf("%q has empty slug", spec.GPUName)
		}
		if spec.Slug != vast.SKUSlug(spec.Slug) {
			t.Errorf("%q slug %q is not slug-normal", spec.GPUName, spec.Slug)
		}
	}
}

func TestGPUsWithAtLeast(t *testing.T) {
	got := vast.GPUsWithAtLeast(24, 89)
	if len(got) == 0 {
		t.Fatal("no results")
	}
	for _, spec := range got {
		if spec.VRAMGB < 24 || spec.SMCapability < 89 {
			t.Errorf("%+v violates constraints", spec)
		}
	}
	// 4090 leads the >=24GB SM89 chain (cheapest liquid consumer SKU).
	if got[0].GPUName != "RTX 4090" {
		t.Errorf("first = %q, want RTX 4090", got[0].GPUName)
	}
}
