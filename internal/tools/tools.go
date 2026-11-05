// Package tools registers the MCP tools. Each one checks who is calling,
// calls a service, and turns the result into short text: assistants read the
// text, so every id and number they need is in it.
package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.icco.me/foods/internal/foodapi"
	"go.icco.me/foods/internal/nutrition"
	"go.icco.me/foods/internal/service"
	"go.icco.me/foods/internal/store"
	"go.icco.me/gutil/logging"
	"go.uber.org/zap"
)

// Instructions tell the assistant how the tools fit together.
const Instructions = `Food and workout log. To log food: search_foods, then import_food for a result marked "not saved", then log_entries with the food_id. ` +
	`Nutrients are always calculated by the server from grams; never do the math yourself. Water: log_entries with water_ml. ` +
	`Call update_settings with the user's time zone before logging if they mention where they are.`

func enum[T ~string](values ...T) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "string"}
	for _, v := range values {
		s.Enum = append(s.Enum, string(v))
	}
	return s
}

var typeSchemas = map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[store.MealType]():       enum(store.MealTypes...),
	reflect.TypeFor[store.WorkoutType]():    enum(store.WorkoutTypes...),
	reflect.TypeFor[foodapi.Source]():       enum(foodapi.Sources...),
	reflect.TypeFor[service.AddSource]():    enum[service.AddSource](store.SourceLabel, store.SourceEstimate),
	reflect.TypeFor[service.FoodField]():    enum[service.FoodField]("brand", "serving_g", "notes"),
	reflect.TypeFor[service.LogField]():     enum[service.LogField]("note", "mindfulness_score"),
	reflect.TypeFor[service.WorkoutField](): enum[service.WorkoutField]("note"),
	reflect.TypeFor[service.TargetField]():  enum[service.TargetField]("kcal", "protein_g", "carbs_g", "fiber_g", "fat_min_g", "fat_max_g", "sodium_max_mg", "water_ml"),
}

func schema[In any]() *jsonschema.Schema {
	s, err := jsonschema.For[In](&jsonschema.ForOptions{TypeSchemas: typeSchemas})
	if err != nil {
		panic(err)
	}
	return s
}

var (
	no  = false
	yes = true
)

var (
	reads        = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &no}
	readsOutside = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &yes}
	adds         = &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no}
	addsOnce     = &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
	changes      = &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
	imports      = &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &yes}
)

// UserID returns the signed-in user of a tool call.
func UserID(req *mcp.CallToolRequest) (string, error) {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.UserID == "" {
		return "", errors.New("no signed-in user")
	}
	return req.Extra.TokenInfo.UserID, nil
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// add registers one tool whose service call returns R and whose reply is
// summary(R). Problems the assistant can fix come back as tool errors;
// anything else is logged and reported without details.
func add[In, R any](srv *mcp.Server, name, title, desc string, ann *mcp.ToolAnnotations, call func(context.Context, string, In) (R, error), summary func(R) string) {
	a := *ann
	a.Title = title
	tool := &mcp.Tool{Name: name, Title: title, Description: desc, Annotations: &a, InputSchema: schema[In]()}
	mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		uid, err := UserID(req)
		if err != nil {
			return nil, nil, err
		}
		r, err := call(ctx, uid, in)
		if err != nil {
			if service.IsUserError(err) {
				res := text(err.Error())
				res.IsError = true
				return res, nil, nil
			}
			logging.FromContext(ctx).Errorw("tool failed", "tool", name, zap.Error(err))
			return nil, nil, errors.New("the server hit an error; try again")
		}
		return text(summary(r)), nil, nil
	})
}

// Register adds every tool to srv.
func Register(srv *mcp.Server, svc *service.Service) {
	add(srv, "search_foods", "Search foods",
		"Find a food by name. Searches the user's saved foods first, then USDA and Open Food Facts. Saved results have a food_id; outside results have external_source and external_id and must be saved with import_food before logging.",
		readsOutside, svc.SearchFoods, searchSummary)
	add(srv, "add_food", "Save a food or meal",
		"Save a food or a meal. Give exactly one of: per_100g, or per_serving together with serving_g (from a label); items (a meal made of foods the user already saved: food_id and grams, 1 to 30; the server adds up the nutrients and the meal's weight becomes one serving); or totals (a figure the user states for a whole meal, \"about 600 calories\", optionally with total_grams). Saved meals are found with search_foods and logged with log_entries, grams or servings.",
		adds, svc.AddFood, func(r *service.SavedFood) string {
			return fmt.Sprintf("Saved %s: %s%s. food_id=%s", r.Name, per100(r.Per100), serving(r.ServingG), r.FoodID)
		})
	add(srv, "import_food", "Save a found food",
		"Save a food found by search_foods from USDA or Open Food Facts into the user's foods, using its external_source and external_id. Importing twice returns the existing food.",
		imports, svc.ImportFood, func(r *service.SavedFood) string {
			verb := "Saved"
			if r.AlreadySaved {
				verb = "Already saved"
			}
			return fmt.Sprintf("%s: %s: %s%s. food_id=%s", verb, r.Name, per100(r.Per100), serving(r.ServingG), r.FoodID)
		})
	add(srv, "update_food", "Fix or archive a food",
		"Fix or archive a saved food or meal (food_id from search_foods): name, brand, serving_g, notes, per_100g values, archived: true to hide it from search, or clear to remove brand, serving_g or notes. Only the fields you send change. Past and future logs of this food follow the fix.",
		changes, svc.UpdateFood, func(r *service.SavedFood) string {
			arch := ""
			if r.Archived {
				arch = " [archived]"
			}
			return fmt.Sprintf("Updated %s: %s%s%s. food_id=%s", r.Name, per100(r.Per100), serving(r.ServingG), arch, r.FoodID)
		})
	registerLogs(srv, svc)
	registerWorkouts(srv, svc)
}

func per100(n nutrition.Nutrients) string {
	return fmt.Sprintf("%s kcal, %s g protein, %s g fat, %s g carbs per 100 g", num(n.Kcal), num(n.ProteinG), num(n.FatG), num(n.CarbsG))
}

func serving(g *float64) string {
	if g == nil {
		return ""
	}
	return fmt.Sprintf(", serving %s g", num(*g))
}

func num(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", nutrition.Round1(f)), "0"), ".")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func searchSummary(r *service.SearchResult) string {
	lines := []string{"No matches."}
	if len(r.Results) > 0 {
		lines = []string{plural(len(r.Results), "match:", "matches:")}
	}
	for i, f := range r.Results {
		name := f.Name
		if f.Brand != nil {
			name += " (" + *f.Brand + ")"
		}
		id := "food_id=" + f.FoodID + " [saved]"
		if f.FoodID == "" {
			id = fmt.Sprintf("external_source=%s external_id=%s [not saved: call import_food first]", f.Source, f.ExternalID)
		}
		lines = append(lines, fmt.Sprintf("%d. %s | %s | %s", i+1, name, id, per100(f.Per100)))
	}
	return strings.Join(append(lines, r.Warnings...), "\n")
}
