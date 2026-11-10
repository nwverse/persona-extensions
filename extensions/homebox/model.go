package main

import "regexp"

type entity struct {
	ID               string      `json:"id"`
	AssetID          string      `json:"assetId"`
	Name             string      `json:"name"`
	Description      string      `json:"description"`
	Quantity         float64     `json:"quantity"`
	Archived         bool        `json:"archived"`
	SerialNumber     string      `json:"serialNumber"`
	ModelNumber      string      `json:"modelNumber"`
	Manufacturer     string      `json:"manufacturer"`
	Notes            string      `json:"notes"`
	WarrantyDetails  string      `json:"warrantyDetails"`
	WarrantyExpires  string      `json:"warrantyExpires"`
	LifetimeWarranty bool        `json:"lifetimeWarranty"`
	Location         *entity     `json:"location"`
	Parent           *entity     `json:"parent"`
	EntityType       *entityType `json:"entityType"`
	Tags             []tag       `json:"tags"`
	UpdatedAt        string      `json:"updatedAt"`
}

type entityType struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsLocation  bool   `json:"isLocation"`
}

type tag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type entityPage struct {
	Items    []entity `json:"items"`
	Page     int      `json:"page"`
	PageSize int      `json:"pageSize"`
	Total    int      `json:"total"`
}

type treeItem struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	Children []treeItem `json:"children"`
}

var entityIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validEntityID(id string) bool { return entityIDPattern.MatchString(id) }
