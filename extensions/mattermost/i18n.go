package main

import "strings"

type texts struct {
	inbox, unread, noUnread, channels, noChannels, messages, noMessages, more, back, missingConfig string
}

var locales = map[string]*texts{
	"en": {
		inbox: "Mattermost unread", unread: "%d unread · %d mentions", noUnread: "No unread channels.", channels: "Channels", noChannels: "No accessible channels.", messages: "Messages", noMessages: "No messages.", more: "Older messages", back: "Back", missingConfig: "Set the Mattermost instance URL and personal access token in Settings › Extensions first.",
	},
	"ja": {
		inbox: "Mattermost 未読", unread: "未読 %d 件・メンション %d 件", noUnread: "未読チャンネルはありません。", channels: "チャンネル", noChannels: "アクセス可能なチャンネルはありません。", messages: "メッセージ", noMessages: "メッセージはありません。", more: "過去のメッセージ", back: "戻る", missingConfig: "設定 › 拡張機能で Mattermost のインスタンス URL と個人アクセストークンを設定してください。",
	},
	"zh-Hans": {
		inbox: "Mattermost 未读消息", unread: "%d 条未读 · %d 条提及", noUnread: "没有未读频道。", channels: "频道", noChannels: "没有可访问的频道。", messages: "消息", noMessages: "暂无消息。", more: "更早的消息", back: "返回", missingConfig: "先在「设置 › 扩展」中填写 Mattermost 实例地址和个人访问令牌。",
	},
}

func textsFor(locale string) *texts {
	if t := locales[locale]; t != nil {
		return t
	}
	if i := strings.IndexAny(locale, "-_"); i > 0 {
		if t := locales[locale[:i]]; t != nil {
			return t
		}
	}
	return locales["en"]
}
