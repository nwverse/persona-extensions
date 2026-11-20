package model

import "github.com/nwverse/persona/sdk/go/wire"

// The candidate-model parts of model.complete (persona #10398), as the hub sends them. They are declared here rather
// than taken from the SDK copy so that the extension builds the same on an SDK copied before them (a repository that
// has not synced its SDK yet); the JSON on the wire is the hub's. On a newer SDK the embedded structs carry fields of
// the same names one level down, and the ones here, shallower, are the ones encoding/json uses.

// codeCandidatesFailed: every candidate of two or more failed; the data is candidatesFailed.
const codeCandidatesFailed = "model_candidates_failed"

// chainParams is model.complete's params with the candidates, tried in order.
type chainParams struct {
	wire.ModelCompleteParams
	Models []string `json:"models,omitempty"`
}

// chainResult is model.complete's result with the candidate that answered and the ones that failed before it.
type chainResult struct {
	wire.ModelCompleteResult
	Candidate string    `json:"candidate,omitempty"`
	Attempts  []attempt `json:"attempts,omitempty"`
}

// attempt is a candidate that failed.
type attempt struct {
	Model   string          `json:"model"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Until   string          `json:"until,omitempty"`
	Seconds float64         `json:"seconds"`
	Usage   wire.ModelUsage `json:"usage"`
}

// candidatesFailed is the data of model_candidates_failed.
type candidatesFailed struct {
	Attempts []attempt       `json:"attempts"`
	Usage    wire.ModelUsage `json:"usage"`
}

// notOffered is the data of model_unavailable for a candidate no provider offers.
type notOffered struct {
	Model string `json:"model"`
}
