package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	storeTagsTool = "steam_store_tags"

	tagCatalogPath = "/tagdata/populartags/"

	discoveryBudget    = 8 * time.Second
	tagCatalogTimeout  = 1200 * time.Millisecond
	tagCatalogTTL      = 8 * time.Hour
	discoveryCacheSize = 64
	tagNameRunes       = 60
	tagCatalogBytes    = 128 << 10
)

const (
	problemDiscoveryDisabled problem = "discovery_disabled"
	problemDiscoveryTagArgs  problem = "invalid_store_tags_args"
	problemBuiltInTags       problem = "built_in_tags"
)

func discoveryTools(limit int) []wire.ConnectorTool {
	const language = `"language":{"type":"string","description":"Steam language code such as english or schinese. Defaults to the Hub locale."}`
	resultLimit := fmt.Sprintf(`"limit":{"type":"integer","minimum":1,"maximum":%d,"default":%d}`, limit, limit)
	return []wire.ConnectorTool{
		{
			Name:        storeTagsTool,
			Description: "Find localized Steam Store tags and their numeric IDs. Use a short query for each gameplay or mood term.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Tag name or ID, up to 100 characters."},` + language + "," + resultLimit + `}}`),
		},
	}
}

type steamDiscovery struct {
	client steamClient
	now    func() time.Time
	tags   discoveryCache[[]discoveryTag]
}

func newSteamDiscovery(client *http.Client, now func() time.Time) *steamDiscovery {
	return &steamDiscovery{client: steamClient{http: client}, now: now}
}

// Reuse the existing request coalescing, retry delay and bounded stale reads.
// Public results live in memory and never include account membership.
type discoveryCache[T any] struct {
	mu    sync.Mutex
	slots map[string]*cacheSlot[T]
	order []string
}

func (c *discoveryCache[T]) slot(key string, now func() time.Time) *cacheSlot[T] {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.slots[key]; s != nil {
		return s
	}
	if c.slots == nil {
		c.slots = make(map[string]*cacheSlot[T])
	}
	if len(c.order) == discoveryCacheSize {
		delete(c.slots, c.order[0])
		c.order = c.order[1:]
	}
	s := newSlot[T]("", "", now, nil)
	c.slots[key] = s
	c.order = append(c.order, key)
	return s
}

func (d *steamDiscovery) get(ctx context.Context, budget time.Duration, maxBytes int64, target string, params url.Values, out any) problem {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return d.client.readBoundedJSON(ctx, target, params, nil, false, maxBytes, out)
}

type discoveryOptions struct {
	Language string `json:"language"`
	Limit    *int   `json:"limit"`
}

func (o discoveryOptions) resultLimit(configured int) (int, bool) {
	if o.Limit == nil {
		return configured, true
	}
	return min(*o.Limit, configured), *o.Limit > 0 && *o.Limit <= maxToolLimit
}

// Steam's Store endpoints use API language names. Accept the common Hub locale aliases too.
const discoveryLanguages = " arabic brazilian bulgarian czech danish dutch english finnish french german greek hungarian indonesian italian japanese koreana latam malay norwegian polish portuguese romanian russian schinese spanish swedish tchinese thai turkish ukrainian vietnamese "

var discoveryLocaleAliases = map[string]string{
	"ar": "arabic", "bg": "bulgarian", "cs": "czech", "da": "danish", "de": "german", "el": "greek",
	"en": "english", "es": "spanish", "es-419": "latam", "fi": "finnish", "fr": "french", "hu": "hungarian",
	"id": "indonesian", "it": "italian", "ja": "japanese", "ko": "koreana", "ms": "malay", "nl": "dutch",
	"no": "norwegian", "pl": "polish", "pt": "portuguese", "pt-br": "brazilian", "ro": "romanian",
	"ru": "russian", "sv": "swedish", "th": "thai", "tr": "turkish", "uk": "ukrainian", "vi": "vietnamese",
	"zh": "schinese", "zh-hans": "schinese", "zh-hans-cn": "schinese", "zh-cn": "schinese",
	"zh-hant": "tchinese", "zh-hant-tw": "tchinese", "zh-tw": "tchinese", "zh-hk": "tchinese",
}

func discoveryLanguage(input, fallback string) (string, bool) {
	language := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(input), "_", "-"))
	if language == "" {
		return fallback, true
	}
	if alias := discoveryLocaleAliases[language]; alias != "" {
		return alias, true
	}
	if base, _, ok := strings.Cut(language, "-"); ok && discoveryLocaleAliases[base] != "" {
		return discoveryLocaleAliases[base], true
	}
	return language, !strings.Contains(language, " ") && strings.Contains(discoveryLanguages, " "+language+" ")
}

func (a *app) callDiscoveryTool(ctx context.Context, name string, raw json.RawMessage) (wire.ConnectorCallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryBudget)
	defer cancel()
	t := textFor(a.locale)
	fallbackLanguage, ok := discoveryLanguage(a.locale, t.storeLanguage)
	if !ok {
		fallbackLanguage = t.storeLanguage
	}
	limit := a.config.DiscoveryResultLimit()
	out := map[string]any{"available": false}
	var err error
	switch name {
	case storeTagsTool:
		err = a.discovery.listTags(ctx, raw, fallbackLanguage, limit, t, out)
	}
	if err != nil {
		return wire.ConnectorCallResult{}, err
	}
	return encodeSteamTool(out, nil)
}

type discoveryTag struct {
	ID   uint32 `json:"tagid"`
	Name string `json:"name,omitempty"`
}

// A small set of common Store tags for cold starts during catalog outages.
var commonStoreTags = []struct {
	id               uint32
	english, chinese string
}{
	{492, "Indie", "独立"}, {19, "Action", "动作"}, {21, "Adventure", "冒险"},
	{597, "Casual", "休闲"}, {599, "Simulation", "模拟"}, {9, "Strategy", "策略"},
	{122, "RPG", "角色扮演"}, {4182, "Singleplayer", "单人"}, {3859, "Multiplayer", "多人"},
	{1685, "Co-op", "合作"}, {3843, "Online Co-Op", "在线合作"}, {3841, "Local Co-Op", "本地合作"},
	{1742, "Story Rich", "剧情丰富"}, {1664, "Puzzle", "解谜"}, {1654, "Relaxing", "放松"},
	{97376, "Cozy", "温馨惬意"}, {1716, "Roguelike", "类 Rogue"}, {3959, "Roguelite", "轻度 Rogue"},
	{32322, "Deckbuilding", "牌组构建"}, {1695, "Open World", "开放世界"}, {3834, "Exploration", "探索"},
	{1662, "Survival", "生存"}, {3810, "Sandbox", "沙盒"}, {1734, "Fast-Paced", "快节奏"},
	{4234, "Short", "短篇"}, {7481, "Controller", "控制器"}, {87918, "Farming Sim", "农场模拟"},
}

func tagNameKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

func builtInTagLanguage(language string) string {
	if language == "schinese" {
		return language
	}
	return "english"
}

func builtInTags(language string) []discoveryTag {
	tags := make([]discoveryTag, 0, len(commonStoreTags))
	for _, tag := range commonStoreTags {
		name := tag.english
		if language == "schinese" {
			name = tag.chinese
		}
		tags = append(tags, discoveryTag{tag.id, name})
	}
	return tags
}

func (d *steamDiscovery) catalog(ctx context.Context, language string) reading[[]discoveryTag] {
	return d.tags.slot(language, d.now).read(ctx, language, tagCatalogTTL, false, func(ctx context.Context) (*[]discoveryTag, problem) {
		var reply []discoveryTag
		if p := d.get(ctx, tagCatalogTimeout, tagCatalogBytes, storeBase+tagCatalogPath+language, nil, &reply); p != "" {
			return nil, p
		}
		tags := make([]discoveryTag, 0, len(reply))
		seen := make(map[uint32]struct{}, len(reply))
		for _, tag := range reply {
			tag.Name = cleanText(redactCredentials(tag.Name), tagNameRunes)
			if _, ok := seen[tag.ID]; tag.ID == 0 || tag.Name == "" || ok {
				continue
			}
			seen[tag.ID] = struct{}{}
			tags = append(tags, tag)
		}
		if len(tags) == 0 {
			return nil, problemResponse
		}
		return &tags, ""
	})
}

func (d *steamDiscovery) listTags(ctx context.Context, raw json.RawMessage, fallback string, limit int, t *text, out map[string]any) error {
	var args struct {
		discoveryOptions
		Query string `json:"query"`
	}
	if !decodeArgs(raw, &args) || utf8.RuneCountInString(args.Query) > maxQueryRunes {
		return invalid(t.message(problemDiscoveryTagArgs, false))
	}
	language, validLanguage := discoveryLanguage(args.Language, fallback)
	limit, validLimit := args.resultLimit(limit)
	if !validLanguage || !validLimit {
		return invalid(t.message(problemDiscoveryTagArgs, false))
	}
	tags := d.catalog(ctx, language)
	describeReading(out, "tags", tags, t)
	source, actualLanguage := "steam_store", language
	catalog := []discoveryTag(nil)
	if tags.Data != nil {
		catalog = *tags.Data
	} else {
		source, actualLanguage = "built_in", builtInTagLanguage(language)
		catalog = builtInTags(actualLanguage)
		describeReading(out, "tags", reading[[]discoveryTag]{Issue: problemBuiltInTags}, t)
	}
	query := tagNameKey(args.Query)
	matches := make([]discoveryTag, 0, limit)
	total := 0
	for _, tag := range catalog {
		if strings.Contains(tagNameKey(tag.Name), query) || appIDText(tag.ID) == query {
			total++
			if len(matches) < limit {
				matches = append(matches, tag)
			}
		}
	}
	out["available"], out["tags"], out["source"] = true, matches, source
	out["language"], out["matched_count"] = actualLanguage, total
	return nil
}
