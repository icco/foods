// Package store holds every database model and query for food data. Every
// function except FindOrCreateUser takes a user id and is scoped by it.
package store

import (
	"time"

	"go.icco.me/foods/internal/nutrition"
)

// MealType groups food logs. Water is drinking water, logged in grams (= ml).
type MealType string

// MealTypes lists every meal type.
var MealTypes = []MealType{"breakfast", "lunch", "dinner", "snack", "dessert", "water"}

// WorkoutType is the kind of a workout.
type WorkoutType string

// WorkoutTypes lists every workout type.
var WorkoutTypes = []WorkoutType{"run", "walk", "cycle", "swim", "strength", "yoga", "hiit", "other"}

// Food sources.
const (
	SourceLabel    = "label"
	SourceUSDA     = "usda"
	SourceOFF      = "off"
	SourceEstimate = "estimate"
	SourceRecipe   = "recipe"
)

// WaterFoodName is the food every user gets, so water is logged like any food.
const WaterFoodName = "Water"

// DefaultTimeZone decides "today" until a user sets their own.
const DefaultTimeZone = "America/New_York"

// User is someone who signed in, keyed by their upstream identity.
type User struct {
	ID        string `gorm:"primaryKey"`
	Provider  string `gorm:"not null;uniqueIndex:idx_users_identity"`
	Subject   string `gorm:"not null;uniqueIndex:idx_users_identity"`
	Email     string `gorm:"not null"`
	TimeZone  string `gorm:"not null;default:America/New_York"`
	CreatedAt time.Time
}

// Food holds nutrients per 100 g. A saved meal is a food with source recipe
// whose serving is the whole meal.
type Food struct {
	ID         string   `gorm:"primaryKey"`
	UserID     string   `gorm:"not null;index:idx_foods_user_name;uniqueIndex:idx_foods_user_external,where:external_id IS NOT NULL"`
	Name       string   `gorm:"not null;index:idx_foods_user_name"`
	Brand      *string  `gorm:""`
	ServingG   *float64 `gorm:"check:serving_g IS NULL OR serving_g > 0"`
	Kcal       float64  `gorm:"not null;check:kcal >= 0"`
	ProteinG   float64  `gorm:"not null;check:protein_g >= 0"`
	FatG       float64  `gorm:"not null;check:fat_g >= 0"`
	CarbsG     float64  `gorm:"not null;check:carbs_g >= 0"`
	FiberG     *float64 `gorm:""`
	SodiumMg   *float64 `gorm:""`
	Source     string   `gorm:"not null;uniqueIndex:idx_foods_user_external,where:external_id IS NOT NULL"`
	ExternalID *string  `gorm:"uniqueIndex:idx_foods_user_external,where:external_id IS NOT NULL"`
	Verified   bool     `gorm:"not null;default:false"`
	Notes      *string
	ArchivedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Values returns the food's per-100 g nutrients.
func (f *Food) Values() nutrition.Values {
	return nutrition.Values{Kcal: f.Kcal, ProteinG: f.ProteinG, FatG: f.FatG, CarbsG: f.CarbsG, FiberG: f.FiberG, SodiumMg: f.SodiumMg}
}

// FoodLog is a pointer at a food and an amount in grams. It never stores
// nutrients.
type FoodLog struct {
	ID               string    `gorm:"primaryKey"`
	UserID           string    `gorm:"not null;index:idx_food_logs_user_date"`
	Date             string    `gorm:"type:varchar(10);not null;index:idx_food_logs_user_date"`
	MealType         MealType  `gorm:"not null"`
	FoodID           string    `gorm:"not null"`
	Food             Food      `gorm:"foreignKey:FoodID"`
	Grams            float64   `gorm:"not null;check:grams > 0 AND grams <= 5000"`
	Note             *string   `gorm:""`
	Eaten            bool      `gorm:"not null"`
	MindfulnessScore *float64  `gorm:""`
	EatenAt          time.Time `gorm:"not null"`
	ArchivedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Target is a user's daily goals. A nil field is not set.
type Target struct {
	UserID      string `gorm:"primaryKey"`
	Kcal        *int
	ProteinG    *int
	FatMinG     *int
	FatMaxG     *int
	CarbsG      *int
	FiberG      *int
	SodiumMaxMg *int
	WaterMl     *int
	UpdatedAt   time.Time
}

// Nutrition returns the targets in the form the math uses.
func (t *Target) Nutrition() nutrition.Targets {
	if t == nil {
		return nutrition.Targets{}
	}
	return nutrition.Targets{Kcal: t.Kcal, ProteinG: t.ProteinG, FatMinG: t.FatMinG, FatMaxG: t.FatMaxG, CarbsG: t.CarbsG, FiberG: t.FiberG, SodiumMaxMg: t.SodiumMaxMg, WaterMl: t.WaterMl}
}

// Workout is deliberately minimal: a day, a type and a duration.
type Workout struct {
	ID              string      `gorm:"primaryKey"`
	UserID          string      `gorm:"not null;index:idx_workouts_user_date"`
	Date            string      `gorm:"type:varchar(10);not null;index:idx_workouts_user_date"`
	Type            WorkoutType `gorm:"not null"`
	DurationMinutes float64     `gorm:"not null;check:duration_minutes > 0 AND duration_minutes <= 1440"`
	Note            *string
	ArchivedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Request kinds keep each tool's client_request_id values apart.
const (
	RequestFood    = "food"
	RequestWorkout = "workout"
)

// RequestLog remembers each logging request by the id the assistant sent, so
// a retried request is not saved twice.
type RequestLog struct {
	UserID    string   `gorm:"primaryKey"`
	Kind      string   `gorm:"primaryKey"`
	RequestID string   `gorm:"primaryKey"`
	Date      string   `gorm:"type:varchar(10);not null"`
	IDs       []string `gorm:"serializer:json;not null"`
	CreatedAt time.Time
}

// Models lists every model for AutoMigrate.
var Models = []any{&User{}, &Food{}, &FoodLog{}, &Target{}, &Workout{}, &RequestLog{}}
