// Package nutrition holds the nutrient math. The server does all of it, never
// the model. Food values are per 100 g; a log stores grams only.
package nutrition

import (
	"math"
	"strconv"
)

// Values is a set of nutrients for some amount of food. Fiber and sodium may
// be unknown (nil), which counts as 0.
type Values struct {
	Kcal     float64
	ProteinG float64
	FatG     float64
	CarbsG   float64
	FiberG   *float64
	SodiumMg *float64
}

// Nutrients is a set of nutrients with every field known.
type Nutrients struct {
	Kcal     float64 `json:"kcal"`
	ProteinG float64 `json:"protein_g"`
	FatG     float64 `json:"fat_g"`
	CarbsG   float64 `json:"carbs_g"`
	FiberG   float64 `json:"fiber_g"`
	SodiumMg float64 `json:"sodium_mg"`
}

// Targets are a user's daily goals. A nil field is not set.
type Targets struct {
	Kcal        *int `json:"kcal,omitempty"`
	ProteinG    *int `json:"protein_g,omitempty"`
	FatMinG     *int `json:"fat_min_g,omitempty"`
	FatMaxG     *int `json:"fat_max_g,omitempty"`
	CarbsG      *int `json:"carbs_g,omitempty"`
	FiberG      *int `json:"fiber_g,omitempty"`
	SodiumMaxMg *int `json:"sodium_max_mg,omitempty"`
	WaterMl     *int `json:"water_ml,omitempty"`
}

// Entry is one log's unrounded nutrients and whether it was eaten.
type Entry struct {
	Eaten     bool
	Nutrients Nutrients
}

// Remaining is one target minus what was eaten. Negative means over.
type Remaining struct {
	Key   string
	Value float64
}

// DayTotals are the totals for one day.
type DayTotals struct {
	Date      string
	Eaten     Nutrients
	Planned   Nutrients
	Targets   Targets
	Remaining []Remaining
	WaterMl   float64
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// snap trims float noise so 10.1 * 1.5 rounds like 15.15.
func snap(x float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'g', 12, 64), 64)
	return v
}

func clean(x float64) float64 {
	if x == 0 {
		return 0
	}
	return x
}

// Round1 rounds to one decimal place.
func Round1(x float64) float64 {
	return clean(math.Floor(snap(x*10)+0.5) / 10)
}

// RoundKcal rounds calories to a whole number.
func RoundKcal(x float64) float64 {
	return clean(math.Floor(snap(x) + 0.5))
}

// Scale returns the unrounded nutrients in grams of a food given per 100 g.
func Scale(v Values, grams float64) Nutrients {
	f := grams / 100
	return Nutrients{
		Kcal:     v.Kcal * f,
		ProteinG: v.ProteinG * f,
		FatG:     v.FatG * f,
		CarbsG:   v.CarbsG * f,
		FiberG:   deref(v.FiberG) * f,
		SodiumMg: deref(v.SodiumMg) * f,
	}
}

// Times multiplies every nutrient by k.
func (n Nutrients) Times(k float64) Nutrients {
	return Nutrients{n.Kcal * k, n.ProteinG * k, n.FatG * k, n.CarbsG * k, n.FiberG * k, n.SodiumMg * k}
}

// Sum adds nutrients together.
func Sum(list []Nutrients) Nutrients {
	var t Nutrients
	for _, n := range list {
		t.Kcal += n.Kcal
		t.ProteinG += n.ProteinG
		t.FatG += n.FatG
		t.CarbsG += n.CarbsG
		t.FiberG += n.FiberG
		t.SodiumMg += n.SodiumMg
	}
	return t
}

// Round applies display rounding: whole calories, one decimal for the rest.
func Round(n Nutrients) Nutrients {
	return Nutrients{RoundKcal(n.Kcal), Round1(n.ProteinG), Round1(n.FatG), Round1(n.CarbsG), Round1(n.FiberG), Round1(n.SodiumMg)}
}

// Per100 returns a food's per-100 g values with display rounding.
func Per100(v Values) Nutrients {
	return Round(Scale(v, 100))
}

// Day totals one day's entries. Eaten counts eaten entries; planned counts
// all of them. Remaining is the target minus the rounded eaten value, so the
// numbers read back always add up, and only for targets that are set. Fat and
// sodium measure against their maximums.
func Day(date string, entries []Entry, t Targets, waterMl float64) DayTotals {
	var all, eaten []Nutrients
	for _, e := range entries {
		all = append(all, e.Nutrients)
		if e.Eaten {
			eaten = append(eaten, e.Nutrients)
		}
	}
	d := DayTotals{Date: date, Eaten: Round(Sum(eaten)), Planned: Round(Sum(all)), Targets: t, WaterMl: Round1(waterMl)}
	add := func(key string, target *int, got float64, kcal bool) {
		if target == nil {
			return
		}
		v := Round1(float64(*target) - got)
		if kcal {
			v = RoundKcal(float64(*target) - got)
		}
		d.Remaining = append(d.Remaining, Remaining{key, v})
	}
	add("kcal", t.Kcal, d.Eaten.Kcal, true)
	add("protein_g", t.ProteinG, d.Eaten.ProteinG, false)
	add("carbs_g", t.CarbsG, d.Eaten.CarbsG, false)
	add("fiber_g", t.FiberG, d.Eaten.FiberG, false)
	add("fat_g", t.FatMaxG, d.Eaten.FatG, false)
	add("sodium_mg", t.SodiumMaxMg, d.Eaten.SodiumMg, false)
	add("water_ml", t.WaterMl, d.WaterMl, false)
	return d
}

// RemainingValue returns the remaining amount for key, if that target is set.
func (d DayTotals) RemainingValue(key string) (float64, bool) {
	for _, r := range d.Remaining {
		if r.Key == key {
			return r.Value, true
		}
	}
	return 0, false
}

// MindfulnessToStored maps the 1 to 10 score shown to users onto 0 to 1.
func MindfulnessToStored(score float64) float64 {
	return (score - 1) / 9
}

// MindfulnessFromStored maps a stored 0 to 1 score back onto 1 to 10.
func MindfulnessFromStored(stored float64) float64 {
	return Round1(stored*9 + 1)
}
