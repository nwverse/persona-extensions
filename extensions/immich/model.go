package main

import "regexp"

type asset struct {
	ID               string `json:"id"`
	OriginalFileName string `json:"originalFileName"`
	Type             string `json:"type"`
	FileCreatedAt    string `json:"fileCreatedAt"`
	IsFavorite       bool   `json:"isFavorite"`
	IsTrashed        bool   `json:"isTrashed"`
	Visibility       string `json:"visibility"`
	Exif             struct {
		City        string  `json:"city"`
		Country     string  `json:"country"`
		Description string  `json:"description"`
		Make        string  `json:"make"`
		Model       string  `json:"model"`
		LensModel   string  `json:"lensModel"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
	} `json:"exifInfo"`
	People []struct {
		Name string `json:"name"`
	} `json:"people"`
}

type searchResponse struct {
	Assets struct {
		Items      []asset `json:"items"`
		Count      int     `json:"count"`
		NextCursor *string `json:"nextCursor"`
	} `json:"assets"`
}

type album struct {
	ID          string  `json:"id"`
	AlbumName   string  `json:"albumName"`
	AssetCount  int     `json:"assetCount"`
	Description string  `json:"description"`
	UpdatedAt   string  `json:"updatedAt"`
	Assets      []asset `json:"assets"`
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validAssetID(id string) bool { return uuidPattern.MatchString(id) }
