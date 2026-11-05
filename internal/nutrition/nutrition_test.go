package nutrition

import (
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestRounding(t *testing.T) {
	cases := []struct {
		in, want1, wantKcal float64
	}{
		{10.1 * 1.5, 15.2, 15},
		{-0.04, 0, 0},
		{2.25, 2.3, 2},
		{99.5, 99.5, 100},
	}
	for _, c := range cases {
		if got := Round1(c.in); got != c.want1 {
			t.Errorf("Round1(%v) = %v, want %v", c.in, got, c.want1)
		}
		if got := RoundKcal(c.in); got != c.wantKcal {
			t.Errorf("RoundKcal(%v) = %v, want %v", c.in, got, c.wantKcal)
		}
	}
}

func TestScaleTreatsMissingAsZero(t *testing.T) {
	got := Scale(Values{Kcal: 200, ProteinG: 10, FatG: 5, CarbsG: 30}, 150)
	want := Nutrients{Kcal: 300, ProteinG: 15, FatG: 7.5, CarbsG: 45}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	got = Scale(Values{FiberG: ptr(4.0), SodiumMg: ptr(100.0)}, 50)
	if got.FiberG != 2 || got.SodiumMg != 50 {
		t.Fatalf("got %+v", got)
	}
}

func TestPer100AndTimes(t *testing.T) {
	n := Per100(Values{Kcal: 123.4, ProteinG: 1.26})
	if n.Kcal != 123 || n.ProteinG != 1.3 {
		t.Fatalf("got %+v", n)
	}
	if got := (Nutrients{Kcal: 2, SodiumMg: 3}).Times(2); got.Kcal != 4 || got.SodiumMg != 6 {
		t.Fatalf("got %+v", got)
	}
}

func TestDay(t *testing.T) {
	entries := []Entry{
		{Eaten: true, Nutrients: Nutrients{Kcal: 500.4, ProteinG: 30.04, FatG: 20, CarbsG: 50, SodiumMg: 1000}},
		{Eaten: false, Nutrients: Nutrients{Kcal: 300, ProteinG: 10}},
	}
	d := Day("2026-03-14", entries, Targets{Kcal: ptr(2000), ProteinG: ptr(120), FatMaxG: ptr(70), SodiumMaxMg: ptr(900), WaterMl: ptr(2000)}, 500)
	if d.Eaten.Kcal != 500 || d.Planned.Kcal != 800 || d.WaterMl != 500 {
		t.Fatalf("totals %+v", d)
	}
	want := map[string]float64{"kcal": 1500, "protein_g": 90, "fat_g": 50, "sodium_mg": -100, "water_ml": 1500}
	if len(d.Remaining) != len(want) {
		t.Fatalf("remaining %+v", d.Remaining)
	}
	for k, v := range want {
		if got, ok := d.RemainingValue(k); !ok || got != v {
			t.Errorf("remaining %s = %v (%v), want %v", k, got, ok, v)
		}
	}
	if _, ok := d.RemainingValue("carbs_g"); ok {
		t.Error("carbs_g has no target and should not be listed")
	}
}

func TestMindfulness(t *testing.T) {
	for _, s := range []float64{1, 5.5, 10} {
		if got := MindfulnessFromStored(MindfulnessToStored(s)); got != s {
			t.Errorf("round trip %v = %v", s, got)
		}
	}
}
