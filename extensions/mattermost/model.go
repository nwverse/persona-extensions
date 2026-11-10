package main

import "regexp"

type user struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
}

type team struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type channel struct {
	ID          string `json:"id"`
	TeamID      string `json:"team_id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	Name        string `json:"name"`
	DeleteAt    int64  `json:"delete_at"`
}

type unread struct {
	TeamID       string `json:"team_id"`
	ChannelID    string `json:"channel_id"`
	MessageCount int    `json:"msg_count"`
	MentionCount int    `json:"mention_count"`
}

type post struct {
	ID        string `json:"id"`
	CreateAt  int64  `json:"create_at"`
	UpdateAt  int64  `json:"update_at"`
	DeleteAt  int64  `json:"delete_at"`
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
	RootID    string `json:"root_id"`
	Message   string `json:"message"`
	Type      string `json:"type"`
}

type postList struct {
	Order      []string        `json:"order"`
	Posts      map[string]post `json:"posts"`
	NextPostID string          `json:"next_post_id"`
	PrevPostID string          `json:"prev_post_id"`
	HasNext    bool            `json:"has_next"`
}

type unreadChannel struct {
	Channel channel
	Unread  unread
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validID(id string) bool { return idPattern.MatchString(id) }
