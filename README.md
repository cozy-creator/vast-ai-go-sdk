# vast-ai-go-sdk

Go client for the vast.ai GPU marketplace API. Covers the four-verb lifecycle
the cozy platform consumes — **search offers → create instance → poll →
destroy** — plus account balance and a static GPU catalog bridging vast
`gpu_name` strings to the compilecache SKU-slug space.

```bash
go get github.com/cozy-creator/vast-ai-go-sdk
```

## Auth

API key from https://cloud.vast.ai/account/ (sent as `Authorization: Bearer`).

```go
client, err := vast.NewClient(os.Getenv("VAST_API_KEY"))
```

Options: `WithTimeout`, `WithMaxRetryAttempts`, `WithRetryDelay`, `WithDebug`,
`WithLogger`, `WithHTTPClient`, `WithBaseURL`, `WithUserAgent`.

## The four verbs

```go
// 1. Search (read-only, free). Cheapest first by default.
offers, err := client.SearchOffers(ctx, &vast.OfferFilter{
    GPUName:        "RTX 3090",       // or GPUNames for a fallback set
    NumGPUs:        1,
    MinReliability: 0.98,
    Verified:       vast.Bool(true),
    MaxDPHTotal:    0.20,
    MinDiskGB:      30,
    MinCUDA:        12.4,
})

// 2. Create — accepts the offer (an "ask"). On-demand unless BidPricePerHour set.
resp, err := client.CreateInstance(ctx, offers[0].ID, &vast.CreateInstanceRequest{
    Image:   "ghcr.io/cozy-creator/cell-producer:0.3.1",
    Env:     map[string]string{"SESSION_SPEC_URL": presignedURL},
    Onstart: bootstrap, // <= 4048 chars; gzip+base64 anything larger
    DiskGB:  32,
    Label:   "forge-session-42",
})

// 3. Poll.
inst, err := client.WaitForInstanceRunning(ctx, resp.InstanceID, 10*time.Second)
// or: client.GetInstance / client.ListInstances (walks the 25/page keyset pagination)

// 4. Destroy — the ONLY call that stops billing (stopped instances still bill storage).
err = client.DestroyInstance(ctx, resp.InstanceID)
```

Balance for spend guardrails: `client.Balance(ctx)`.

## Offer-filter cookbook

| Goal | Filter |
|---|---|
| Trustworthy host | `Verified: vast.Bool(true), MinReliability: 0.98` |
| Datacenter boxes only | `Datacenter: vast.Bool(true)` (consumer cards are mostly non-datacenter) |
| Match our cell stack | `MinCUDA: 12.4` (checks `cuda_max_good`) |
| Any SM89 card ≥ 24GB | `GPUNames: vast.GPUNames(vast.GPUsWithAtLeast(24, 89))` |
| Cheap egress | check `Offer.InetUpCost`/`InetDownCost` ($/GB, billed both directions) |
| Region pin | `Geolocation: []string{"US", "CA"}` |
| Interruptible | `Type: vast.OfferTypeBid` + `BidPricePerHour` on create — outbid ⇒ PAUSED, not killed; unsuitable for run-to-completion |

Offers are perishable: an id can be rented out from under you between search
and create. That returns an error matching `vast.ErrOfferGone` — re-search and
take the next offer, never retry the same id.

## Billing evidence

`GetInstanceCharges(ctx, instanceID, start, end)` reads vast's own charges of one
instance (`GET /api/v0/charges/`) over the UTC days `[start, end)` touches: one
record per contract row, its total of gpu, disk and bandwidth, in signed integer
USD micros decoded from vast's decimal text (never `float64`). vast cannot filter
charges by instance, so the read walks every instance contract of those days and
returns an error unless the walk is complete. `RawResponse` is the instance's rows
byte-for-byte; an instance missing from the listing reads empty, which is not
proof of zero cost. Malformed, sub-micro, overflowing or oversized answers are a
typed `*ChargesEvidenceError` carrying the query and the bytes refused.

## Lifecycle states

`Instance.ActualStatus`: `loading → running`. Per vast docs, `exited`,
`offline`, and `unknown` are dead ends (`IsTerminal()`) — destroy and re-rent.
`StatusMsg` carries pull/docker errors for stuck instances.

## Error taxonomy

`errors.Is`-matchable sentinels consumed by backoff logic: `ErrOfferGone`,
`ErrInsufficientCredit`, `ErrRateLimited` (Retry-After honored),
`ErrUnauthorized`, `ErrNotFound`. `*APIError` exposes `StatusCode`/`Code`.
Retries: 429 always; 5xx only on idempotent calls — `CreateInstance` is never
replayed (double-rent hazard), callers own create-retry policy.

## The SKU-slug bridge

Cells are keyed by the compilecache slug of the CUDA **device** name
(`gen_worker.compile_cache.sku_slug` / tensorhub `compilecache.SKUSlug`).
vast's marketing names don't always slugify to that ("RTX 4070S" ⇒ device
"RTX 4070 SUPER" ⇒ `rtx-4070-super`; "H100 SXM" ⇒ `h100-80gb-hbm3`), so the
static catalog carries explicit slugs both ways:

```go
spec, ok := vast.GPUSpecByName("RTX 4070S") // spec.Slug == "rtx-4070-super"
name, ok := vast.GPUNameForSlug("rtx-3090") // "RTX 3090" — desired cell -> offer filter
slug := offer.SKUSlug()                     // catalog-first, SKUSlug fallback
vast.SKUSlug("NVIDIA H100 80GB HBM3")       // byte-compatible slugifier for device names
```

Catalog order is fallback preference (cheapest/most-liquid first), mirroring
runpod-go-sdk's `GPUSpec` catalog so SKU pinning is identical across providers.

## Testing

```bash
go test ./...                                 # unit tests, httptest fixtures, no credentials
VAST_API_KEY=... go test -tags live ./...     # + live read-only smoke (search/balance/list)
VAST_API_KEY=... VAST_LIVE_RENT=1 go test -tags live -run TestLiveRentDestroy ./...
                                              # rents the cheapest verified 3090, destroys it (~$0.01)
```

## License

MIT
