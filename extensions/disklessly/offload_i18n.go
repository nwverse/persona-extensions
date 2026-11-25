package main

import (
	"fmt"
	"strings"
)

// Sentences of the disk_offload point (offload.go) in the caller's language. The pages of this extension are in
// Chinese only; the point is drawn by Persona's own apps, which pass the user's locale with every call, so what it
// returns for people follows that locale: en, zh-Hans or ja, falling back to en.

const (
	locEN = "en"
	locZH = "zh-Hans"
	locJA = "ja"
)

// offloadLocale maps a BCP 47 tag to one of the three tables.
func offloadLocale(tag string) string {
	t := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(tag, "_", "-")))
	switch {
	case t == "zh" || strings.HasPrefix(t, "zh-"):
		return locZH
	case t == "ja" || strings.HasPrefix(t, "ja-"):
		return locJA
	}
	return locEN
}

var offloadText = map[string]map[string]string{
	locEN: {
		"unknown_machine":  "%s is not one of the computers Disklessly manages. Add it on the Machine disk page, or as a Host in ~/.ssh/config on the hub's computer.",
		"offline":          "%s is offline.",
		"unreachable":      "Could not reach %s: %s",
		"windows":          "Offloading to the cloud is not available on Windows computers.",
		"no_cli":           "The disklessly command-line tool is not installed on %s.",
		"cli_old":          "disklessly %s on %s is too old: it cannot offload files and folders yet.",
		"no_backend":       "disklessly on %s has no storage set up yet. Run disklessly init (or disklessly backend add) on that computer first.",
		"unreadable":       "disklessly on %s gave an answer that could not be read: %s",
		"partial":          "Some of what is offloaded could not be listed.",
		"tool_title":       "Disklessly command-line tool",
		"source_brew":      "Installs disklessly-cli from the Homebrew tap nwverse/tap with Homebrew on %s, into Homebrew's own folder; nothing else is changed.",
		"source_copy":      "Copies disklessly %s from %s to ~/.local/opt/%s on %s and links ~/.local/bin/disklessly to it; nothing is downloaded and no administrator rights are needed.",
		"source_none":      "It cannot be installed automatically: %s has no Homebrew and no computer of the same kind has a copy to share. Install it there with: brew tap nwverse/tap && brew install nwverse/tap/disklessly-cli",
		"no_install_route": "disklessly cannot be installed on %s automatically. Install it there with: brew tap nwverse/tap && brew install nwverse/tap/disklessly-cli",
		"busy":             "%s is busy with another Disklessly task; try again when it finishes.",
		"install_failed":   "Installing disklessly failed: %s",
		"still_old":        "disklessly was installed, but the version on %s still cannot offload files and folders.",
		"all_failed":       "Nothing was moved: %s",
		"canceled":         "Canceled; items not started were left as they were.",
		"no_items":         "Nothing to do.",
		"bad_action":       "action must be offload or restore.",
		// Refusals, by code.
		"refusal_bad_path":          "Not an absolute path.",
		"refusal_not_found":         "It does not exist.",
		"refusal_symlink":           "It is a symbolic link; offload what it points to instead.",
		"refusal_not_regular":       "It is not a regular file or folder.",
		"refusal_outside_home":      "Only items in the home folder can be offloaded.",
		"refusal_home_root":         "The home folder itself cannot be offloaded.",
		"refusal_standard_folder":   "Standard folders such as Documents or Library stay; offload what is inside them.",
		"refusal_app_managed":       "Its app manages it (Library, Applications, Trash, Public, app libraries and virtual machine disks stay).",
		"refusal_inside_app":        "It is inside an app bundle.",
		"refusal_own_data":          "It holds Disklessly's own storage, cache or state.",
		"refusal_credentials":       "Keys and credentials stay on the computer.",
		"refusal_placeholder":       "It is already a Disklessly placeholder.",
		"refusal_placeholder_taken": "A Disklessly placeholder with that name is already next to it; bring that one back first.",
		"refusal_already_offloaded": "This app is already offloaded.",
		"refusal_app_platform":      "Apps can be offloaded on Mac computers only.",
		"refusal_not_app":           "It is not an app bundle.",
		"refusal_kind":              "Disklessly does not offload this kind of item.",
		"refusal_unknown_item":      "It is no longer in the cloud inventory; refresh and try again.",
		"refusal_not_restorable":    "It cannot be brought back from this computer.",
		"refusal_tool":              "disklessly refused it: %s",
	},
	locZH: {
		"unknown_machine":           "「%s」不在 Disklessly 管理的电脑里。请在「机器磁盘」页添加，或在 hub 所在电脑的 ~/.ssh/config 里加一个 Host。",
		"offline":                   "「%s」离线。",
		"unreachable":               "连不上「%s」：%s",
		"windows":                   "Windows 电脑暂不支持上云。",
		"no_cli":                    "「%s」上没有安装 disklessly 命令行工具。",
		"cli_old":                   "「%[2]s」上的 disklessly %[1]s 太旧，还不能把文件和文件夹上云。",
		"no_backend":                "「%s」上的 disklessly 还没有设置存储。请先在那台电脑上运行 disklessly init（或 disklessly backend add）。",
		"unreadable":                "「%s」上的 disklessly 回答无法识别：%s",
		"partial":                   "部分已上云的内容没能列出。",
		"tool_title":                "Disklessly 命令行工具",
		"source_brew":               "在「%s」上用 Homebrew 从 nwverse/tap 安装 disklessly-cli，装在 Homebrew 自己的目录里，不改动其他东西。",
		"source_copy":               "把 disklessly %s 从「%s」复制到「%[4]s」的 ~/.local/opt/%[3]s，并把 ~/.local/bin/disklessly 指向它；不从网络下载，也不需要管理员权限。",
		"source_none":               "无法自动安装：「%s」上没有 Homebrew，也没有同类电脑可以复制。请在那台电脑上运行：brew tap nwverse/tap && brew install nwverse/tap/disklessly-cli",
		"no_install_route":          "无法在「%s」上自动安装 disklessly。请在那台电脑上运行：brew tap nwverse/tap && brew install nwverse/tap/disklessly-cli",
		"busy":                      "「%s」正在执行另一个 Disklessly 任务，完成后再试。",
		"install_failed":            "安装 disklessly 失败：%s",
		"still_old":                 "disklessly 已安装，但「%s」上的版本仍不能把文件和文件夹上云。",
		"all_failed":                "没有移动任何内容：%s",
		"canceled":                  "已取消，尚未开始的项目保持原样。",
		"no_items":                  "没有要处理的内容。",
		"bad_action":                "action 只能是 offload 或 restore。",
		"refusal_bad_path":          "不是绝对路径。",
		"refusal_not_found":         "它不存在。",
		"refusal_symlink":           "它是符号链接，请对它指向的内容上云。",
		"refusal_not_regular":       "它不是普通文件或文件夹。",
		"refusal_outside_home":      "只能上云个人文件夹里的内容。",
		"refusal_home_root":         "不能上云整个个人文件夹。",
		"refusal_standard_folder":   "「文稿」「资源库」等标准文件夹保留在本机，请上云其中的内容。",
		"refusal_app_managed":       "它由所属 App 自己管理（资源库、应用程序、废纸篓、公共、App 资料库和虚拟机磁盘都保留在本机）。",
		"refusal_inside_app":        "它在某个 App 包里面。",
		"refusal_own_data":          "它包含 Disklessly 自己的存储、缓存或状态。",
		"refusal_credentials":       "密钥和凭据保留在本机。",
		"refusal_placeholder":       "它已经是 Disklessly 占位文件。",
		"refusal_placeholder_taken": "旁边已有同名的 Disklessly 占位文件，请先取回那一个。",
		"refusal_already_offloaded": "这个 App 已经上云。",
		"refusal_app_platform":      "只有 Mac 电脑上的 App 可以上云。",
		"refusal_not_app":           "它不是 App。",
		"refusal_kind":              "Disklessly 不处理这类项目。",
		"refusal_unknown_item":      "它已不在云端清单里，请刷新后再试。",
		"refusal_not_restorable":    "无法从这台电脑取回它。",
		"refusal_tool":              "disklessly 拒绝了：%s",
	},
	locJA: {
		"unknown_machine":           "%s は Disklessly が管理するコンピュータではありません。「マシンディスク」ページで追加するか、ハブのコンピュータの ~/.ssh/config に Host を追加してください。",
		"offline":                   "%s はオフラインです。",
		"unreachable":               "%s に接続できません：%s",
		"windows":                   "Windows コンピュータではクラウドへの移動を利用できません。",
		"no_cli":                    "%s に disklessly コマンドラインツールがインストールされていません。",
		"cli_old":                   "%[2]s の disklessly %[1]s は古いため、ファイルとフォルダをまだ移動できません。",
		"no_backend":                "%s の disklessly にはまだ保存先が設定されていません。先にそのコンピュータで disklessly init（または disklessly backend add）を実行してください。",
		"unreadable":                "%s の disklessly の応答を読み取れませんでした：%s",
		"partial":                   "クラウドにある項目の一部を一覧にできませんでした。",
		"tool_title":                "Disklessly コマンドラインツール",
		"source_brew":               "%s の Homebrew で nwverse/tap から disklessly-cli を Homebrew 自身のフォルダにインストールします。ほかには何も変更しません。",
		"source_copy":               "disklessly %s を %s から %[4]s の ~/.local/opt/%[3]s にコピーし、~/.local/bin/disklessly をそこにリンクします。ダウンロードも管理者権限も不要です。",
		"source_none":               "自動ではインストールできません：%s には Homebrew がなく、コピー元になる同じ種類のコンピュータもありません。そのコンピュータで次を実行してください：brew tap nwverse/tap && brew install nwverse/tap/disklessly-cli",
		"no_install_route":          "%s には disklessly を自動でインストールできません。そのコンピュータで次を実行してください：brew tap nwverse/tap && brew install nwverse/tap/disklessly-cli",
		"busy":                      "%s は別の Disklessly の処理中です。終わってからもう一度お試しください。",
		"install_failed":            "disklessly のインストールに失敗しました：%s",
		"still_old":                 "disklessly をインストールしましたが、%s のバージョンはまだファイルとフォルダを移動できません。",
		"all_failed":                "何も移動しませんでした：%s",
		"canceled":                  "キャンセルしました。未着手の項目はそのままです。",
		"no_items":                  "処理する項目がありません。",
		"bad_action":                "action は offload か restore です。",
		"refusal_bad_path":          "絶対パスではありません。",
		"refusal_not_found":         "存在しません。",
		"refusal_symlink":           "シンボリックリンクです。リンク先を移動してください。",
		"refusal_not_regular":       "通常のファイルやフォルダではありません。",
		"refusal_outside_home":      "ホームフォルダ内の項目だけを移動できます。",
		"refusal_home_root":         "ホームフォルダそのものは移動できません。",
		"refusal_standard_folder":   "書類やライブラリなどの標準フォルダは残します。中身を移動してください。",
		"refusal_app_managed":       "App 自身が管理しています（ライブラリ、アプリケーション、ゴミ箱、パブリック、App のライブラリや仮想マシンのディスクは残します）。",
		"refusal_inside_app":        "App バンドルの中にあります。",
		"refusal_own_data":          "Disklessly 自身の保存先、キャッシュ、状態を含みます。",
		"refusal_credentials":       "鍵や認証情報はコンピュータに残します。",
		"refusal_placeholder":       "すでに Disklessly のプレースホルダです。",
		"refusal_placeholder_taken": "同じ名前の Disklessly プレースホルダが隣にあります。先にそちらを戻してください。",
		"refusal_already_offloaded": "この App はすでにクラウドにあります。",
		"refusal_app_platform":      "App を移動できるのは Mac だけです。",
		"refusal_not_app":           "App バンドルではありません。",
		"refusal_kind":              "Disklessly はこの種類の項目を扱いません。",
		"refusal_unknown_item":      "クラウドの一覧にもうありません。更新してからもう一度お試しください。",
		"refusal_not_restorable":    "このコンピュータからは戻せません。",
		"refusal_tool":              "disklessly が拒否しました：%s",
	},
}

// otr is the sentence key in locale loc (en when that table lacks it), formatted with args.
func otr(loc, key string, args ...any) string {
	s, ok := offloadText[offloadLocale(loc)][key]
	if !ok {
		s = offloadText[locEN][key]
	}
	if s == "" {
		s = key
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// refusalText is the sentence for refusal code, with the tool's own words for "tool".
func refusalText(loc, code, detail string) string {
	if code == "tool" {
		return otr(loc, "refusal_tool", detail)
	}
	return otr(loc, "refusal_"+code)
}
