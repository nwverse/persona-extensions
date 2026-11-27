package main

import (
	"strings"
	"testing"
)

const nullForecast = `{"current":{"time":"2026-11-16T12:00","temperature_2m":null,"apparent_temperature":null,"relative_humidity_2m":null,"weather_code":null,"wind_speed_10m":null},"daily":{"time":["2026-11-16"],"temperature_2m_max":[null],"temperature_2m_min":[null],"weather_code":[null],"precipitation_probability_max":[null]}}`

func TestNullWeatherReadingsAreUnknown(t *testing.T) {
	f := newFakeSite(t)
	f.serve("/v1/forecast", nullForecast)
	a := testApp(t, f, "")
	for _, name := range []string{"weather_now", "weather_forecast"} {
		out, err := callTool(t, a, name, "")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "0°C") || strings.Contains(out, "晴朗") || strings.Contains(out, "0%") {
			t.Fatalf("%s turned missing measurements into zero/clear: %s", name, out)
		}
		if !strings.Contains(out, "未知") {
			t.Fatalf("%s does not name unavailable readings: %s", name, out)
		}
	}
}

func TestZeroWeatherReadingsRemainZero(t *testing.T) {
	f := newFakeSite(t)
	f.serve("/v1/forecast", `{"current":{"time":"2026-11-16T12:00","temperature_2m":0,"apparent_temperature":0,"relative_humidity_2m":0,"weather_code":0,"wind_speed_10m":0},"daily":{"time":["2026-11-16"],"temperature_2m_max":[0],"temperature_2m_min":[0],"weather_code":[0],"precipitation_probability_max":[0]}}`)
	a := testApp(t, f, "")
	now, err := callTool(t, a, "weather_now", "")
	if err != nil {
		t.Fatal(err)
	}
	forecast, err := callTool(t, a, "weather_forecast", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{now, forecast} {
		if !strings.Contains(out, "0°C") || !strings.Contains(out, "晴朗") || !strings.Contains(out, "0%") {
			t.Fatalf("genuine zero was not preserved: %s", out)
		}
	}
}

func TestNullWindDirectionWithNonzeroSpeedDoesNotFabricateNorthWind(t *testing.T) {
	for name, body := range map[string]string{
		"null":    `{"current":{"time":"2026-11-16T12:00","temperature_2m":12,"apparent_temperature":10,"relative_humidity_2m":50,"weather_code":1,"wind_speed_10m":15,"wind_direction_10m":null},"daily":{"time":["2026-11-16"],"temperature_2m_max":[16],"temperature_2m_min":[8],"weather_code":[1],"precipitation_probability_max":[10]}}`,
		"omitted": `{"current":{"time":"2026-11-16T12:00","temperature_2m":12,"apparent_temperature":10,"relative_humidity_2m":50,"weather_code":1,"wind_speed_10m":15},"daily":{"time":["2026-11-16"],"temperature_2m_max":[16],"temperature_2m_min":[8],"weather_code":[1],"precipitation_probability_max":[10]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeSite(t)
			sample(t, f)
			f.serve("/v1/forecast", body)
			a := testApp(t, f, "")

			now, err := callTool(t, a, "weather_now", "")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(now, "北风") {
				t.Fatalf("weather_now fabricated north wind for %s wind_direction_10m: %s", name, now)
			}
			if !strings.Contains(now, "风 15 km/h") {
				t.Fatalf("weather_now missing wind speed: %s", now)
			}

			v := draw(t, a, 3, nil)
			says(t, v, "{风=15 km/h}")
			saysNot(t, v, "北风")
		})
	}

	t.Run("genuine zero degrees remains north wind", func(t *testing.T) {
		f := newFakeSite(t)
		sample(t, f)
		f.serve("/v1/forecast", `{"current":{"time":"2026-11-16T12:00","temperature_2m":12,"apparent_temperature":10,"relative_humidity_2m":50,"weather_code":1,"wind_speed_10m":15,"wind_direction_10m":0},"daily":{"time":["2026-11-16"],"temperature_2m_max":[16],"temperature_2m_min":[8],"weather_code":[1],"precipitation_probability_max":[10]}}`)
		a := testApp(t, f, "")

		now, err := callTool(t, a, "weather_now", "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(now, "北风 15 km/h") {
			t.Fatalf("weather_now lost genuine 0-degree north wind: %s", now)
		}
		v := draw(t, a, 3, nil)
		says(t, v, "{北风=15 km/h}")
	})
}
