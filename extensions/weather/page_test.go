package main

import (
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

func TestDefaultPage(t *testing.T) {
	f := newFakeSite(t)
	sample(t, f)
	a := testApp(t, f, "")
	v := draw(t, a, 3, nil)
	says(t, v,
		"## 上海",
		"[metric]",
		"⛅ 11°C", "局部多云", "现在",
		"{体感=7°C}", "{湿度=63%}", "{西北风=17.7 km/h}", "{今天=8.2°C / 15°C}",
		"## 未来 24 小时", "series:气温[24]",
		"## 7 天预报", "周一", "15°C / 8.2°C", "降水概率 79%",
		"## 空气质量", "空气质量指数=49 · 一般", "PM2.5=14.3", "PM10=14.4",
		"刷新")
	saysNot(t, v, "segmented", "读不懂", "0°C")
}

func TestFahrenheitAndMs(t *testing.T) {
	f := newFakeSite(t)
	sample(t, f)
	a := testApp(t, f, `{"temperature_unit": "fahrenheit", "wind_unit": "ms"}`)
	v := draw(t, a, 3, nil)
	says(t, v, "⛅ 51.8°F", "{体感=44.6°F}", "{西北风=4.9 m/s}")
	saysNot(t, v, "°C", "km/h")
}

func TestMultipleCitiesSwitch(t *testing.T) {
	f := newFakeSite(t)
	sample(t, f)
	a := testApp(t, f, `{"cities": [{"name": "上海", "latitude": 31.22, "longitude": 121.46}, {"name": "北京", "latitude": 39.9, "longitude": 116.4}]}`)
	v := draw(t, a, 3, nil)
	says(t, v, "segmented", "<上海:上海>", "<北京:北京>", "## 上海")
	if st := stateOf(t, v.State); st.City != "" {
		t.Fatalf("state %+v", st)
	}
	v = tap(t, a, 3, v, "city", map[string]string{"city": "北京"})
	says(t, v, "## 北京")
	saysNot(t, v, "## 上海")
	if st := stateOf(t, v.State); st.City != "北京" {
		t.Fatalf("state %+v", st)
	}
	// A city not in the setting leaves the choice unchanged.
	v = tap(t, a, 3, v, "city", map[string]string{"city": "广州"})
	says(t, v, "## 北京")
}

// Before UI version 2 the picker is rows, not a segmented control.
func TestPickerBeforeUIVersion2(t *testing.T) {
	f := newFakeSite(t)
	sample(t, f)
	a := testApp(t, f, `{"cities": [{"name": "上海", "latitude": 31.22, "longitude": 121.46}, {"name": "北京", "latitude": 39.9, "longitude": 116.4}]}`)
	v := draw(t, a, 1, nil)
	saysNot(t, v, "segmented")
	says(t, v, "## 城市")
	if shanghai, beijing := findRow(v, "上海"), findRow(v, "北京"); shanghai == nil || shanghai.Badge == nil || beijing == nil || beijing.Badge != nil {
		t.Fatalf("the rows: %+v %+v", shanghai, beijing)
	}
}

func TestErrorIsNotZero(t *testing.T) {
	f := newFakeSite(t)
	sample(t, f)
	a := testApp(t, f, "")
	f.serve("/v1/forecast", `{"error": true}`)
	v := draw(t, a, 3, nil)
	says(t, v, "读不懂", "重试", "→refresh")
	saysNot(t, v, "未来 24 小时", "7 天预报", "11°C", "0°C")

	// An HTTP failure says the status too.
	g := newFakeSite(t)
	sample(t, g)
	g.put("/v1/forecast", answer{status: 502, body: "bad gateway"})
	b := testApp(t, g, "")
	v = draw(t, b, 3, nil)
	says(t, v, "HTTP 502", "重试")
}

func TestAirFailureKeepsForecast(t *testing.T) {
	f := newFakeSite(t)
	sample(t, f)
	a := testApp(t, f, "")
	f.serve("/v1/air-quality", `{"error": true}`)
	v := draw(t, a, 3, nil)
	says(t, v, "## 未来 24 小时", "## 7 天预报", "## 空气质量", "读不懂")
	saysNot(t, v, "空气质量指数=")
}

func TestBadCitiesNote(t *testing.T) {
	f := newFakeSite(t)
	sample(t, f)
	a := testApp(t, f, `{"cities": [{"name": "", "latitude": 999, "longitude": 999}]}`)
	v := draw(t, a, 3, nil)
	says(t, v, "这些城市没填对", "## 上海")
}

func TestRefreshRereads(t *testing.T) {
	f := newFakeSite(t)
	sample(t, f)
	a := testApp(t, f, "")
	v := draw(t, a, 3, nil)
	if n := f.hit("/v1/forecast"); n != 1 {
		t.Fatalf("first draw read forecast %d times", n)
	}
	// A second draw within 10 minutes answers from the cache.
	draw(t, a, 3, nil)
	if n := f.hit("/v1/forecast"); n != 1 {
		t.Fatalf("cached draw read forecast %d times", n)
	}
	// A refresh reads again.
	v = tap(t, a, 3, v, "refresh", nil)
	if n := f.hit("/v1/forecast"); n != 2 {
		t.Fatalf("refresh read forecast %d times", n)
	}
}

// The chart points carry the chosen unit and the next 24 hours' labels.
func TestChartPoints(t *testing.T) {
	f := newFakeSite(t)
	sample(t, f)
	a := testApp(t, f, "")
	v := draw(t, a, 3, nil)
	var chart *wire.Component
	for i := range v.Sections {
		for j := range v.Sections[i].Items {
			if c := &v.Sections[i].Items[j]; c.Type == wire.ComponentChart {
				chart = c
			}
		}
	}
	if chart == nil || chart.Kind != wire.ChartKindLine || chart.Unit != "°C" || len(chart.Series) != 1 || len(chart.Series[0].Points) != 24 {
		t.Fatalf("chart %+v", chart)
	}
	if got := chart.Series[0].Points[0]; got.Label != "17时" || got.Value != 11.3 {
		t.Fatalf("first point %+v", got)
	}
	if chart.Series[0].Points[1].Label != "18时" {
		t.Fatalf("second point label %q", chart.Series[0].Points[1].Label)
	}
}

// A configured name the search does not know is said on the page, with what to write instead and a retry (#493).
func TestConfiguredNameNotFound(t *testing.T) {
	f := newFakeSite(t)
	sample(t, f)
	f.serve("/v1/search", `{"results":[]}`)
	a := testApp(t, f, `{"cities": ["不存在的城市"]}`)
	v := draw(t, a, 3, nil)
	says(t, v, "## 不存在的城市", "没搜到", "纬度,经度", "重试")
	saysNot(t, v, "0°C")
}

func TestPickerFallsBackToRowsWhenOverSegmentedLimits(t *testing.T) {
	t.Run("more than 5 cities", func(t *testing.T) {
		f := newFakeSite(t)
		sample(t, f)
		a := testApp(t, f, `{"cities": ["上海", "北京 39.9,116.4", "广州 23.13,113.26", "深圳 22.54,114.06", "杭州 30.27,120.15", "成都 30.57,104.07"]}`)
		v := draw(t, a, 3, nil)
		saysNot(t, v, "segmented")
		says(t, v, "## 城市", "## 上海")
		row := findRow(v, "成都")
		if row == nil || row.Action == nil {
			t.Fatalf("missing row for 成都 in:\n%s", texts(v))
		}
		// Simulate UI version 3, which does not send Action.Values (x-since: 4).
		v = tap(t, a, 3, v, row.Action.ID, nil)
		says(t, v, "## 成都")
		saysNot(t, v, "## 上海")
	})

	t.Run("city name over 12 runes", func(t *testing.T) {
		f := newFakeSite(t)
		sample(t, f)
		a := testApp(t, f, `{"cities": ["上海", "San Francisco 37.77,-122.42"]}`)
		v := draw(t, a, 3, nil)
		saysNot(t, v, "segmented")
		says(t, v, "## 城市", "## 上海")
		row := findRow(v, "San Francisco")
		if row == nil || row.Action == nil {
			t.Fatalf("missing row for San Francisco in:\n%s", texts(v))
		}
		v = tap(t, a, 3, v, row.Action.ID, nil)
		says(t, v, "## San Francisco")
		saysNot(t, v, "## 上海")

		// UI v4+ passes row.Action.Values; verify tapping back to 上海 works with Values populated.
		shRow := findRow(v, "上海")
		if shRow == nil || shRow.Action == nil {
			t.Fatalf("missing row for 上海 in:\n%s", texts(v))
		}
		v = tap(t, a, 4, v, shRow.Action.ID, shRow.Action.Values)
		says(t, v, "## 上海")
		saysNot(t, v, "## San Francisco")

		// UI v1 also uses row fallback without Action.Values.
		v1 := draw(t, a, 1, nil)
		sfRow1 := findRow(v1, "San Francisco")
		if sfRow1 == nil || sfRow1.Action == nil {
			t.Fatalf("missing UI v1 row for San Francisco in:\n%s", texts(v1))
		}
		v1 = tap(t, a, 1, v1, sfRow1.Action.ID, nil)
		says(t, v1, "## San Francisco")
	})

	t.Run("multi-byte 12-rune vs 13-rune CJK boundary", func(t *testing.T) {
		f := newFakeSite(t)
		sample(t, f)
		// 12 CJK runes (36 UTF-8 bytes) still fit segmented control.
		twelve := "新疆维吾尔自治区乌鲁木齐"
		a := testApp(t, f, `{"cities": ["上海", "`+twelve+` 43.82,87.62"]}`)
		v := draw(t, a, 3, nil)
		says(t, v, "segmented", "<"+twelve+":"+twelve+">")

		// 13 CJK runes exceed the 12-rune segmented limit and fall back to rows.
		thirteen := "新疆维吾尔自治区乌鲁木齐市"
		b := testApp(t, f, `{"cities": ["上海", "`+thirteen+` 43.82,87.62"]}`)
		v2 := draw(t, b, 3, nil)
		saysNot(t, v2, "segmented")
		says(t, v2, "## 城市")
		if row := findRow(v2, thirteen); row == nil || row.Action == nil {
			t.Fatalf("missing row for %s in:\n%s", thirteen, texts(v2))
		}
	})

	t.Run("invalid city actions and whitespace values leave selection unchanged", func(t *testing.T) {
		f := newFakeSite(t)
		sample(t, f)
		a := testApp(t, f, `{"cities": ["上海", "San Francisco 37.77,-122.42"]}`)
		v := draw(t, a, 3, &pageState{City: "San Francisco"})
		says(t, v, "## San Francisco")

		for _, tc := range []struct {
			id     string
			values map[string]string
		}{
			{"city", map[string]string{"city": "   "}},
			{"city", map[string]string{"city": "不存在"}},
			{"city:0", map[string]string{"city": "不存在"}},
			{"city:-1", nil},
			{"city:abc", nil},
			{"city:999", nil},
			{"city:", nil},
		} {
			got := tap(t, a, 3, v, tc.id, tc.values)
			says(t, got, "## San Francisco")
			if st := stateOf(t, got.State); st.City != "San Francisco" {
				t.Errorf("action %q values %v corrupted state: %+v", tc.id, tc.values, st)
			}
		}

		// Whitespace around a valid city name stores canonical trimmed state.
		got := tap(t, a, 3, v, "city", map[string]string{"city": " 上海 "})
		says(t, got, "## 上海")
		if st := stateOf(t, got.State); st.City != "上海" {
			t.Errorf("expected canonical trimmed state 上海, got %+v", st)
		}
	})

	t.Run("5 cities within 12 runes still uses segmented", func(t *testing.T) {
		f := newFakeSite(t)
		sample(t, f)
		a := testApp(t, f, `{"cities": ["上海", "北京 39.9,116.4", "广州 23.13,113.26", "深圳 22.54,114.06", "123456789012 30.27,120.15"]}`)
		v := draw(t, a, 3, nil)
		says(t, v, "segmented", "<123456789012:123456789012>")
	})
}
