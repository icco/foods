package foodapi

import (
	"encoding/json"
	"strconv"
	"strings"

	"go.icco.me/foods/internal/nutrition"
)

// USDA nutrient ids. Some Foundation foods only carry the Atwater energy values 2047 or 2048.
var (
	usdaKcal    = []int{1008, 2047, 2048}
	usdaProtein = []int{1003}
	usdaFat     = []int{1004}
	usdaCarbs   = []int{1005}
	usdaFiber   = []int{1079}
	usdaSodium  = []int{1093}
)

type usdaNutrient struct {
	NutrientID int      `json:"nutrientId"`
	Value      *float64 `json:"value"`
	Amount     *float64 `json:"amount"`
	Nutrient   struct {
		ID int `json:"id"`
	} `json:"nutrient"`
}

type usdaFood struct {
	FdcID           *int64         `json:"fdcId"`
	Description     string         `json:"description"`
	BrandName       string         `json:"brandName"`
	BrandOwner      string         `json:"brandOwner"`
	ServingSize     float64        `json:"servingSize"`
	ServingSizeUnit string         `json:"servingSizeUnit"`
	FoodNutrients   []usdaNutrient `json:"foodNutrients"`
}

// usdaAmount finds a nutrient. Search results use nutrientId/value; the detail
// endpoint uses nutrient.id/amount. Both are per 100 g.
func usdaAmount(ns []usdaNutrient, ids []int) *float64 {
	for _, id := range ids {
		for _, n := range ns {
			if n.NutrientID != id && n.Nutrient.ID != id {
				continue
			}
			if n.Value != nil {
				return n.Value
			}
			if n.Amount != nil {
				return n.Amount
			}
		}
	}
	return nil
}

func normalizeUSDA(raw json.RawMessage) *Food {
	var f usdaFood
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	kcal, protein := usdaAmount(f.FoodNutrients, usdaKcal), usdaAmount(f.FoodNutrients, usdaProtein)
	fat, carbs := usdaAmount(f.FoodNutrients, usdaFat), usdaAmount(f.FoodNutrients, usdaCarbs)
	if f.FdcID == nil || strings.TrimSpace(f.Description) == "" || kcal == nil || protein == nil || fat == nil || carbs == nil {
		return nil
	}
	out := &Food{
		Source:     USDA,
		ExternalID: strconv.FormatInt(*f.FdcID, 10),
		Name:       strings.TrimSpace(f.Description),
		Brand:      strp(f.BrandName),
		Per100: nutrition.Values{
			Kcal: *kcal, ProteinG: *protein, FatG: *fat, CarbsG: *carbs,
			FiberG: usdaAmount(f.FoodNutrients, usdaFiber), SodiumMg: usdaAmount(f.FoodNutrients, usdaSodium),
		},
	}
	if out.Brand == nil {
		out.Brand = strp(f.BrandOwner)
	}
	if strings.EqualFold(f.ServingSizeUnit, "g") && f.ServingSize > 0 {
		out.ServingG = &f.ServingSize
	}
	return out
}

const offFields = "code,product_name,brands,serving_quantity,nutriments"

type offProduct struct {
	Code            any            `json:"code"`
	ProductName     string         `json:"product_name"`
	Brands          any            `json:"brands"`
	ServingQuantity any            `json:"serving_quantity"`
	Nutriments      map[string]any `json:"nutriments"`
}

func offBrand(b any) *string {
	switch x := b.(type) {
	case []any:
		if len(x) > 0 {
			s, _ := x[0].(string)
			return strp(s)
		}
	case string:
		return strp(strings.Split(x, ",")[0])
	}
	return nil
}

// normalizeOFF reads an Open Food Facts product. Energy may be kJ only, and
// sodium is in grams; with only salt given, sodium is salt / 2.5.
func normalizeOFF(raw json.RawMessage) *Food {
	var p offProduct
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	n := p.Nutriments
	kcal := num(n["energy-kcal_100g"])
	if kcal == nil {
		if kj := num(n["energy_100g"]); kj != nil {
			v := *kj / 4.184
			kcal = &v
		}
	}
	protein, fat, carbs := num(n["proteins_100g"]), num(n["fat_100g"]), num(n["carbohydrates_100g"])
	code := ""
	switch c := p.Code.(type) {
	case string:
		code = c
	case float64:
		code = strconv.FormatFloat(c, 'f', -1, 64)
	}
	name := strings.TrimSpace(p.ProductName)
	if name == "" || kcal == nil || protein == nil || fat == nil || carbs == nil {
		return nil
	}
	sodiumG := num(n["sodium_100g"])
	if sodiumG == nil {
		if salt := num(n["salt_100g"]); salt != nil {
			v := *salt / 2.5
			sodiumG = &v
		}
	}
	var sodiumMg *float64
	if sodiumG != nil {
		v := *sodiumG * 1000
		sodiumMg = &v
	}
	out := &Food{
		Source:     OFF,
		ExternalID: code,
		Name:       name,
		Brand:      offBrand(p.Brands),
		Per100:     nutrition.Values{Kcal: *kcal, ProteinG: *protein, FatG: *fat, CarbsG: *carbs, FiberG: num(n["fiber_100g"]), SodiumMg: sodiumMg},
	}
	if s := num(p.ServingQuantity); s != nil && *s > 0 {
		out.ServingG = s
	}
	return out
}
