package main

import "strings"

type texts struct {
	connection, connected, connectedDetail, checkAgain                   string
	notSetUp, notSetUpDetail, setUpTitle                                 string
	setUpSteps, can                                                      []string
	canTitle, vaultsTitle, vaultsFooter, items, moreVaults, noVaults     string
	noVaultsDetail, unreachable, unreachableGuide, refused, refusedGuide string
	rateLimited, rateLimitedGuide, timedOut                              string
	checkNotToken, checkRefused, says                                    string
	// the secret provider (secrets.go): its errors, and the page's list of uses
	secretNoToken, secretNotFound, secretEmpty, secretInvalid string
	usesTitle, usesFooter, noUses, noUsesDetail               string
	useExtension, useLauncher, useAgent, useOn                string
	bySettings, byRule, byDevice                              string
}

var locales = map[string]*texts{
	"en": {
		connection: "Connection", connected: "Connected",
		connectedDetail: "Service account · vaults it can read: %d", checkAgain: "Check again",
		notSetUp:       "Not connected yet",
		notSetUpDetail: "Add a 1Password service account token in this extension's settings.",
		setUpTitle:     "Set it up",
		setUpSteps: []string{
			"1. Sign in on 1Password.com, select Developer in the sidebar and create a service account.",
			"2. Grant it only the vaults Persona may look into, with read access.",
			"3. Copy the token (it starts with ops_) and paste it into this extension's settings. Persona checks it with 1Password before saving.",
		},
		canTitle: "What the assistant can do",
		can: []string{
			"List the vaults this service account can read, and find items in them by title, tag or website.",
			"See which fields an item has, and each field's op:// reference.",
			"Passwords, keys, notes and one-time codes never reach the assistant, and nothing here can change your vaults.",
		},
		vaultsTitle: "Vaults", vaultsFooter: "Change which vaults these are on 1Password.com, in the service account's settings.",
		items: "%d items", moreVaults: "%d more vaults not shown.", noVaults: "No vaults",
		noVaultsDetail:   "The service account can't read any vault yet. Grant it vaults on 1Password.com.",
		unreachable:      "Can't reach 1Password",
		unreachableGuide: "Check that the hub can reach 1Password, then check again.",
		refused:          "1Password refused the token",
		refusedGuide:     "The token may have been revoked or may have expired. Create a new one and paste it into this extension's settings.",
		rateLimited:      "1Password's rate limit reached",
		rateLimitedGuide: "Service accounts may make a limited number of requests an hour. Try again later.",
		timedOut:         "1Password did not answer in time.",
		says:             "1Password says: %s",
		checkNotToken:    "This is not a 1Password service account token: those start with ops_.",
		checkRefused:     "1Password did not accept this token: %s",
		secretNoToken:    "No 1Password service account token is set: add one in the 1Password extension's settings.",
		secretNotFound:   "1Password has no such vault, item or field for this service account: %s",
		secretEmpty:      "This field is empty in 1Password.",
		secretInvalid:    "This is not a 1Password reference Persona can read: write it as op://vault/item/field.",
		usesTitle:        "Used by Persona",
		usesFooter:       "The uses Persona reported since this extension last started, newest first. A key Persona answers again from its own short memory is not reported twice. Values are never kept here; Persona keeps its own record.",
		noUses:           "Not used yet",
		noUsesDetail:     "Turn on Use a reference in another extension's secret or a model provider's key and enter op://vault/item/field. Persona then asks this extension for the value each time it is used.",
		useExtension:     "Extension secret · %s",
		useLauncher:      "Model provider key · %s",
		useAgent:         "For an Agent run · %s",
		useOn:            " on %s",
		bySettings:       "saved in Settings",
		byRule:           "an Always rule",
		byDevice:         "approved on a device",
	},
	"ja": {
		connection: "接続", connected: "接続済み",
		connectedDetail: "サービスアカウント · 読み取れる保管庫：%d", checkAgain: "もう一度確認",
		notSetUp:       "まだ接続されていません",
		notSetUpDetail: "この拡張機能の設定に 1Password のサービスアカウントトークンを入力してください。",
		setUpTitle:     "設定方法",
		setUpSteps: []string{
			"1. 1Password.com にサインインし、サイドバーの「開発者」からサービスアカウントを作成します。",
			"2. Persona に見せてよい保管庫だけを、読み取り権限で割り当てます。",
			"3. トークン（ops_ で始まります）をコピーし、この拡張機能の設定に貼り付けます。保存前に 1Password で確認します。",
		},
		canTitle: "アシスタントにできること",
		can: []string{
			"このサービスアカウントが読み取れる保管庫を一覧し、タイトル・タグ・ウェブサイトでアイテムを探す。",
			"アイテムにどんなフィールドがあるかと、各フィールドの op:// 参照を見る。",
			"パスワード・キー・メモ・ワンタイムコードがアシスタントに渡ることはなく、保管庫を変更することもありません。",
		},
		vaultsTitle: "保管庫", vaultsFooter: "対象の保管庫は 1Password.com のサービスアカウント設定で変更できます。",
		items: "%d 件のアイテム", moreVaults: "ほかに %d 件の保管庫があります。", noVaults: "保管庫がありません",
		noVaultsDetail:   "サービスアカウントが読み取れる保管庫がまだありません。1Password.com で保管庫を割り当ててください。",
		unreachable:      "1Password に接続できません",
		unreachableGuide: "hub から 1Password に接続できるか確認してから、もう一度確認してください。",
		refused:          "1Password がトークンを拒否しました",
		refusedGuide:     "トークンが取り消されたか期限切れの可能性があります。新しいトークンを作成し、この拡張機能の設定に貼り付けてください。",
		rateLimited:      "1Password のリクエスト上限に達しました",
		rateLimitedGuide: "サービスアカウントは 1 時間あたりのリクエスト数に上限があります。しばらくしてからお試しください。",
		timedOut:         "1Password が時間内に応答しませんでした。",
		says:             "1Password からのメッセージ：%s",
		checkNotToken:    "1Password のサービスアカウントトークンではありません（ops_ で始まります）。",
		checkRefused:     "1Password がこのトークンを受け付けませんでした：%s",
		secretNoToken:    "1Password のサービスアカウントトークンが設定されていません。1Password 拡張機能の設定で入力してください。",
		secretNotFound:   "このサービスアカウントでは、該当する保管庫・アイテム・フィールドが 1Password に見つかりません：%s",
		secretEmpty:      "このフィールドは 1Password で空です。",
		secretInvalid:    "Persona が読める 1Password の参照ではありません。op://保管庫/アイテム/フィールド の形で書いてください。",
		usesTitle:        "Persona による使用",
		usesFooter:       "この拡張機能の前回の起動以降に Persona から報告された使用です（新しい順）。Persona が短時間の記憶から再利用したキーは重ねて報告されません。値はここに保存されません。Persona 側にも記録があります。",
		noUses:           "まだ使用されていません",
		noUsesDetail:     "ほかの拡張機能のシークレットやモデルプロバイダのキーで「参照を使う」をオンにし、op://保管庫/アイテム/フィールド を入力してください。使われるたびに Persona がこの拡張機能に値を問い合わせます。",
		useExtension:     "拡張機能のシークレット · %s",
		useLauncher:      "モデルプロバイダのキー · %s",
		useAgent:         "Agent の実行用 · %s",
		useOn:            "（%s）",
		bySettings:       "設定で保存",
		byRule:           "「常に許可」ルール",
		byDevice:         "デバイスで承認",
	},
	"zh-Hans": {
		connection: "连接", connected: "已连接",
		connectedDetail: "服务账号 · 可读的保险库：%d 个", checkAgain: "重新检查",
		notSetUp:       "还没有连接",
		notSetUpDetail: "在这个扩展的设置里填入 1Password 服务账号令牌。",
		setUpTitle:     "怎么设置",
		setUpSteps: []string{
			"1. 登录 1Password.com，在侧边栏选「开发者」，创建一个服务账号。",
			"2. 只把 Persona 可以查看的保险库授权给它，给「读取」权限即可。",
			"3. 复制令牌（以 ops_ 开头），粘贴到这个扩展的设置里。保存前会先向 1Password 检查一次。",
		},
		canTitle: "助手能做什么",
		can: []string{
			"列出这个服务账号能读的保险库，按标题、标签或网站查找里面的条目。",
			"看一个条目有哪些字段，以及每个字段的 op:// 引用。",
			"密码、密钥、备注和一次性验证码从不交给助手，也不会改动你的保险库。",
		},
		vaultsTitle: "保险库", vaultsFooter: "要换哪些保险库，在 1Password.com 这个服务账号的设置里改。",
		items: "%d 个条目", moreVaults: "还有 %d 个保险库没有列出。", noVaults: "没有保险库",
		noVaultsDetail:   "这个服务账号还读不到任何保险库。去 1Password.com 给它授权保险库。",
		unreachable:      "连不上 1Password",
		unreachableGuide: "确认 hub 能访问 1Password，然后重新检查。",
		refused:          "1Password 拒绝了这个令牌",
		refusedGuide:     "令牌可能已被撤销或已过期。新建一个令牌，粘贴到这个扩展的设置里。",
		rateLimited:      "达到 1Password 的请求上限",
		rateLimitedGuide: "服务账号每小时的请求数有上限，稍后再试。",
		timedOut:         "1Password 没有及时回应。",
		says:             "1Password 的原话：%s",
		checkNotToken:    "这不是 1Password 服务账号令牌（应以 ops_ 开头）。",
		checkRefused:     "1Password 不接受这个令牌：%s",
		secretNoToken:    "还没有填 1Password 服务账号令牌：去 1Password 扩展的设置里填一个。",
		secretNotFound:   "这个服务账号在 1Password 里找不到对应的保险库、条目或字段：%s",
		secretEmpty:      "这个字段在 1Password 里是空的。",
		secretInvalid:    "这不是 Persona 能读的 1Password 引用：写成 op://保险库/条目/字段。",
		usesTitle:        "Persona 的取用",
		usesFooter:       "这个扩展这次启动以来 Persona 报来的取用，最新的在前。Persona 从自己短时间的记忆里再次用到的 Key 不会重复报。值从不在这里保存；Persona 自己也有记录。",
		noUses:           "还没有取用",
		noUsesDetail:     "在别的扩展的密钥或模型服务商的 Key 里打开「使用引用」，填 op://保险库/条目/字段。之后每次用到，Persona 都会向这个扩展取值。",
		useExtension:     "扩展密钥 · %s",
		useLauncher:      "模型服务商 Key · %s",
		useAgent:         "交给 Agent 运行 · %s",
		useOn:            "（%s）",
		bySettings:       "设置里保存的",
		byRule:           "「始终允许」规则",
		byDevice:         "在设备上批准",
	},
}

func textsFor(locale string) *texts {
	if t := locales[locale]; t != nil {
		return t
	}
	if strings.HasPrefix(locale, "zh") {
		return locales["zh-Hans"]
	}
	if i := strings.IndexAny(locale, "-_"); i > 0 {
		if t := locales[locale[:i]]; t != nil {
			return t
		}
	}
	return locales["en"]
}
