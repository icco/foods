package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"go.icco.me/foods/internal/foodapi"
	"go.icco.me/foods/internal/nutrition"
	"go.icco.me/foods/internal/store"
)

// NutrientsInput are nutrient numbers as the assistant sends them.
type NutrientsInput struct {
	Kcal     float64  `json:"kcal" jsonschema:"Calories (kcal)."`
	ProteinG float64  `json:"protein_g" jsonschema:"Protein in grams."`
	FatG     float64  `json:"fat_g" jsonschema:"Fat in grams."`
	CarbsG   float64  `json:"carbs_g" jsonschema:"Carbohydrates in grams."`
	FiberG   *float64 `json:"fiber_g,omitempty" jsonschema:"Fiber in grams, if known."`
	SodiumMg *float64 `json:"sodium_mg,omitempty" jsonschema:"Sodium in milligrams, if known."`
}

func (n NutrientsInput) values() nutrition.Values {
	return nutrition.Values{Kcal: n.Kcal, ProteinG: n.ProteinG, FatG: n.FatG, CarbsG: n.CarbsG, FiberG: n.FiberG, SodiumMg: n.SodiumMg}
}

// FoodResult is one food in a search: saved (FoodID set) or from outside.
type FoodResult struct {
	FoodID     string
	Source     foodapi.Source
	ExternalID string
	Name       string
	Brand      *string
	Per100     nutrition.Nutrients
}

// SearchInput is the input of search_foods.
type SearchInput struct {
	Query           string `json:"query" jsonschema:"What to look for, for example 'cottage cheese' or 'Fage 2%'."`
	Limit           int    `json:"limit,omitempty" jsonschema:"Most results to return, 1 to 20. Default 5."`
	IncludeExternal *bool  `json:"include_external,omitempty" jsonschema:"false searches only the user's saved foods; default is to also search USDA and Open Food Facts."`
}

// SearchResult is the output of search_foods.
type SearchResult struct {
	Results  []FoodResult
	Warnings []string
}

// plausible rejects outside rows with impossible values; nothing tops about
// 900 kcal per 100 g, which is pure fat.
func plausible(v nutrition.Values) bool {
	return v.Kcal <= 900 && v.ProteinG <= 100 && v.FatG <= 100 && v.CarbsG <= 100 && v.ProteinG+v.FatG+v.CarbsG <= 105
}

// SearchFoods looks in the user's saved foods first, then in USDA and Open
// Food Facts while there are fewer than limit results.
func (s *Service) SearchFoods(ctx context.Context, userID string, in SearchInput) (*SearchResult, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" || len(query) > 200 {
		return nil, fail("query must be 1 to 200 characters.")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 5
	}
	if limit < 1 || limit > 20 {
		return nil, fail("limit must be 1 to 20.")
	}
	local, err := s.Store.SearchFoods(ctx, userID, query, limit)
	if err != nil {
		return nil, err
	}
	out := &SearchResult{}
	for _, f := range local {
		out.Results = append(out.Results, FoodResult{FoodID: f.ID, Source: foodapi.Source(f.Source), Name: f.Name, Brand: f.Brand, Per100: nutrition.Per100(f.Values())})
	}
	if (in.IncludeExternal != nil && !*in.IncludeExternal) || len(local) >= limit {
		return out, nil
	}

	found := make([][]foodapi.Food, len(foodapi.Sources))
	errs := make([]error, len(foodapi.Sources))
	var wg sync.WaitGroup
	for i, src := range foodapi.Sources {
		wg.Go(func() { found[i], errs[i] = s.Foods.Search(ctx, src, query, limit) })
	}
	wg.Wait()
	failed := 0
	for i, err := range errs {
		if err != nil {
			failed++
			out.Warnings = append(out.Warnings, foodapi.Sources[i].Label()+" is unavailable right now.")
		}
	}
	if failed == len(foodapi.Sources) && len(out.Results) == 0 {
		return nil, fail("Could not reach USDA or Open Food Facts. Try again, or add the food from its label.")
	}

	// Take turns between sources so one cannot fill every slot.
	var merged []foodapi.Food
	for i := 0; ; i++ {
		more := false
		for _, list := range found {
			if i < len(list) {
				merged = append(merged, list[i])
				more = true
			}
		}
		if !more {
			break
		}
	}
	hidden := 0
	for _, f := range merged {
		if len(out.Results) >= limit {
			break
		}
		if !plausible(f.Per100) {
			hidden++
			continue
		}
		saved, err := s.Store.GetFoodByExternal(ctx, userID, string(f.Source), f.ExternalID)
		if err != nil {
			return nil, err
		}
		if saved != nil {
			continue
		}
		out.Results = append(out.Results, FoodResult{Source: f.Source, ExternalID: f.ExternalID, Name: f.Name, Brand: f.Brand, Per100: nutrition.Per100(f.Per100)})
	}
	if hidden > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("Hid %d outside result(s) with impossible nutrient values.", hidden))
	}
	return out, nil
}

// RecipeItem is one saved food in a meal.
type RecipeItem struct {
	FoodID string  `json:"food_id" jsonschema:"food_id of a saved food."`
	Grams  float64 `json:"grams" jsonschema:"Grams of it in the meal, 0 to 5000."`
}

// TotalsInput is a figure the user states for a whole meal.
type TotalsInput struct {
	TotalGrams *float64 `json:"total_grams,omitempty" jsonschema:"Weight of the whole meal in grams, if known."`
	Kcal       float64  `json:"kcal" jsonschema:"Calories for the whole meal."`
	ProteinG   *float64 `json:"protein_g,omitempty" jsonschema:"Protein for the whole meal, in grams."`
	FatG       *float64 `json:"fat_g,omitempty" jsonschema:"Fat for the whole meal, in grams."`
	CarbsG     *float64 `json:"carbs_g,omitempty" jsonschema:"Carbs for the whole meal, in grams."`
}

// AddSource is where add_food numbers came from.
type AddSource string

// AddFoodInput is the input of add_food.
type AddFoodInput struct {
	Name       string          `json:"name" jsonschema:"The food's name, for example 'Cottage cheese, 2%'."`
	Source     AddSource       `json:"source,omitempty" jsonschema:"Where the numbers came from: label (a package label) or estimate (a guess or a figure the user stated). Defaults to estimate for totals and label otherwise. Foods from USDA or Open Food Facts are saved with import_food."`
	Brand      *string         `json:"brand,omitempty" jsonschema:"The brand, if any."`
	ServingG   *float64        `json:"serving_g,omitempty" jsonschema:"Grams in one serving. Required with per_serving."`
	Per100G    *NutrientsInput `json:"per_100g,omitempty" jsonschema:"Nutrients in 100 g of the food."`
	PerServing *NutrientsInput `json:"per_serving,omitempty" jsonschema:"Nutrients in one serving (needs serving_g). The server converts it to per 100 g."`
	Items      []RecipeItem    `json:"items,omitempty" jsonschema:"A meal made of saved foods, 1 to 30. The server adds up the nutrients and the meal's weight becomes one serving."`
	Totals     *TotalsInput    `json:"totals,omitempty" jsonschema:"A figure the user states for a whole meal, for example 'about 600 calories'."`
	Notes      *string         `json:"notes,omitempty" jsonschema:"Free-text notes."`
}

// SavedFood is the output of add_food, import_food and update_food.
type SavedFood struct {
	FoodID       string
	Name         string
	ServingG     *float64
	Per100       nutrition.Nutrients
	Archived     bool
	AlreadySaved bool
}

func view(f *store.Food) *SavedFood {
	return &SavedFood{FoodID: f.ID, Name: f.Name, ServingG: f.ServingG, Per100: nutrition.Per100(f.Values()), Archived: f.ArchivedAt != nil}
}

// checkPer100 catches a per-serving number entered as per 100 g.
func checkPer100(v nutrition.Values) error {
	fiber, sodium := 0.0, 0.0
	if v.FiberG != nil {
		fiber = *v.FiberG
	}
	if v.SodiumMg != nil {
		sodium = *v.SodiumMg
	}
	switch {
	case v.Kcal < 0 || v.ProteinG < 0 || v.FatG < 0 || v.CarbsG < 0 || fiber < 0 || sodium < 0:
		return fail("Nutrients cannot be negative.")
	case v.Kcal > 1000:
		return fail("More than 1000 kcal per 100 g is not possible. Are these per-serving values?")
	case v.ProteinG > 100 || v.FatG > 100 || v.CarbsG > 100 || fiber > 100:
		return fail("A nutrient above 100 g per 100 g is not possible.")
	case v.ProteinG+v.FatG+v.CarbsG > 105:
		return fail("Protein, fat and carbs together cannot exceed 100 g per 100 g.")
	case sodium > 40000:
		return fail("Sodium above 40000 mg per 100 g is not possible.")
	}
	return nil
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return "", fail("name must be 1 to 200 characters.")
	}
	return name, nil
}

func scaleValues(v nutrition.Values, k float64) nutrition.Values {
	mul := func(p *float64) *float64 {
		if p == nil {
			return nil
		}
		x := *p * k
		return &x
	}
	return nutrition.Values{Kcal: v.Kcal * k, ProteinG: v.ProteinG * k, FatG: v.FatG * k, CarbsG: v.CarbsG * k, FiberG: mul(v.FiberG), SodiumMg: mul(v.SodiumMg)}
}

func or0(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// AddFood saves a food or a meal from exactly one of per_100g, per_serving,
// items or totals.
func (s *Service) AddFood(ctx context.Context, userID string, in AddFoodInput) (*SavedFood, error) {
	name, err := checkName(in.Name)
	if err != nil {
		return nil, err
	}
	given := 0
	for _, set := range []bool{in.Per100G != nil, in.PerServing != nil, len(in.Items) > 0, in.Totals != nil} {
		if set {
			given++
		}
	}
	if given != 1 {
		return nil, fail("Give exactly one of per_100g, per_serving, items or totals.")
	}
	if in.Source != "" && in.Source != store.SourceLabel && in.Source != store.SourceEstimate {
		return nil, fail("source must be label or estimate. Save USDA and Open Food Facts foods with import_food.")
	}
	if in.ServingG != nil && *in.ServingG <= 0 {
		return nil, fail("serving_g must be greater than 0.")
	}
	f := &store.Food{Name: name, Brand: in.Brand, ServingG: in.ServingG, Notes: in.Notes, Source: string(in.Source)}
	var per100 nutrition.Values
	switch {
	case len(in.Items) > 0:
		if len(in.Items) > 30 {
			return nil, fail("A meal has at most 30 items.")
		}
		ids := make([]string, len(in.Items))
		for i, it := range in.Items {
			ids[i] = it.FoodID
		}
		foods, err := s.Store.GetFoods(ctx, userID, ids)
		if err != nil {
			return nil, err
		}
		var parts []nutrition.Nutrients
		total := 0.0
		for i, it := range in.Items {
			food := foods[it.FoodID]
			if food == nil {
				return nil, fail("No food with that id (items[%d].food_id). Use search_foods to get a food_id.", i)
			}
			if it.Grams <= 0 || it.Grams > 5000 {
				return nil, fail("items[%d].grams must be greater than 0 and at most 5000.", i)
			}
			parts = append(parts, nutrition.Scale(food.Values(), it.Grams))
			total += it.Grams
		}
		n := nutrition.Sum(parts).Times(100 / total)
		per100 = nutrition.Values{Kcal: n.Kcal, ProteinG: n.ProteinG, FatG: n.FatG, CarbsG: n.CarbsG, FiberG: &n.FiberG, SodiumMg: &n.SodiumMg}
		f.ServingG, f.Source = &total, store.SourceRecipe
	case in.Totals != nil:
		t := in.Totals
		weight := 100.0
		if t.TotalGrams != nil {
			if *t.TotalGrams <= 0 {
				return nil, fail("total_grams must be greater than 0.")
			}
			weight = *t.TotalGrams
		}
		if t.Kcal < 0 || or0(t.ProteinG) < 0 || or0(t.FatG) < 0 || or0(t.CarbsG) < 0 {
			return nil, fail("Nutrients cannot be negative.")
		}
		zero := 0.0
		per100 = scaleValues(nutrition.Values{Kcal: t.Kcal, ProteinG: or0(t.ProteinG), FatG: or0(t.FatG), CarbsG: or0(t.CarbsG), FiberG: &zero, SodiumMg: &zero}, 100/weight)
		f.ServingG, f.Source = &weight, store.SourceEstimate
	default:
		if f.Source == "" {
			f.Source = store.SourceLabel
		}
		if in.Per100G != nil {
			per100 = in.Per100G.values()
		} else {
			if in.ServingG == nil {
				return nil, fail("per_serving needs serving_g greater than 0.")
			}
			per100 = scaleValues(in.PerServing.values(), 100 / *in.ServingG)
		}
		if err := checkPer100(per100); err != nil {
			return nil, err
		}
	}
	f.Kcal, f.ProteinG, f.FatG, f.CarbsG, f.FiberG, f.SodiumMg = per100.Kcal, per100.ProteinG, per100.FatG, per100.CarbsG, per100.FiberG, per100.SodiumMg
	if err := s.Store.CreateFood(ctx, userID, f); err != nil {
		return nil, err
	}
	return view(f), nil
}

// ImportFoodInput is the input of import_food.
type ImportFoodInput struct {
	ExternalSource foodapi.Source `json:"external_source" jsonschema:"usda or off (Open Food Facts), as shown by search_foods."`
	ExternalID     string         `json:"external_id" jsonschema:"The external_id shown by search_foods."`
}

// ImportFood copies an outside food into the user's foods. Importing the
// same food twice returns the saved one.
func (s *Service) ImportFood(ctx context.Context, userID string, in ImportFoodInput) (*SavedFood, error) {
	if !slices.Contains(foodapi.Sources, in.ExternalSource) {
		return nil, fail("external_source must be usda or off.")
	}
	if in.ExternalID == "" || len(in.ExternalID) > 100 {
		return nil, fail("external_id must be 1 to 100 characters.")
	}
	existing, err := s.Store.GetFoodByExternal(ctx, userID, string(in.ExternalSource), in.ExternalID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		v := view(existing)
		v.AlreadySaved = true
		return v, nil
	}
	ext, err := s.Foods.Get(ctx, in.ExternalSource, in.ExternalID)
	if errors.Is(err, foodapi.ErrUnavailable) {
		return nil, fail("%s", err.Error())
	}
	if err != nil {
		return nil, err
	}
	if ext == nil {
		return nil, fail("No food with that id at that source, or it is missing calories or macros.")
	}
	if !plausible(ext.Per100) {
		return nil, fail("That entry has impossible nutrient values (bad data at the source). Pick another result, or use add_food with the label values.")
	}
	id := ext.ExternalID
	f := &store.Food{
		Name: ext.Name, Brand: ext.Brand, ServingG: ext.ServingG, Source: string(ext.Source), ExternalID: &id,
		Kcal: ext.Per100.Kcal, ProteinG: ext.Per100.ProteinG, FatG: ext.Per100.FatG, CarbsG: ext.Per100.CarbsG, FiberG: ext.Per100.FiberG, SodiumMg: ext.Per100.SodiumMg,
	}
	if err := s.Store.CreateFood(ctx, userID, f); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return s.ImportFood(ctx, userID, in)
		}
		return nil, err
	}
	return view(f), nil
}

// FoodField is a food field that update_food can remove.
type FoodField string

// UpdateFoodInput is the input of update_food.
type UpdateFoodInput struct {
	FoodID   string          `json:"food_id" jsonschema:"food_id from search_foods."`
	Name     *string         `json:"name,omitempty" jsonschema:"New name."`
	Brand    *string         `json:"brand,omitempty" jsonschema:"New brand."`
	ServingG *float64        `json:"serving_g,omitempty" jsonschema:"Grams in one serving."`
	Notes    *string         `json:"notes,omitempty" jsonschema:"New notes."`
	Archived *bool           `json:"archived,omitempty" jsonschema:"true hides the food from search; false brings it back."`
	Per100G  *NutrientsInput `json:"per_100g,omitempty" jsonschema:"Corrected nutrients per 100 g (replaces all of them)."`
	Clear    []FoodField     `json:"clear,omitempty" jsonschema:"Fields to remove: brand, serving_g or notes."`
}

// UpdateFood fixes or archives a food. Every log of it follows the fix.
func (s *Service) UpdateFood(ctx context.Context, userID string, in UpdateFoodInput) (*SavedFood, error) {
	c := map[string]any{}
	if in.Name != nil {
		name, err := checkName(*in.Name)
		if err != nil {
			return nil, err
		}
		c["name"] = name
	}
	if in.Brand != nil {
		c["brand"] = *in.Brand
	}
	if in.ServingG != nil {
		if *in.ServingG <= 0 {
			return nil, fail("serving_g must be greater than 0.")
		}
		c["serving_g"] = *in.ServingG
	}
	if in.Notes != nil {
		c["notes"] = *in.Notes
	}
	for _, f := range in.Clear {
		if f != "brand" && f != "serving_g" && f != "notes" {
			return nil, fail("clear may list brand, serving_g or notes.")
		}
		c[string(f)] = nil
	}
	if in.Archived != nil {
		if *in.Archived {
			c["archived_at"] = s.Now()
		} else {
			c["archived_at"] = nil
		}
	}
	if in.Per100G != nil {
		v := in.Per100G.values()
		if err := checkPer100(v); err != nil {
			return nil, err
		}
		c["kcal"], c["protein_g"], c["fat_g"], c["carbs_g"], c["fiber_g"], c["sodium_mg"] = v.Kcal, v.ProteinG, v.FatG, v.CarbsG, v.FiberG, v.SodiumMg
	}
	var f store.Food
	if err := s.Store.UpdateRow(ctx, userID, in.FoodID, &f, c); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fail("No food with that id. Use search_foods to get a food_id.")
		}
		return nil, err
	}
	return view(&f), nil
}
