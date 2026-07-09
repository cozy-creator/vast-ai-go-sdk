package vast_test

import (
	"context"
	"errors"
	"testing"

	vast "github.com/cozy-creator/vast-ai-go-sdk"
)

func boolPtr(b bool) *bool { return vast.Bool(b) }

func TestSearchOffersParsesFixture(t *testing.T) {
	ts := newTestServer(t)
	ts.handleJSON("/api/v0/bundles/", 200, fixture(t, "offers.json"))
	c := ts.client(t)

	offers, err := c.SearchOffers(context.Background(), &vast.OfferFilter{
		GPUName:        "RTX_3090", // underscore form must normalize
		NumGPUs:        1,
		MinReliability: 0.98,
		MaxDPHTotal:    0.40,
		Verified:       boolPtr(true),
		MinDiskGB:      30,
		MinCUDA:        12.4,
	})
	if err != nil {
		t.Fatalf("SearchOffers: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("got %d offers, want 2", len(offers))
	}

	o := offers[0]
	if o.ID != 19882713 || o.GPUName != "RTX 3090" || o.NumGPUs != 1 {
		t.Errorf("offer identity mismatch: %+v", o)
	}
	if o.GPURAMGB() != 24 {
		t.Errorf("GPURAMGB = %d, want 24", o.GPURAMGB())
	}
	if o.Reliability != 0.994512 || !o.Verified || o.Datacenter {
		t.Errorf("quality fields mismatch: %+v", o)
	}
	if o.DPHTotal != 0.089 || o.StorageCost != 0.12 || o.InetDownCost != 0.005 {
		t.Errorf("cost fields mismatch: %+v", o)
	}
	if o.CUDAMaxGood != 12.8 || o.DriverVersion != "570.86.15" || o.ComputeCap != 86 {
		t.Errorf("stack fields mismatch: %+v", o)
	}
	if o.SKUSlug() != "rtx-3090" {
		t.Errorf("SKUSlug = %q, want rtx-3090", o.SKUSlug())
	}

	// Request encoding: top-level {field: {op: value}} + defaults.
	body := ts.lastBody
	if body["type"] != "on-demand" {
		t.Errorf("type = %v, want on-demand", body["type"])
	}
	if got := body["gpu_name"].(map[string]interface{})["eq"]; got != "RTX 3090" {
		t.Errorf("gpu_name eq = %v, want normalized 'RTX 3090'", got)
	}
	if got := body["reliability2"].(map[string]interface{})["gte"]; got != 0.98 {
		t.Errorf("reliability2 gte = %v", got)
	}
	if got := body["dph_total"].(map[string]interface{})["lte"]; got != 0.40 {
		t.Errorf("dph_total lte = %v", got)
	}
	if got := body["gpu_ram"]; got != nil {
		t.Errorf("gpu_ram filter present without MinGPURAMGB: %v", got)
	}
	for _, def := range []struct {
		field string
		op    string
		want  interface{}
	}{
		{"rentable", "eq", true},
		{"rented", "eq", false},
		{"external", "eq", false},
		{"verified", "eq", true},
	} {
		got := body[def.field].(map[string]interface{})[def.op]
		if got != def.want {
			t.Errorf("%s %s = %v, want %v", def.field, def.op, got, def.want)
		}
	}
	order := body["order"].([]interface{})[0].([]interface{})
	if order[0] != "dph_total" || order[1] != "asc" {
		t.Errorf("default order = %v", order)
	}
	if body["limit"] != float64(64) {
		t.Errorf("limit = %v, want 64", body["limit"])
	}
}

func TestSearchOffersGPUNamesIn(t *testing.T) {
	ts := newTestServer(t)
	ts.handleJSON("/api/v0/bundles/", 200, `{"offers": []}`)
	c := ts.client(t)

	specs := vast.GPUsWithAtLeast(24, 86)
	_, err := c.SearchOffers(context.Background(), &vast.OfferFilter{GPUNames: vast.GPUNames(specs)})
	if err != nil {
		t.Fatalf("SearchOffers: %v", err)
	}
	in := ts.lastBody["gpu_name"].(map[string]interface{})["in"].([]interface{})
	if len(in) != len(specs) {
		t.Fatalf("gpu_name in has %d entries, want %d", len(in), len(specs))
	}
	if in[0] != "RTX 3090" {
		t.Errorf("first candidate = %v, want RTX 3090 (fallback order)", in[0])
	}
}

func TestSearchOffersValidation(t *testing.T) {
	ts := newTestServer(t)
	c := ts.client(t)

	_, err := c.SearchOffers(context.Background(), &vast.OfferFilter{GPUName: "RTX 4090", GPUNames: []string{"RTX 3090"}})
	var verr *vast.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("want ValidationError for GPUName+GPUNames, got %v", err)
	}

	_, err = c.SearchOffers(context.Background(), &vast.OfferFilter{Type: "spot"})
	if !errors.As(err, &verr) {
		t.Fatalf("want ValidationError for bad Type, got %v", err)
	}
}
