package main

import "github.com/nwverse/persona/sdk/go/wire"

// rowSays is what a computer's row on the AI 对话 page says about its state: the row's own
// detail and, for a computer that is not ready, the plain line drawn right under it.
func rowSays(items []wire.Component) string {
	if len(items) == 0 {
		return ""
	}
	s := items[0].Detail
	if len(items) > 1 && items[1].Type == wire.ComponentText {
		s += items[1].Text
	}
	return s
}
