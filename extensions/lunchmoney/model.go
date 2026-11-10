package main

type transaction struct {
	ID              int64   `json:"id"`
	Date            string  `json:"date"`
	Amount          string  `json:"amount"`
	Currency        string  `json:"currency"`
	ToBase          float64 `json:"to_base"`
	Payee           string  `json:"payee"`
	OriginalName    string  `json:"original_name"`
	CategoryID      *int64  `json:"category_id"`
	ManualAccountID *int64  `json:"manual_account_id"`
	PlaidAccountID  *int64  `json:"plaid_account_id"`
	Notes           string  `json:"notes"`
	Status          string  `json:"status"`
	IsPending       bool    `json:"is_pending"`
}

type transactionPage struct {
	Transactions []transaction `json:"transactions"`
	HasMore      bool          `json:"has_more"`
	Error        string        `json:"error"`
}

type account struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	DisplayName     string  `json:"display_name"`
	InstitutionName string  `json:"institution_name"`
	Type            string  `json:"type"`
	Subtype         string  `json:"subtype"`
	Balance         string  `json:"balance"`
	Currency        string  `json:"currency"`
	ToBase          float64 `json:"to_base"`
	BalanceAsOf     string  `json:"balance_as_of"`
	Status          string  `json:"status"`
}

type category struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	IsIncome          bool       `json:"is_income"`
	ExcludeFromTotals bool       `json:"exclude_from_totals"`
	IsGroup           bool       `json:"is_group"`
	Archived          bool       `json:"archived"`
	GroupID           *int64     `json:"group_id"`
	Children          []category `json:"children"`
}
