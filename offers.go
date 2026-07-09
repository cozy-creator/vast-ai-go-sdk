package vast

import (
	"context"
	"net/http"
	"strings"
)

// Offer types (pricing model) accepted by OfferFilter.Type.
const (
	// OfferTypeOnDemand — fixed-price, exclusive until destroyed.
	OfferTypeOnDemand = "on-demand"
	// OfferTypeBid — interruptible: you bid, and the instance is PAUSED
	// (not destroyed) whenever outbid. Not for run-to-completion work.
	OfferTypeBid = "bid"
	// OfferTypeReserved — reserved-term pricing.
	OfferTypeReserved = "reserved"
)

// Offer is one rentable machine configuration returned by SearchOffers.
// The offer ID is the "ask" accepted by CreateInstance. Offers are a live
// marketplace view: an id can be rented out from under you at any moment
// (ErrOfferGone), so treat the list as immediately perishable.
type Offer struct {
	ID        int64 `json:"id"`
	AskID     int64 `json:"ask_contract_id"`
	MachineID int64 `json:"machine_id"`
	HostID    int64 `json:"host_id"`

	GPUName    string  `json:"gpu_name"`
	NumGPUs    int     `json:"num_gpus"`
	GPURAMMB   float64 `json:"gpu_ram"` // per-GPU VRAM in MB
	GPUArch    string  `json:"gpu_arch"`
	ComputeCap int     `json:"compute_cap"` // SM capability x10 (86, 89, 120)

	// DPHTotal is the on-demand $/hr for the whole offer (all GPUs).
	DPHTotal float64 `json:"dph_total"`
	// MinBid is the current minimum bid $/hr for interruptible rental.
	MinBid float64 `json:"min_bid"`
	// StorageCost is $/GB/month for disk (billed even while stopped).
	StorageCost float64 `json:"storage_cost"`
	// InetUpCost / InetDownCost are $/GB for traffic in each direction.
	InetUpCost   float64 `json:"inet_up_cost"`
	InetDownCost float64 `json:"inet_down_cost"`

	// Reliability is the host uptime score in [0,1].
	Reliability float64 `json:"reliability2"`
	// Verified means the machine passed vast's automated verification
	// (CUDA >= 12, >= 90% reliability).
	Verified bool `json:"verified"`
	// Datacenter is true for hosted datacenter machines (consumer cards are
	// overwhelmingly non-datacenter boxes).
	Datacenter bool   `json:"datacenter"`
	Hostname   string `json:"hostname"`

	CUDAMaxGood   float64 `json:"cuda_max_good"` // max CUDA version the driver supports
	DriverVersion string  `json:"driver_version"`

	CPUCores  float64 `json:"cpu_cores_effective"`
	CPURAMMB  float64 `json:"cpu_ram"`
	DiskSpace float64 `json:"disk_space"` // GB available
	DiskBW    float64 `json:"disk_bw"`
	InetUp    float64 `json:"inet_up"`   // Mbps
	InetDown  float64 `json:"inet_down"` // Mbps

	Geolocation string  `json:"geolocation"`
	DLPerf      float64 `json:"dlperf"`
	Rentable    bool    `json:"rentable"`
	Rented      bool    `json:"rented"`
}

// GPURAMGB returns per-GPU VRAM in whole GB.
func (o Offer) GPURAMGB() int { return int(o.GPURAMMB / 1024) }

// SKUSlug returns the compilecache SKU slug for the offer's GPU (via the
// static catalog when known, falling back to slugifying gpu_name).
func (o Offer) SKUSlug() string {
	if spec, ok := GPUSpecByName(o.GPUName); ok {
		return spec.Slug
	}
	return SKUSlug(o.GPUName)
}

// OfferFilter selects offers. Zero values mean "no constraint" except
// where noted. The SDK always constrains to rentable, un-rented offers.
type OfferFilter struct {
	// GPUName filters on the vast gpu_name (e.g. "RTX 4090"; underscores
	// are normalized to spaces, so "RTX_4090" also works). Mutually
	// exclusive with GPUNames.
	GPUName string
	// GPUNames filters on a set of acceptable gpu_names.
	GPUNames []string
	// NumGPUs requires exactly this many GPUs (most forge work wants 1).
	NumGPUs int
	// MinGPURAMGB requires at least this much per-GPU VRAM.
	MinGPURAMGB int
	// MinReliability requires host reliability >= this ([0,1], e.g. 0.98).
	MinReliability float64
	// MaxDPHTotal caps the on-demand $/hr for the whole offer.
	MaxDPHTotal float64
	// Verified, when non-nil, requires (or excludes) vast-verified machines.
	Verified *bool
	// Datacenter, when non-nil, requires (or excludes) datacenter hosts.
	Datacenter *bool
	// MinDiskGB requires at least this much rentable disk.
	MinDiskGB float64
	// MinCUDA requires cuda_max_good >= this (e.g. 12.4).
	MinCUDA float64
	// MinInetDownMbps / MinInetUpMbps set network floors.
	MinInetDownMbps float64
	MinInetUpMbps   float64
	// MinComputeCap requires SM capability x10 >= this (86, 89, 120).
	MinComputeCap int
	// Geolocation restricts to two-letter country codes (e.g. "US", "DE").
	Geolocation []string
	// Type is the pricing model: OfferTypeOnDemand (default), OfferTypeBid,
	// or OfferTypeReserved.
	Type string
	// OrderBy is a list of [field, direction] pairs; default sorts by
	// dph_total ascending (cheapest first).
	OrderBy [][2]string
	// Limit caps the number of offers returned (default 64).
	Limit int
	// External, when non-nil, includes/excludes offers outside vast's
	// standard pool. Defaults to false (exclude) like the vast CLI.
	External *bool
}

// buildQuery renders the filter into the POST /api/v0/bundles/ body:
// top-level {field: {op: value}} objects plus limit/type/order keys.
func (f *OfferFilter) buildQuery() (map[string]interface{}, error) {
	q := map[string]interface{}{
		"rentable": map[string]interface{}{"eq": true},
		"rented":   map[string]interface{}{"eq": false},
	}

	if f.GPUName != "" && len(f.GPUNames) > 0 {
		return nil, &ValidationError{Field: "GPUName", Message: "GPUName and GPUNames are mutually exclusive"}
	}
	if f.GPUName != "" {
		q["gpu_name"] = map[string]interface{}{"eq": NormalizeGPUName(f.GPUName)}
	}
	if len(f.GPUNames) > 0 {
		names := make([]string, len(f.GPUNames))
		for i, n := range f.GPUNames {
			names[i] = NormalizeGPUName(n)
		}
		q["gpu_name"] = map[string]interface{}{"in": names}
	}
	if f.NumGPUs > 0 {
		q["num_gpus"] = map[string]interface{}{"eq": f.NumGPUs}
	}
	if f.MinGPURAMGB > 0 {
		q["gpu_ram"] = map[string]interface{}{"gte": f.MinGPURAMGB * 1024}
	}
	if f.MinReliability > 0 {
		q["reliability2"] = map[string]interface{}{"gte": f.MinReliability}
	}
	if f.MaxDPHTotal > 0 {
		q["dph_total"] = map[string]interface{}{"lte": f.MaxDPHTotal}
	}
	if f.Verified != nil {
		q["verified"] = map[string]interface{}{"eq": *f.Verified}
	}
	if f.Datacenter != nil {
		q["datacenter"] = map[string]interface{}{"eq": *f.Datacenter}
	}
	if f.MinDiskGB > 0 {
		q["disk_space"] = map[string]interface{}{"gte": f.MinDiskGB}
	}
	if f.MinCUDA > 0 {
		q["cuda_max_good"] = map[string]interface{}{"gte": f.MinCUDA}
	}
	if f.MinInetDownMbps > 0 {
		q["inet_down"] = map[string]interface{}{"gte": f.MinInetDownMbps}
	}
	if f.MinInetUpMbps > 0 {
		q["inet_up"] = map[string]interface{}{"gte": f.MinInetUpMbps}
	}
	if f.MinComputeCap > 0 {
		q["compute_cap"] = map[string]interface{}{"gte": f.MinComputeCap}
	}
	if len(f.Geolocation) > 0 {
		q["geolocation"] = map[string]interface{}{"in": f.Geolocation}
	}

	external := false
	if f.External != nil {
		external = *f.External
	}
	q["external"] = map[string]interface{}{"eq": external}

	typ := f.Type
	switch typ {
	case "", "ondemand":
		typ = OfferTypeOnDemand
	case OfferTypeOnDemand, OfferTypeBid, OfferTypeReserved:
	default:
		return nil, &ValidationError{Field: "Type", Message: "must be on-demand, bid, or reserved"}
	}
	q["type"] = typ

	order := f.OrderBy
	if len(order) == 0 {
		order = [][2]string{{"dph_total", "asc"}}
	}
	orderList := make([][]string, len(order))
	for i, o := range order {
		orderList[i] = []string{o[0], o[1]}
	}
	q["order"] = orderList

	limit := f.Limit
	if limit <= 0 {
		limit = 64
	}
	q["limit"] = limit

	return q, nil
}

// SearchOffers queries the marketplace for rentable offers matching filter,
// cheapest first by default. A nil filter returns the cheapest rentable
// offers of any kind. Read-only and free — safe to call aggressively.
func (c *Client) SearchOffers(ctx context.Context, filter *OfferFilter) ([]Offer, error) {
	if filter == nil {
		filter = &OfferFilter{}
	}
	q, err := filter.buildQuery()
	if err != nil {
		return nil, err
	}
	var resp struct {
		Offers []Offer `json:"offers"`
	}
	// POST here is semantically a read: idempotent, retried on 5xx.
	if err := c.do(ctx, http.MethodPost, "/api/v0/bundles/", q, &resp, true); err != nil {
		return nil, err
	}
	return resp.Offers, nil
}

// Bool returns a pointer to b, for the tri-state OfferFilter fields.
func Bool(b bool) *bool { return &b }

// NormalizeGPUName canonicalizes a vast gpu_name: underscores (the vast CLI
// convention "RTX_4090") become spaces, and whitespace is collapsed.
func NormalizeGPUName(name string) string {
	name = strings.ReplaceAll(name, "_", " ")
	return strings.Join(strings.Fields(name), " ")
}
