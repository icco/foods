package foodapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const usdaSearch = `{"foods":[
 {"fdcId":111,"description":"Cottage cheese","brandOwner":"Acme Dairy","servingSize":113,"servingSizeUnit":"g",
  "foodNutrients":[{"nutrientId":1008,"value":82},{"nutrientId":1003,"value":11},{"nutrientId":1004,"value":2.3},{"nutrientId":1005,"value":3.4},{"nutrientId":1093,"value":364}]},
 {"fdcId":222,"description":"No calories","foodNutrients":[{"nutrientId":1003,"value":1}]},
 {"fdcId":333,"description":"Atwater only","servingSize":1,"servingSizeUnit":"cup",
  "foodNutrients":[{"nutrientId":2047,"value":50},{"nutrientId":1003,"value":1},{"nutrientId":1004,"value":1},{"nutrientId":1005,"value":10}]}
]}`

const usdaDetail = `{"fdcId":111,"description":"Cottage cheese","brandName":"Acme",
 "foodNutrients":[{"nutrient":{"id":1008},"amount":82},{"nutrient":{"id":1003},"amount":11},{"nutrient":{"id":1004},"amount":2.3},{"nutrient":{"id":1005},"amount":3.4},{"nutrient":{"id":1079},"amount":0}]}`

const offSearch = `{"hits":[
 {"code":"0001","product_name":"Skyr","brands":["Isey"," Other"],"serving_quantity":"150",
  "nutriments":{"energy-kcal_100g":62,"proteins_100g":11,"fat_100g":0.2,"carbohydrates_100g":"4","salt_100g":0.1}},
 {"code":"0002","product_name":"Juice","brands":"Brand A, Brand B",
  "nutriments":{"energy_100g":418.4,"proteins_100g":0,"fat_100g":0,"carbohydrates_100g":10,"sodium_100g":0.01}},
 {"code":"","product_name":"No code","nutriments":{"energy-kcal_100g":1,"proteins_100g":0,"fat_100g":0,"carbohydrates_100g":0}},
 {"code":"0003","product_name":"","nutriments":{}}
]}`

const offProductJSON = `{"status":1,"product":{"product_name":"Skyr","brands":"Isey",
 "nutriments":{"energy-kcal_100g":62,"proteins_100g":11,"fat_100g":0.2,"carbohydrates_100g":4}}}`

func fake(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/usda/foods/search":
			if r.Header.Get("X-Api-Key") != "k" {
				t.Errorf("missing api key")
			}
			_, _ = w.Write([]byte(usdaSearch))
		case "/usda/food/111":
			_, _ = w.Write([]byte(usdaDetail))
		case "/off/search":
			if r.URL.Query().Get("q") == "boom" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(offSearch))
		case "/off/products/0001.json":
			_, _ = w.Write([]byte(offProductJSON))
		case "/off/products/bad.json":
			_, _ = w.Write([]byte(`not json`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := New("k")
	c.USDABase, c.OFFSearch, c.OFFProducts = srv.URL+"/usda", srv.URL+"/off/search", srv.URL+"/off/products"
	return c
}

func TestUSDA(t *testing.T) {
	c := fake(t)
	got, err := c.Search(context.Background(), USDA, "cottage", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d foods, want 2: %+v", len(got), got)
	}
	f := got[0]
	if f.ExternalID != "111" || *f.Brand != "Acme Dairy" || *f.ServingG != 113 || *f.Per100.SodiumMg != 364 || f.Per100.FiberG != nil {
		t.Errorf("first = %+v", f)
	}
	if got[1].Per100.Kcal != 50 || got[1].ServingG != nil {
		t.Errorf("atwater = %+v", got[1])
	}

	one, err := c.Get(context.Background(), USDA, "111")
	if err != nil || one == nil || *one.Brand != "Acme" || *one.Per100.FiberG != 0 {
		t.Fatalf("Get = %+v, %v", one, err)
	}
	missing, err := c.Get(context.Background(), USDA, "999")
	if err != nil || missing != nil {
		t.Fatalf("missing = %+v, %v", missing, err)
	}
}

func TestOFF(t *testing.T) {
	c := fake(t)
	got, err := c.Search(context.Background(), OFF, "skyr", 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("limit: %+v, %v", got, err)
	}
	got, err = c.Search(context.Background(), OFF, "skyr", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if *got[0].Brand != "Isey" || *got[0].ServingG != 150 || *got[0].Per100.SodiumMg != 40 || got[0].Per100.CarbsG != 4 {
		t.Errorf("skyr = %+v", got[0])
	}
	if math.Round(got[1].Per100.Kcal) != 100 || *got[1].Brand != "Brand A" || *got[1].Per100.SodiumMg != 10 {
		t.Errorf("juice = %+v", got[1])
	}

	one, err := c.Get(context.Background(), OFF, "0001")
	if err != nil || one == nil || one.ExternalID != "0001" {
		t.Fatalf("Get = %+v, %v", one, err)
	}
	if none, err := c.Get(context.Background(), OFF, "nope"); err != nil || none != nil {
		t.Fatalf("missing = %+v, %v", none, err)
	}
}

func TestErrors(t *testing.T) {
	c := fake(t)
	if _, err := c.Search(context.Background(), OFF, "boom", 5); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "503") {
		t.Errorf("503: %v", err)
	}
	if _, err := c.Get(context.Background(), OFF, "bad"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("bad json: %v", err)
	}
	c.USDABase = "http://127.0.0.1:1"
	if _, err := c.Search(context.Background(), USDA, "x", 5); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unreachable: %v", err)
	}
	if USDA.Label() != "USDA" || OFF.Label() != "Open Food Facts" {
		t.Error("labels")
	}
}
