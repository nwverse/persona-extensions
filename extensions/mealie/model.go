package main

import "regexp"

type recipe struct {
	ID                 string       `json:"id"`
	Name               string       `json:"name"`
	Slug               string       `json:"slug"`
	Description        string       `json:"description"`
	TotalTime          string       `json:"totalTime"`
	PrepTime           string       `json:"prepTime"`
	PerformTime        string       `json:"performTime"`
	RecipeServings     float64      `json:"recipeServings"`
	RecipeYield        string       `json:"recipeYield"`
	OriginalURL        string       `json:"orgURL"`
	RecipeIngredient   []ingredient `json:"recipeIngredient"`
	RecipeInstructions []step       `json:"recipeInstructions"`
	DateUpdated        string       `json:"dateUpdated"`
}

type ingredient struct {
	Title        string `json:"title"`
	Display      string `json:"display"`
	OriginalText string `json:"originalText"`
}

type step struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

type recipePage struct {
	Items      []recipe `json:"items"`
	Page       int      `json:"page"`
	PerPage    int      `json:"perPage"`
	Total      int      `json:"total"`
	TotalPages int      `json:"totalPages"`
	Next       *string  `json:"next"`
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,250}$`)

func validRecipeSlug(slug string) bool { return slugPattern.MatchString(slug) }
