// Command water keeps drink records and sends deterministic reminders through the hub.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata"

	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

//go:embed persona-extension.json
var authorManifest []byte
var manifest = pluginkit.PackagedManifest(authorManifest)

type app struct {
	mu   sync.Mutex
	cfg  config
	s    state
	path string
	hub  *hubClient
	now  func() time.Time
}

func openApp(path string, c config, h *hubClient, now time.Time) (*app, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	s, err := load(path, now)
	if err != nil {
		return nil, err
	}
	if err = save(path, s); err != nil {
		return nil, err
	}
	return &app{cfg: c, s: s, path: path, hub: h, now: time.Now}, nil
}

// update commits a private copy, so failed persistence never reports a successful action.
// Callers hold mu; network requests and UI actions share that lock.
func (a *app) update(now time.Time, fn func(*state)) error {
	s := a.s.clone()
	fn(&s)
	s.prune(now)
	if err := save(a.path, s); err != nil {
		return err
	}
	a.s = s
	return nil
}

func program() ext.Program {
	var m struct {
		Contributes map[string]any `json:"contributes"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		panic(err)
	}
	var current atomic.Pointer[app]
	get := func() (*app, error) {
		a := current.Load()
		if a == nil {
			return nil, fmt.Errorf("喝水提醒还没准备好")
		}
		return a, nil
	}
	return ext.Program{ID: "water", Version: "0.1.0", Manifest: manifest,
		Init: func(ctx context.Context, in *ext.Init, _ *ext.Hub) (map[string]any, error) {
			c := defaults()
			if err := in.Config(&c); err != nil {
				return nil, err
			}
			if in.Info().DataDir == "" {
				return nil, fmt.Errorf("没有可用的记录目录")
			}
			a, err := openApp(filepath.Join(in.Info().DataDir, "water.json"), c, newHub(in.Hub.URL, in.Hub.Token, in.Hub.Scopes), time.Now())
			if err != nil {
				return nil, err
			}
			current.Store(a)
			go a.run(ctx)
			return m.Contributes, nil
		}, UI: map[string]ext.Page{"water": {
			Render: func(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
				a, err := get()
				if err != nil {
					return nil, err
				}
				return a.render(ctx, r)
			},
			Action: func(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
				a, err := get()
				if err != nil {
					return nil, err
				}
				return a.action(ctx, r)
			},
		}}}
}

func (a *app) run(ctx context.Context) {
	t := time.NewTimer(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.tick(ctx, a.now()); err != nil && ctx.Err() == nil {
				log.Printf("喝水提醒：%v", err)
			}
			t.Reset(30 * time.Second)
		}
	}
}

func main() { log.SetFlags(0); ext.Main(program()) }
