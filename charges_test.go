package vast_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	vast "github.com/cozy-creator/vast-ai-go-sdk"
)

// chargeRow is one contract row as vast writes it (Python json.dumps spacing).
func chargeRow(id int64, day int64, amount string, items ...string) string {
	return fmt.Sprintf(`{"start": %d, "end": %d, "type": "instance", "source": "instance-%d", "description": "Instance %d Charges - 1 day", "amount": %s, "metadata": {"label": "pr-%d"}, "items": [%s]}`,
		day, day, id, id, amount, id, strings.Join(items, ", "))
}

func chargeItem(kind, desc, amount string) string {
	return fmt.Sprintf(`{"start": 0, "end": 0, "type": %q, "source": null, "description": %q, "amount": %s, "metadata": {}, "items": []}`, kind, desc, amount)
}

// chargesPeer serves scripted pages: page i answers after_token "t<i>".
type chargesPeer struct {
	pages   []string
	queries []url.Values
}

func (p *chargesPeer) serve(t *testing.T) *vast.Client {
	ts := newTestServer(t)
	ts.mux.HandleFunc("/api/v0/charges/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p.queries = append(p.queries, q)
		i := 0
		if tok := q.Get("after_token"); tok != "" {
			fmt.Sscanf(tok, "t%d", &i)
		}
		if i >= len(p.pages) {
			http.Error(w, "unscripted page", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(p.pages[i]))
	})
	return ts.client(t)
}

func page(total int, next string, rows ...string) string {
	tok := "null"
	if next != "" {
		tok = fmt.Sprintf("%q", next)
	}
	return fmt.Sprintf(`{"success": true, "count": %d, "total": %d, "next_token": %s, "results": [%s]}`,
		len(rows), total, tok, strings.Join(rows, ", "))
}

const day = int64(1791504000) // 2026-10-09T00:00:00Z

var (
	lifeStart = time.Date(2026, 10, 9, 17, 0, 0, 0, time.UTC)
	lifeEnd   = time.Date(2026, 10, 9, 21, 12, 5, 123456000, time.UTC)
)

func TestInstanceChargesWalksEveryPageAndKeepsOnlyTheInstanceRowsExactly(t *testing.T) {
	mine := chargeRow(55062389, day, "0.333",
		chargeItem("gpu", "0.202 hours at $0.533/hour", "0.107"), chargeItem("disk", "0.218 hours at $0.056/hour", "0.012"),
		chargeItem("bwd", "79.1 GB Downloaded at $0.003/GB", "0.206"), chargeItem("bwu", "1.9 GB Uploaded at $0.004/GB", "0.008"))
	p := &chargesPeer{pages: []string{
		page(4, "t1", chargeRow(55049822, day, "0.514"), chargeRow(55050000, day, "20.342")),
		page(4, "", mine, chargeRow(55065993, day, "0.0")),
	}}
	got, err := p.serve(t).GetInstanceCharges(context.Background(), 55062389, lifeStart, lifeEnd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.RawResponse) != "["+mine+"]" {
		t.Fatalf("raw is not the instance's row verbatim:\n%s", got.RawResponse)
	}
	want := `format=table&latest_first=false&limit=500&select_filters=%7B%22day%22%3A%7B%22gte%22%3A1791504000%2C%22lte%22%3A1791590399%7D%2C%22type%22%3A%7B%22in%22%3A%5B%22instance%22%5D%7D%7D`
	if got.NormalizedQuery != want {
		t.Fatalf("query %s", got.NormalizedQuery)
	}
	if len(p.queries) != 2 || p.queries[0].Get("after_token") != "" || p.queries[1].Get("after_token") != "t1" ||
		p.queries[1].Get("select_filters") != p.queries[0].Get("select_filters") {
		t.Fatalf("walk %v", p.queries)
	}
	if got.TotalAmountUSDMicros != 333_000 || len(got.Records) != 1 {
		t.Fatalf("%+v", got)
	}
	r := got.Records[0]
	if r.InstanceID != 55062389 || !r.Start.Equal(time.Unix(day, 0)) || r.Label != "pr-55062389" || r.AmountUSDMicros != 333_000 {
		t.Fatalf("%+v", r)
	}
}

func TestInstanceChargesDays(t *testing.T) {
	for _, c := range []struct {
		start, end time.Time
		gte, lte   int64
	}{
		{lifeStart, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), day, day + 86399},
		{lifeStart, time.Date(2026, 10, 10, 0, 0, 1, 0, time.UTC), day, day + 2*86400 - 1},
		{time.Date(2026, 10, 8, 23, 59, 59, 0, time.UTC), lifeEnd, day - 86400, day + 86399},
	} {
		p := &chargesPeer{pages: []string{page(0, "")}}
		if _, err := p.serve(t).GetInstanceCharges(context.Background(), 1, c.start, c.end); err != nil {
			t.Fatal(err)
		}
		if f := p.queries[0].Get("select_filters"); f != fmt.Sprintf(`{"day":{"gte":%d,"lte":%d},"type":{"in":["instance"]}}`, c.gte, c.lte) {
			t.Fatalf("%s..%s: %s", c.start, c.end, f)
		}
	}
}

func TestInstanceChargesAbsentAndZeroAreDifferentEvidence(t *testing.T) {
	p := &chargesPeer{pages: []string{page(1, "", chargeRow(7, day, "1.25"))}}
	absent, err := p.serve(t).GetInstanceCharges(context.Background(), 8, lifeStart, lifeEnd)
	if err != nil || string(absent.RawResponse) != "[]" || len(absent.Records) != 0 || absent.TotalAmountUSDMicros != 0 {
		t.Fatalf("absent: %+v %v", absent, err)
	}
	p = &chargesPeer{pages: []string{page(1, "", chargeRow(8, day, "0.0"))}}
	zero, err := p.serve(t).GetInstanceCharges(context.Background(), 8, lifeStart, lifeEnd)
	if err != nil || len(zero.Records) != 1 || zero.Records[0].AmountUSDMicros != 0 {
		t.Fatalf("zero: %+v %v", zero, err)
	}
}

func TestInstanceChargesKeepsSignAndExactness(t *testing.T) {
	for amount, micros := range map[string]int64{"-0.5": -500_000, "31.173": 31_173_000, "1e-3": 1_000, "0.000001": 1, "12345678.9": 12_345_678_900_000, `"0.5"`: 500_000} {
		p := &chargesPeer{pages: []string{page(1, "", chargeRow(8, day, amount))}}
		got, err := p.serve(t).GetInstanceCharges(context.Background(), 8, lifeStart, lifeEnd)
		if err != nil || got.TotalAmountUSDMicros != micros {
			t.Fatalf("%s: %+v %v", amount, got, err)
		}
	}
}

func TestInstanceChargesRefusals(t *testing.T) {
	malformed := `{"start": 1791504000, "end": 1791504000, "type": "instance", "source": "instance-8", "items": []}`
	for _, c := range []struct {
		name string
		page string
		kind vast.ChargesEvidenceErrorKind
		raw  string // "" = the page itself
	}{
		{"sub-micro", page(1, "", chargeRow(8, day, "0.0000001")), vast.ChargesEvidenceSubmicroAmount, "[" + chargeRow(8, day, "0.0000001") + "]"},
		{"overflow", page(1, "", chargeRow(8, day, "10000000000000")), vast.ChargesEvidenceAmountOverflow, "[" + chargeRow(8, day, "10000000000000") + "]"},
		{"total overflow", page(2, "", chargeRow(8, day, "9000000000000"), chargeRow(8, day+86400, "9000000000000")),
			vast.ChargesEvidenceAmountOverflow, "[" + chargeRow(8, day, "9000000000000") + "," + chargeRow(8, day+86400, "9000000000000") + "]"},
		{"quoted non-decimal", page(1, "", chargeRow(8, day, `"0.5 USD"`)), vast.ChargesEvidenceSchemaAmbiguity, "[" + chargeRow(8, day, `"0.5 USD"`) + "]"},
		{"quoted sub-micro", page(1, "", chargeRow(8, day, `"0.0000001"`)), vast.ChargesEvidenceSubmicroAmount, "[" + chargeRow(8, day, `"0.0000001"`) + "]"},
		{"no amount", page(1, "", malformed), vast.ChargesEvidenceSchemaAmbiguity, "[" + malformed + "]"},
		{"repeated day", page(2, "", chargeRow(8, day, "1"), chargeRow(8, day, "1")), vast.ChargesEvidenceSchemaAmbiguity,
			"[" + chargeRow(8, day, "1") + "," + chargeRow(8, day, "1") + "]"},
		{"no total", `{"success": true, "count": 0, "results": []}`, vast.ChargesEvidenceSchemaAmbiguity, ""},
		{"count disagrees", `{"success": true, "count": 2, "total": 2, "next_token": null, "results": []}`, vast.ChargesEvidenceSchemaAmbiguity, ""},
		{"foreign row without source", page(1, "", `{"amount": 1}`), vast.ChargesEvidenceSchemaAmbiguity, ""},
		{"not json", `<html>`, vast.ChargesEvidenceSchemaAmbiguity, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &chargesPeer{pages: []string{c.page}}
			_, err := p.serve(t).GetInstanceCharges(context.Background(), 8, lifeStart, lifeEnd)
			var refused *vast.ChargesEvidenceError
			if !errors.As(err, &refused) || refused.Kind != c.kind || refused.NormalizedQuery == "" {
				t.Fatalf("%v", err)
			}
			raw := c.raw
			if raw == "" {
				raw = c.page
			}
			if string(refused.RawResponse) != raw {
				t.Fatalf("raw %s", refused.RawResponse)
			}
		})
	}
}

// A walk that does not add up produced no evidence: a plain error the caller retries.
func TestInstanceChargesIncompleteWalkIsNoEvidence(t *testing.T) {
	for name, pages := range map[string][]string{
		"total fell":            {page(3, "t1", chargeRow(7, day, "1")), page(2, "", chargeRow(8, day, "1"))},
		"short":                 {page(3, "t1", chargeRow(7, day, "1")), page(3, "", chargeRow(8, day, "1"))},
		"earlier contract grew": {page(2, "t1", chargeRow(7, day, "1")), page(3, "", chargeRow(8, day, "1"))},
		"out of order":          {page(2, "t1", chargeRow(9, day, "1")), page(2, "", chargeRow(8, day, "1"))},
		"empty, continuing":     {page(1, "t1"), page(1, "", chargeRow(8, day, "1"))},
		"stalled":               {page(3, "t1", chargeRow(7, day, "1")), page(3, "t1", chargeRow(8, day, "1"))},
	} {
		t.Run(name, func(t *testing.T) {
			p := &chargesPeer{pages: pages}
			got, err := p.serve(t).GetInstanceCharges(context.Background(), 8, lifeStart, lifeEnd)
			var refused *vast.ChargesEvidenceError
			if err == nil || errors.As(err, &refused) {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

// A contract created during the walk has a higher id and lands on a later page: the total
// grows and the walk still counts every contract.
func TestInstanceChargesCountsAContractCreatedDuringTheWalk(t *testing.T) {
	p := &chargesPeer{pages: []string{page(2, "t1", chargeRow(7, day, "1")), page(3, "", chargeRow(8, day, "2"), chargeRow(9, day, "1"))}}
	got, err := p.serve(t).GetInstanceCharges(context.Background(), 8, lifeStart, lifeEnd)
	if err != nil || got.TotalAmountUSDMicros != 2_000_000 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestInstanceChargesTooLargeKeepsNoBody(t *testing.T) {
	big := page(1, "", chargeRow(8, day, "1", chargeItem("gpu", strings.Repeat("x", 17<<20), "1")))
	p := &chargesPeer{pages: []string{big}}
	_, err := p.serve(t).GetInstanceCharges(context.Background(), 8, lifeStart, lifeEnd)
	var refused *vast.ChargesEvidenceError
	if !errors.As(err, &refused) || refused.Kind != vast.ChargesEvidenceResponseTooLarge || len(refused.RawResponse) != 0 {
		t.Fatalf("%v", err)
	}
	var endless []string
	for i := range 65 {
		endless = append(endless, page(65, fmt.Sprintf("t%d", i+1), chargeRow(int64(i+1), day, "1")))
	}
	p = &chargesPeer{pages: endless}
	if _, err := p.serve(t).GetInstanceCharges(context.Background(), 8, lifeStart, lifeEnd); !errors.As(err, &refused) ||
		refused.Kind != vast.ChargesEvidenceResponseTooLarge || len(p.queries) != 64 {
		t.Fatalf("%d pages: %v", len(p.queries), err)
	}
}

func TestInstanceChargesValidatesItsQuery(t *testing.T) {
	c := (&chargesPeer{}).serve(t)
	for _, q := range []struct {
		id         int64
		start, end time.Time
	}{{0, lifeStart, lifeEnd}, {8, lifeStart.Local().In(time.FixedZone("x", 3600)), lifeEnd}, {8, lifeEnd, lifeStart}, {8, time.Time{}, lifeEnd}} {
		var invalid *vast.ValidationError
		if _, err := c.GetInstanceCharges(context.Background(), q.id, q.start, q.end); !errors.As(err, &invalid) {
			t.Fatalf("%+v: %v", q, err)
		}
	}
}
