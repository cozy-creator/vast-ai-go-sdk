package vast

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	chargesPath      = "/api/v0/charges/"
	chargesPageLimit = 500
	// maxChargesResponseBytes bounds every page of one read together.
	maxChargesResponseBytes = 16 << 20
	maxChargesPages         = 64
	secondsPerDay           = 24 * 60 * 60
	usdMicrosPerUSD         = 1_000_000
)

// InstanceCharge is vast's charge of one instance contract over the queried
// UTC days: Start and End are the first and last day it covers, and the amount
// is vast's total of its gpu, disk and bandwidth items (kept verbatim in the raw
// row). Money is signed integer USD micros decoded from vast's decimal text,
// never float64.
type InstanceCharge struct {
	InstanceID      int64
	Start, End      time.Time
	Label           string
	AmountUSDMicros int64
}

// InstanceCharges is the evidence of one billing read. NormalizedQuery is the
// URL-encoded query of the walk's first page (later pages add after_token).
// RawResponse is the JSON array of the instance's contract rows, each
// byte-for-byte as vast sent it; the rest of the account's listing is not
// evidence about this instance and is not kept. Treat both as immutable.
type InstanceCharges struct {
	NormalizedQuery      string
	RawResponse          []byte
	Records              []InstanceCharge
	TotalAmountUSDMicros int64
}

// ChargesEvidenceErrorKind is a stable wire-refusal class callers persist.
type ChargesEvidenceErrorKind string

const (
	ChargesEvidenceSchemaAmbiguity  ChargesEvidenceErrorKind = "schema_ambiguity"
	ChargesEvidenceSubmicroAmount   ChargesEvidenceErrorKind = "submicro_amount"
	ChargesEvidenceAmountOverflow   ChargesEvidenceErrorKind = "amount_overflow"
	ChargesEvidenceResponseTooLarge ChargesEvidenceErrorKind = "response_too_large"
)

// ChargesEvidenceError is a response that cannot become typed charges. It
// keeps the normalized query and the bounded bytes refused: the instance's
// rows when one of them is malformed, the page when the page is.
// RawResponse is empty for ResponseTooLarge: a partial body is not evidence.
type ChargesEvidenceError struct {
	Kind            ChargesEvidenceErrorKind
	NormalizedQuery string
	RawResponse     []byte

	cause error
}

func (e *ChargesEvidenceError) Error() string {
	return fmt.Sprintf("vast: instance charges refused (%s): %v", e.Kind, e.cause)
}

func (e *ChargesEvidenceError) Unwrap() error { return e.cause }

// GetInstanceCharges reads vast's charges of one instance over the UTC days
// that [start, end) touches. vast cannot filter charges by instance, so the
// read walks every instance contract of those days and keeps the instance's
// rows. A walk that is not complete and consistent (counts that do not add up
// to the final total, contracts out of order, a total that fell, pages that do
// not advance) is a plain error: it produced no evidence. A contract created
// during the walk has a higher id, so it lands on a later page. An instance absent from a complete
// walk is an empty read, which is not proof of zero cost.
func (c *Client) GetInstanceCharges(ctx context.Context, instanceID int64, start, end time.Time) (*InstanceCharges, error) {
	switch {
	case instanceID <= 0:
		return nil, &ValidationError{Field: "instanceID", Message: "must be positive"}
	case start.IsZero() || !isUTC(start):
		return nil, &ValidationError{Field: "start", Message: "must be a nonzero UTC time"}
	case end.IsZero() || !isUTC(end):
		return nil, &ValidationError{Field: "end", Message: "must be a nonzero UTC time"}
	case !end.After(start):
		return nil, &ValidationError{Field: "end", Message: "must be after start"}
	}
	first := floorDay(start.Unix())
	last := floorDay(end.Add(-time.Nanosecond).Unix()) + secondsPerDay - 1
	q := url.Values{}
	q.Set("format", "table")
	q.Set("latest_first", "false")
	q.Set("limit", strconv.Itoa(chargesPageLimit))
	q.Set("select_filters", fmt.Sprintf(`{"day":{"gte":%d,"lte":%d},"type":{"in":["instance"]}}`, first, last))
	query := q.Encode()

	refuse := func(kind ChargesEvidenceErrorKind, raw []byte, format string, args ...any) error {
		return &ChargesEvidenceError{Kind: kind, NormalizedQuery: query,
			RawResponse: append([]byte{}, raw...), cause: fmt.Errorf(format, args...)}
	}
	var rows []json.RawMessage
	var token string
	var total, counted, budget = 0, 0, maxChargesResponseBytes
	lastID := int64(0)
	for page := 0; ; page++ {
		if page == maxChargesPages || budget <= 0 {
			return nil, refuse(ChargesEvidenceResponseTooLarge, nil, "walk exceeds %d pages or %d bytes", maxChargesPages, maxChargesResponseBytes)
		}
		path := chargesPath + "?" + query
		if token != "" {
			path += "&" + url.Values{"after_token": {token}}.Encode()
		}
		body, err := c.send(ctx, http.MethodGet, path, nil, true, budget)
		if errors.Is(err, errResponseTooLarge) {
			return nil, refuse(ChargesEvidenceResponseTooLarge, nil, "walk exceeds %d bytes", maxChargesResponseBytes)
		}
		if err != nil {
			return nil, fmt.Errorf("vast: get instance charges: %w", err)
		}
		budget -= len(body)
		var env struct {
			Count     *int               `json:"count"`
			Total     *int               `json:"total"`
			NextToken *string            `json:"next_token"`
			Results   *[]json.RawMessage `json:"results"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, refuse(ChargesEvidenceSchemaAmbiguity, body, "page %d: %v", page, err)
		}
		if env.Count == nil || env.Total == nil || env.Results == nil || *env.Count != len(*env.Results) {
			return nil, refuse(ChargesEvidenceSchemaAmbiguity, body, "page %d lacks a consistent count, total and results", page)
		}
		if *env.Total < total {
			return nil, fmt.Errorf("vast: instance charges total fell during the walk: %d, then %d", total, *env.Total)
		}
		total = *env.Total
		counted += len(*env.Results)
		for i, raw := range *env.Results {
			var row struct {
				Source *string `json:"source"`
			}
			id, ok := int64(0), false
			if json.Unmarshal(raw, &row) == nil && row.Source != nil {
				id, ok = instanceSource(*row.Source)
			}
			if !ok {
				return nil, refuse(ChargesEvidenceSchemaAmbiguity, body, "page %d row %d names no instance contract", page, i)
			}
			if id < lastID {
				return nil, fmt.Errorf("vast: instance charges out of contract order: %d after %d", id, lastID)
			}
			lastID = id
			if id == instanceID {
				rows = append(rows, raw)
			}
		}
		if env.NextToken == nil || *env.NextToken == "" {
			break
		}
		if len(*env.Results) == 0 || *env.NextToken == token {
			return nil, fmt.Errorf("vast: instance charges page %d does not advance", page)
		}
		token = *env.NextToken
	}
	if counted != total {
		return nil, fmt.Errorf("vast: instance charges walk read %d of %d contracts", counted, total)
	}

	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, r := range rows {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(r)
	}
	buf.WriteByte(']')
	raw := buf.Bytes()
	out := &InstanceCharges{NormalizedQuery: query, RawResponse: raw, Records: []InstanceCharge{}}
	seen := map[int64]bool{}
	for i, r := range rows {
		charge, kind, err := decodeInstanceCharge(r)
		if err != nil {
			return nil, refuse(kind, raw, "instance row %d: %v", i, err)
		}
		if seen[charge.Start.Unix()] {
			return nil, refuse(ChargesEvidenceSchemaAmbiguity, raw, "instance row %d repeats day %s", i, charge.Start.Format(time.DateOnly))
		}
		seen[charge.Start.Unix()] = true
		a := charge.AmountUSDMicros
		if (a > 0 && out.TotalAmountUSDMicros > math.MaxInt64-a) || (a < 0 && out.TotalAmountUSDMicros < math.MinInt64-a) {
			return nil, refuse(ChargesEvidenceAmountOverflow, raw, "instance row %d makes the total overflow", i)
		}
		out.TotalAmountUSDMicros += a
		out.Records = append(out.Records, charge)
	}
	return out, nil
}

var instanceSourcePattern = regexp.MustCompile(`^instance-([1-9][0-9]{0,18})$`)

func instanceSource(source string) (int64, bool) {
	m := instanceSourcePattern.FindStringSubmatch(source)
	if m == nil {
		return 0, false
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	return id, err == nil
}

func decodeInstanceCharge(raw json.RawMessage) (InstanceCharge, ChargesEvidenceErrorKind, error) {
	var row struct {
		Start    *int64          `json:"start"`
		End      *int64          `json:"end"`
		Type     *string         `json:"type"`
		Source   string          `json:"source"`
		Amount   json.RawMessage `json:"amount"`
		Metadata *struct {
			Label *string `json:"label"`
		} `json:"metadata"`
	}
	schema := ChargesEvidenceSchemaAmbiguity
	if err := json.Unmarshal(raw, &row); err != nil {
		return InstanceCharge{}, schema, err
	}
	if row.Type == nil || *row.Type != "instance" {
		return InstanceCharge{}, schema, errors.New("type is not instance")
	}
	if row.Start == nil || row.End == nil || *row.Start <= 0 || *row.End < *row.Start {
		return InstanceCharge{}, schema, errors.New("start and end are not an ordered pair of instants")
	}
	id, _ := instanceSource(row.Source)
	charge := InstanceCharge{InstanceID: id, Start: time.Unix(*row.Start, 0).UTC(), End: time.Unix(*row.End, 0).UTC()}
	if row.Metadata != nil && row.Metadata.Label != nil {
		charge.Label = *row.Metadata.Label
	}
	var err error
	var kind ChargesEvidenceErrorKind
	if charge.AmountUSDMicros, kind, err = usdMicros(row.Amount); err != nil {
		return InstanceCharge{}, kind, fmt.Errorf("amount: %w", err)
	}
	return charge, "", nil
}

// jsonDecimal is a JSON number's text; the exponent is bounded so big.Rat stays small.
var jsonDecimal = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,30})(\.[0-9]{1,30})?([eE][+-]?[0-9]{1,2})?$`)

// usdMicros decodes US dollars exactly, from a JSON number or a quoted decimal.
// vast documents amounts to three decimals, so a sub-micro amount is a wire it
// was never verified against and refuses rather than rounds.
func usdMicros(raw json.RawMessage) (int64, ChargesEvidenceErrorKind, error) {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, `"`) {
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, ChargesEvidenceSchemaAmbiguity, err
		}
	}
	if !jsonDecimal.MatchString(text) {
		return 0, ChargesEvidenceSchemaAmbiguity, fmt.Errorf("%q is not a JSON number", text)
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0, ChargesEvidenceSchemaAmbiguity, fmt.Errorf("%q is not a decimal", text)
	}
	value.Mul(value, big.NewRat(usdMicrosPerUSD, 1))
	if !value.IsInt() {
		return 0, ChargesEvidenceSubmicroAmount, fmt.Errorf("%q has sub-micro precision", text)
	}
	if !value.Num().IsInt64() {
		return 0, ChargesEvidenceAmountOverflow, fmt.Errorf("%q exceeds int64 USD micros", text)
	}
	return value.Num().Int64(), "", nil
}

func floorDay(unix int64) int64 {
	day := unix / secondsPerDay
	if unix < 0 && unix%secondsPerDay != 0 {
		day--
	}
	return day * secondsPerDay
}

func isUTC(t time.Time) bool {
	_, offset := t.Zone()
	return offset == 0
}
