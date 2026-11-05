// Package foodapi searches USDA FoodData Central and Open Food Facts and
// normalizes their foods to per-100 g values.
package foodapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.icco.me/foods/internal/nutrition"
)

// Source names an outside food database.
type Source string

// The outside food databases.
const (
	USDA Source = "usda"
	OFF  Source = "off"
)

// Sources lists every outside database, in the order results interleave.
var Sources = []Source{USDA, OFF}

// Label is the human name of a source.
func (s Source) Label() string {
	if s == USDA {
		return "USDA"
	}
	return "Open Food Facts"
}

// Food is a food from an outside database, not yet saved.
type Food struct {
	Source     Source
	ExternalID string
	Name       string
	Brand      *string
	ServingG   *float64
	Per100     nutrition.Values
}

// ErrUnavailable wraps every failure to reach an outside database.
var ErrUnavailable = errors.New("food database unavailable")

const userAgent = "foods/1.0 (+https://github.com/icco/foods)"

// Client talks to both databases.
type Client struct {
	HTTP        *http.Client
	USDAKey     string
	USDABase    string
	OFFSearch   string
	OFFProducts string
}

// New returns a Client for the public endpoints.
func New(usdaKey string) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 6 * time.Second},
		USDAKey:     usdaKey,
		USDABase:    "https://api.nal.usda.gov/fdc/v1",
		OFFSearch:   "https://search.openfoodfacts.org/search",
		OFFProducts: "https://world.openfoodfacts.org/api/v2/product",
	}
}

// Search returns up to limit foods matching query from one source.
func (c *Client) Search(ctx context.Context, s Source, query string, limit int) ([]Food, error) {
	size := strconv.Itoa(min(limit*3, 50))
	var raw []json.RawMessage
	var norm func(json.RawMessage) *Food
	if s == USDA {
		var body struct{ Foods []json.RawMessage }
		q := url.Values{"query": {query}, "pageSize": {size}}
		if err := c.get(ctx, s, c.USDABase+"/foods/search?"+q.Encode(), &body); err != nil {
			return nil, err
		}
		raw, norm = body.Foods, normalizeUSDA
	} else {
		var body struct{ Hits []json.RawMessage }
		q := url.Values{"q": {query}, "page_size": {size}, "fields": {offFields}}
		if err := c.get(ctx, s, c.OFFSearch+"?"+q.Encode(), &body); err != nil {
			return nil, err
		}
		raw, norm = body.Hits, normalizeOFF
	}
	out := []Food{}
	for _, r := range raw {
		if f := norm(r); f != nil && f.ExternalID != "" && len(out) < limit {
			out = append(out, *f)
		}
	}
	return out, nil
}

// Get returns one food by id, or nil if the source has no usable food there.
func (c *Client) Get(ctx context.Context, s Source, id string) (*Food, error) {
	if s == USDA {
		var body json.RawMessage
		if err := c.get(ctx, s, c.USDABase+"/food/"+url.PathEscape(id), &body); err != nil || body == nil {
			return nil, err
		}
		return normalizeUSDA(body), nil
	}
	var body struct {
		Status  int             `json:"status"`
		Product json.RawMessage `json:"product"`
	}
	if err := c.get(ctx, s, c.OFFProducts+"/"+url.PathEscape(id)+".json?fields="+offFields, &body); err != nil {
		return nil, err
	}
	if body.Product == nil {
		return nil, nil
	}
	f := normalizeOFF(body.Product)
	if f != nil && f.ExternalID == "" {
		f.ExternalID = id
	}
	return f, nil
}

// get decodes a JSON body into out. A 404 leaves out untouched.
func (c *Client) get(ctx context.Context, s Source, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if s == USDA {
		req.Header.Set("X-Api-Key", c.USDAKey)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s request failed: %w", ErrUnavailable, s.Label(), err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s returned HTTP %d", ErrUnavailable, s.Label(), res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: %s sent a bad response: %w", ErrUnavailable, s.Label(), err)
	}
	return nil
}

// num reads a JSON number or numeric string.
func num(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return &f
		}
	}
	return nil
}

func strp(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
