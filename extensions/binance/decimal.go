package main

import (
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strings"
)

// dec is an exact decimal as Binance takes it: the text sent, and its value.
type dec struct {
	s string
	r *big.Rat
}

var decRE = regexp.MustCompile(`^[0-9]{1,18}(\.[0-9]{1,18})?$`)

// parseDec reads a positive decimal from a JSON string or number; no exponent, sign or more than 18 digits a side.
func parseDec(raw json.RawMessage) (dec, error) {
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, `"`) {
		if err := json.Unmarshal(raw, &s); err != nil {
			return dec{}, errors.New("not a number")
		}
		s = strings.TrimSpace(s)
	}
	return decFromString(s)
}

func decFromString(s string) (dec, error) {
	if !decRE.MatchString(s) {
		return dec{}, errors.New("not a plain positive decimal")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() <= 0 {
		return dec{}, errors.New("must be greater than zero")
	}
	return dec{s: normalize(s), r: r}, nil
}

// normalize drops leading and trailing zeros ("0010.500" → "10.5").
func normalize(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	s = strings.TrimLeft(s, "0")
	if s == "" || s[0] == '.' {
		s = "0" + s
	}
	return s
}

func (d dec) ok() bool            { return d.r != nil }
func (d dec) String() string      { return d.s }
func (d dec) Float() float64      { f, _ := d.r.Float64(); return f }
func (d dec) Rat() *big.Rat       { return new(big.Rat).Set(d.r) }
func ratFloat(r *big.Rat) float64 { f, _ := r.Float64(); return f }

// multipleOf says whether d is a whole number of step (a step of zero or none allows anything).
func (d dec) multipleOf(step *big.Rat) bool {
	if step == nil || step.Sign() == 0 {
		return true
	}
	q := new(big.Rat).Quo(d.r, step)
	return q.IsInt()
}

// ratOf reads Binance's decimal text ("0.00100000"); nil for empty or invalid.
func ratOf(s string) *big.Rat {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil
	}
	return r
}

// floatOf reads Binance's decimal text as a float (0 when invalid).
func floatOf(s string) float64 {
	r := ratOf(s)
	if r == nil {
		return 0
	}
	return ratFloat(r)
}
