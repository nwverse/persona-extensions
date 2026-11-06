package x

import "strings"

// text is what the connector says to people (status, sign-in, inbox rows) in one language: the hub's locale
// (initialize's hub.locale), Chinese when it says none.
type text struct {
	notSignedIn, notSignedInGuide                           string
	unauthorized                                            string
	unauthorizedGuide                                       []string
	credits                                                 string
	creditsGuide                                            []string
	rateLimited                                             string // %s: the time it resets
	failed                                                  string // %s: what went wrong
	apiKey, apiSecret, accessToken, accessTokenSecret, hint string
	missing, wrongKeys, noCredits, unreachable              string
	mentionLabel, mentionWhy, mentionTitle                  string // mentionTitle %s: @username
}

func texts(locale string) text {
	switch {
	case strings.HasPrefix(locale, "en"):
		return text{
			notSignedIn:      "Not signed in to X",
			notSignedInGuide: "Sign in with your own X developer app's keys (Settings › Connectors › X).",
			unauthorized:     "X refused the keys: they were revoked, regenerated or mistyped.",
			unauthorizedGuide: []string{"In console.x.com, open your app's Keys and tokens.",
				"Regenerate the Access Token and Secret for your account (Read permission).", "Sign in to X here again with the new keys."},
			credits: "X refused the request for your developer account: no credits left, or the app is not enrolled for the X API.",
			creditsGuide: []string{"In console.x.com, check your app belongs to a project with pay-per-use access.",
				"Add credits (Billing) or raise your spending limit; reading resumes on the next sync."},
			rateLimited: "X's rate limit was reached; reading resumes after %s.",
			failed:      "Could not read X: %s",
			apiKey:      "API Key", apiSecret: "API Key Secret", accessToken: "Access Token", accessTokenSecret: "Access Token Secret",
			hint: "Create a developer app at console.x.com, buy pay-per-use credits and set the app's permission to Read. Then, under Keys and tokens, " +
				"copy the API Key and Secret and generate the Access Token and Secret for your own account: you are the app's owner. The keys stay sealed on the hub.",
			missing: "Fill in all four values.", wrongKeys: "X did not accept these keys. Check them, or regenerate the Access Token and Secret.",
			noCredits:    "X accepted the keys but refused the request: add credits in console.x.com, or check the app is enrolled for the X API.",
			unreachable:  "Could not reach X. Check the hub's network or the proxy setting.",
			mentionLabel: "Mention", mentionWhy: "Rule: someone mentioned you or replied to you on X", mentionTitle: "%s mentioned you on X",
		}
	case strings.HasPrefix(locale, "ja"):
		return text{
			notSignedIn:      "X にサインインしていません",
			notSignedInGuide: "自分の X 開発者アプリのキーでサインインしてください（設定 › コネクタ › X）。",
			unauthorized:     "X がキーを受け付けませんでした。取り消された、再生成された、または入力に誤りがあります。",
			unauthorizedGuide: []string{"console.x.com でアプリの Keys and tokens を開きます。",
				"自分のアカウントの Access Token と Secret を再生成します（権限は Read）。", "新しいキーでここからもう一度 X にサインインします。"},
			credits: "X が開発者アカウントのリクエストを拒否しました。クレジットが残っていないか、アプリが X API に登録されていません。",
			creditsGuide: []string{"console.x.com で、アプリが従量課金のプロジェクトに属しているか確認します。",
				"クレジットを追加する（Billing）か、利用上限を上げます。次の同期から読み込みを再開します。"},
			rateLimited: "X のレート制限に達しました。%s 以降に再開します。",
			failed:      "X を読み込めませんでした：%s",
			apiKey:      "API Key", apiSecret: "API Key Secret", accessToken: "Access Token", accessTokenSecret: "Access Token Secret",
			hint: "console.x.com で開発者アプリを作り、従量課金のクレジットを購入して、アプリの権限を Read にします。Keys and tokens で API Key と Secret をコピーし、" +
				"自分のアカウントの Access Token と Secret を生成してください（あなたがアプリのオーナーです）。キーは hub で暗号化して保存されます。",
			missing: "4 つの値をすべて入力してください。", wrongKeys: "X がこのキーを受け付けませんでした。確認するか、Access Token と Secret を再生成してください。",
			noCredits:    "キーは有効ですが X がリクエストを拒否しました。console.x.com でクレジットを追加するか、アプリが X API に登録されているか確認してください。",
			unreachable:  "X に接続できませんでした。hub のネットワークかプロキシ設定を確認してください。",
			mentionLabel: "メンション", mentionWhy: "ルール：X であなたへのメンションか返信", mentionTitle: "%s が X であなたをメンションしました",
		}
	}
	return text{
		notSignedIn:      "还没有登录 X",
		notSignedInGuide: "用你自己的 X 开发者应用的密钥登录（设置 › 连接器 › X）。",
		unauthorized:     "X 不认这组密钥：可能被撤销、重新生成过，或者填错了。",
		unauthorizedGuide: []string{"在 console.x.com 打开你的应用的 Keys and tokens。",
			"给你自己的账号重新生成 Access Token 和 Secret（权限选 Read）。", "在这里用新的密钥重新登录 X。"},
		credits: "X 拒绝了你的开发者账号的请求：额度用完了，或者应用没有开通 X API。",
		creditsGuide: []string{"在 console.x.com 确认应用属于一个按量付费的项目。",
			"充值额度（Billing）或调高消费上限；下一次同步会接着读。"},
		rateLimited: "X 的请求频率到上限了，%s 之后接着读。",
		failed:      "读不到 X：%s",
		apiKey:      "API Key", apiSecret: "API Key Secret", accessToken: "Access Token", accessTokenSecret: "Access Token Secret",
		hint: "在 console.x.com 建一个开发者应用，购买按量付费的额度，把应用权限设成 Read；然后在 Keys and tokens 里复制 API Key 和 Secret，" +
			"再给你自己的账号生成 Access Token 和 Secret（你就是这个应用的所有者）。密钥加密存在 hub 上。",
		missing: "四项都要填。", wrongKeys: "X 不认这组密钥。检查一下，或者重新生成 Access Token 和 Secret。",
		noCredits:    "密钥是对的，但 X 拒绝了请求：在 console.x.com 充值额度，或确认应用开通了 X API。",
		unreachable:  "连不上 X。检查 hub 的网络或者代理设置。",
		mentionLabel: "@我", mentionWhy: "规则：X 上有人 @ 了你或回复了你", mentionTitle: "%s 在 X 上 @ 了你",
	}
}
