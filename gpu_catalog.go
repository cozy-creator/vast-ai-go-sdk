package vast

import "strings"

// GPUSpec bridges a vast.ai gpu_name to the cozy platform's canonical SKU
// slug space (compilecache: python-gen-worker gen_worker.compile_cache
// sku_slug / tensorhub internal/orchestrator/compilecache.SKUSlug).
//
// The slug is derived from the CUDA DEVICE name as torch reports it on the
// box ("NVIDIA GeForce RTX 4090" -> "rtx-4090"), NOT from vast's marketing
// name. For consumer cards the two coincide; for datacenter cards they do
// not (vast "H100 SXM" is device "NVIDIA H100 80GB HBM3" -> slug
// "h100-80gb-hbm3"), so every entry carries an explicit Slug. This mirrors
// runpod-go-sdk's GPUSpec catalog so the forge pins SKUs identically across
// providers.
type GPUSpec struct {
	// GPUName is the vast.ai gpu_name as returned in offers and accepted by
	// OfferFilter.GPUName (e.g. "RTX 4090").
	GPUName string
	// Slug is the canonical compilecache SKU slug for the device
	// (e.g. "rtx-4090").
	Slug string
	// VRAMGB is the nominal per-GPU memory in GB. Some vast names cover
	// multiple VRAM variants (A100 PCIE 40/80) — offers carry the exact
	// gpu_ram; this is the common configuration.
	VRAMGB int
	// SMCapability is the CUDA compute capability x10 (86 = SM8.6,
	// 120 = SM12.0) — matches Offer.ComputeCap.
	SMCapability int
	// Consumer is true for GeForce SKUs (no attestation, marketplace boxes).
	Consumer bool
}

// gpuCatalog is the static SKU table, ordered by fallback preference:
// cheaper/more-liquid SKUs first (Longtail 2026-07 marketplace survey), so
// the order is directly usable as an offer-search preference chain.
var gpuCatalog = []GPUSpec{
	// Consumer 30-series (SM86)
	{GPUName: "RTX 3060", Slug: "rtx-3060", VRAMGB: 12, SMCapability: 86, Consumer: true},
	{GPUName: "RTX 3060 Ti", Slug: "rtx-3060-ti", VRAMGB: 8, SMCapability: 86, Consumer: true},
	{GPUName: "RTX 3070", Slug: "rtx-3070", VRAMGB: 8, SMCapability: 86, Consumer: true},
	{GPUName: "RTX 3080", Slug: "rtx-3080", VRAMGB: 10, SMCapability: 86, Consumer: true},
	{GPUName: "RTX 3080 Ti", Slug: "rtx-3080-ti", VRAMGB: 12, SMCapability: 86, Consumer: true},
	{GPUName: "RTX 3090", Slug: "rtx-3090", VRAMGB: 24, SMCapability: 86, Consumer: true},
	{GPUName: "RTX 3090 Ti", Slug: "rtx-3090-ti", VRAMGB: 24, SMCapability: 86, Consumer: true},

	// Consumer 40-series (SM89). vast abbreviates SUPER as "S"; the device
	// (and slug) spells it out.
	{GPUName: "RTX 4060 Ti", Slug: "rtx-4060-ti", VRAMGB: 16, SMCapability: 89, Consumer: true},
	{GPUName: "RTX 4070", Slug: "rtx-4070", VRAMGB: 12, SMCapability: 89, Consumer: true},
	{GPUName: "RTX 4070S", Slug: "rtx-4070-super", VRAMGB: 12, SMCapability: 89, Consumer: true},
	{GPUName: "RTX 4070 Ti", Slug: "rtx-4070-ti", VRAMGB: 12, SMCapability: 89, Consumer: true},
	{GPUName: "RTX 4070S Ti", Slug: "rtx-4070-ti-super", VRAMGB: 16, SMCapability: 89, Consumer: true},
	{GPUName: "RTX 4080", Slug: "rtx-4080", VRAMGB: 16, SMCapability: 89, Consumer: true},
	{GPUName: "RTX 4080S", Slug: "rtx-4080-super", VRAMGB: 16, SMCapability: 89, Consumer: true},
	{GPUName: "RTX 4090", Slug: "rtx-4090", VRAMGB: 24, SMCapability: 89, Consumer: true},

	// Consumer 50-series (SM120)
	{GPUName: "RTX 5060 Ti", Slug: "rtx-5060-ti", VRAMGB: 16, SMCapability: 120, Consumer: true},
	{GPUName: "RTX 5070", Slug: "rtx-5070", VRAMGB: 12, SMCapability: 120, Consumer: true},
	{GPUName: "RTX 5070 Ti", Slug: "rtx-5070-ti", VRAMGB: 16, SMCapability: 120, Consumer: true},
	{GPUName: "RTX 5080", Slug: "rtx-5080", VRAMGB: 16, SMCapability: 120, Consumer: true},
	{GPUName: "RTX 5090", Slug: "rtx-5090", VRAMGB: 32, SMCapability: 120, Consumer: true},

	// Workstation
	{GPUName: "RTX A4000", Slug: "rtx-a4000", VRAMGB: 16, SMCapability: 86},
	{GPUName: "RTX A5000", Slug: "rtx-a5000", VRAMGB: 24, SMCapability: 86},
	{GPUName: "RTX A6000", Slug: "rtx-a6000", VRAMGB: 48, SMCapability: 86},
	{GPUName: "A40", Slug: "a40", VRAMGB: 48, SMCapability: 86},
	{GPUName: "L40", Slug: "l40", VRAMGB: 48, SMCapability: 89},
	{GPUName: "L40S", Slug: "l40s", VRAMGB: 48, SMCapability: 89},
	{GPUName: "RTX 6000Ada", Slug: "rtx-6000-ada-generation", VRAMGB: 48, SMCapability: 89},
	{GPUName: "RTX PRO 6000", Slug: "rtx-pro-6000-blackwell-workstation-edition", VRAMGB: 96, SMCapability: 120},

	// Datacenter accelerators
	{GPUName: "A100 PCIE", Slug: "a100-80gb-pcie", VRAMGB: 80, SMCapability: 80},
	{GPUName: "A100 SXM4", Slug: "a100-sxm4-80gb", VRAMGB: 80, SMCapability: 80},
	{GPUName: "H100 PCIE", Slug: "h100-pcie", VRAMGB: 80, SMCapability: 90},
	{GPUName: "H100 SXM", Slug: "h100-80gb-hbm3", VRAMGB: 80, SMCapability: 90},
	{GPUName: "H100 NVL", Slug: "h100-nvl", VRAMGB: 94, SMCapability: 90},
	{GPUName: "H200", Slug: "h200", VRAMGB: 141, SMCapability: 90},
	{GPUName: "H200 NVL", Slug: "h200-nvl", VRAMGB: 141, SMCapability: 90},
	{GPUName: "B200", Slug: "b200", VRAMGB: 180, SMCapability: 100},
}

// GPUCatalog returns a copy of the static SKU catalog in fallback
// preference order (cheaper/more-liquid SKUs first).
func GPUCatalog() []GPUSpec {
	out := make([]GPUSpec, len(gpuCatalog))
	copy(out, gpuCatalog)
	return out
}

// GPUSpecByName looks up a catalog entry by vast gpu_name. Underscores are
// normalized to spaces ("RTX_4090" works) and matching is case-insensitive.
func GPUSpecByName(name string) (GPUSpec, bool) {
	name = NormalizeGPUName(name)
	for _, spec := range gpuCatalog {
		if strings.EqualFold(spec.GPUName, name) {
			return spec, true
		}
	}
	return GPUSpec{}, false
}

// GPUSpecBySlug looks up a catalog entry by canonical compilecache slug
// (e.g. "rtx-4090") — the reverse bridge used when a desired cell's SKU
// must be turned into a vast offer filter.
func GPUSpecBySlug(slug string) (GPUSpec, bool) {
	slug = strings.TrimSpace(strings.ToLower(slug))
	for _, spec := range gpuCatalog {
		if spec.Slug == slug {
			return spec, true
		}
	}
	return GPUSpec{}, false
}

// GPUNameForSlug returns the vast gpu_name for a compilecache SKU slug.
func GPUNameForSlug(slug string) (string, bool) {
	spec, ok := GPUSpecBySlug(slug)
	return spec.GPUName, ok
}

// GPUsWithAtLeast returns catalog entries with >= minVRAMGB of VRAM and
// SM capability >= minSM (x10, e.g. 89), preserving fallback order. Pass
// zeros to skip either constraint.
func GPUsWithAtLeast(minVRAMGB, minSM int) []GPUSpec {
	var out []GPUSpec
	for _, spec := range gpuCatalog {
		if spec.VRAMGB >= minVRAMGB && spec.SMCapability >= minSM {
			out = append(out, spec)
		}
	}
	return out
}

// GPUNames extracts the vast gpu_name strings from specs — directly usable
// as OfferFilter.GPUNames.
func GPUNames(specs []GPUSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.GPUName
	}
	return out
}

// SKUSlug is the canonical compilecache slugifier, byte-compatible with
// gen_worker.compile_cache.sku_slug and tensorhub compilecache.SKUSlug:
// "NVIDIA GeForce RTX 4090" -> "rtx-4090",
// "NVIDIA H100 80GB HBM3" -> "h100-80gb-hbm3".
//
// Apply it to CUDA device names. For vast gpu_name strings prefer the
// catalog (GPUSpecByName(...).Slug): vast's abbreviations ("RTX 4070S",
// "H100 SXM") do not slugify to the device slug.
func SKUSlug(gpuName string) string {
	s := strings.ToLower(gpuName)
	for _, noise := range []string{"nvidia", "geforce"} {
		s = strings.ReplaceAll(s, noise, " ")
	}
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return out
}
