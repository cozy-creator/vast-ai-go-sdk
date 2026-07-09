//go:build live

package vast_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	vast "github.com/cozy-creator/vast-ai-go-sdk"
)

// Live tests hit the real vast.ai API. Run with:
//
//	VAST_API_KEY=... go test -tags live ./...
//
// TestLiveRentDestroy additionally requires VAST_LIVE_RENT=1 — it spends
// real money (cheapest verified RTX 3090, destroyed immediately, ≤ ~$0.01).

func liveClient(t *testing.T) *vast.Client {
	t.Helper()
	key := os.Getenv("VAST_API_KEY")
	if key == "" {
		t.Skip("VAST_API_KEY not set")
	}
	c, err := vast.NewClient(key, vast.WithTimeout(60*time.Second))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestLiveBalance(t *testing.T) {
	c := liveClient(t)
	balance, err := c.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	t.Logf("balance: $%.2f", balance)
}

func TestLiveSearchOffers(t *testing.T) {
	c := liveClient(t)
	offers, err := c.SearchOffers(context.Background(), &vast.OfferFilter{
		GPUName:        "RTX 3090",
		NumGPUs:        1,
		MinReliability: 0.98,
		Verified:       boolPtr(true),
		MinDiskGB:      20,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("SearchOffers: %v", err)
	}
	if len(offers) == 0 {
		t.Fatal("no verified RTX 3090 offers — unexpected, 3090s are abundant")
	}
	for _, o := range offers {
		if o.GPUName != "RTX 3090" {
			t.Errorf("filter leak: got %q", o.GPUName)
		}
		if o.SKUSlug() != "rtx-3090" {
			t.Errorf("slug bridge: %q", o.SKUSlug())
		}
	}
	t.Logf("cheapest verified 3090: offer %d at $%.3f/hr (reliability %.3f, %s)",
		offers[0].ID, offers[0].DPHTotal, offers[0].Reliability, offers[0].Geolocation)
}

func TestLiveListInstances(t *testing.T) {
	c := liveClient(t)
	instances, err := c.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	t.Logf("%d instances", len(instances))
}

// TestLiveRentDestroy rents the cheapest suitable verified RTX 3090,
// waits briefly, destroys it, and verifies it is gone. Costs pennies.
func TestLiveRentDestroy(t *testing.T) {
	if os.Getenv("VAST_LIVE_RENT") != "1" {
		t.Skip("VAST_LIVE_RENT != 1 (this test spends real money)")
	}
	c := liveClient(t)
	ctx := context.Background()

	offers, err := c.SearchOffers(ctx, &vast.OfferFilter{
		GPUName:        "RTX 3090",
		NumGPUs:        1,
		MinReliability: 0.98,
		Verified:       boolPtr(true),
		MaxDPHTotal:    0.20,
		MinDiskGB:      15,
		Limit:          5,
	})
	if err != nil || len(offers) == 0 {
		t.Fatalf("SearchOffers: %v (%d offers)", err, len(offers))
	}

	var instanceID int64
	for _, offer := range offers {
		resp, err := c.CreateInstance(ctx, offer.ID, &vast.CreateInstanceRequest{
			Image:   "ubuntu:22.04",
			DiskGB:  10,
			Label:   "vast-ai-go-sdk-live-smoke",
			Onstart: "echo smoke && sleep 300",
		})
		if errors.Is(err, vast.ErrOfferGone) {
			t.Logf("offer %d gone, trying next", offer.ID)
			continue
		}
		if err != nil {
			t.Fatalf("CreateInstance(%d): %v", offer.ID, err)
		}
		instanceID = resp.InstanceID
		t.Logf("rented instance %d from offer %d at $%.3f/hr", instanceID, offer.ID, offer.DPHTotal)
		break
	}
	if instanceID == 0 {
		t.Fatal("every candidate offer was gone")
	}

	// Always destroy, even on failure below.
	defer func() {
		if err := c.DestroyInstance(ctx, instanceID); err != nil && !errors.Is(err, vast.ErrNotFound) {
			t.Errorf("DESTROY FAILED — instance %d may still be billing: %v", instanceID, err)
			return
		}
		// Verify billing stopped: the instance must disappear from GET.
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			_, err := c.GetInstance(ctx, instanceID)
			if errors.Is(err, vast.ErrNotFound) {
				t.Logf("instance %d destroyed and gone", instanceID)
				return
			}
			time.Sleep(5 * time.Second)
		}
		t.Errorf("instance %d still visible 2m after destroy — check the console", instanceID)
	}()

	inst, err := c.GetInstance(ctx, instanceID)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	t.Logf("instance state: actual=%q status=%q", inst.ActualStatus, inst.StatusMsg)
}
