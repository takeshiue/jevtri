package main

import (
	"fmt"
	"io"
	"strings"
)

// Help is translated because the tool is hard to use without it (spec:
// multilingual help and man page). Error messages stay in English so that
// they can be searched for.
var helpTexts = map[string]string{
	"en": `Usage: jevtri [options]
       jevtri init
       jevtri --config-update
       jevtri report

Rank the configured logs by how worth they are to examine first.

  -t, --time TIME      incident time (HH:MM[:SS] or YYYY-MM-DD HH:MM[:SS]); default now
  -m, --minutes N      window in minutes (default from config, 5)
  -i, --issue TEXT     symptom, e.g. "The website returns 502"
  -c, --config FILE    configuration file (default /etc/jevtri/jevtri.conf)
  -v, --verbose        show details
  -j, --json           JSON output
      --dry-run        show what would be sent; send nothing
      --lang LANG      language of this help: en, ja or zh-CN
      --version        show the version
      --config-update  look for new logs and containers and ask whether
                       to add each to the configuration
      --group NAME     rank only this group and the system logs (repeatable);
                       without it, Jev first chooses the group when there are
                       two or more
  -h, --help           show this help

  init                 choose the logs and write the configuration
  report               describe the real cause of a past run, for an issue on
                       GitHub; sends nothing

The priority (0-100) ranks where to look first. It is not the probability
that a log holds the cause.

Lines in the window are masked and sent to the Jev API (api.typesafe.ai);
IP addresses and host names are not masked. Check with --dry-run. Every run
is recorded in /var/log/jevtri/sent.log. Run as root.

Exit status: 0 done; 1 configuration or API key error, or no readable log;
2 some logs not evaluated; 3 Jev could not be reached.

See jevtri(1).
`,
	"ja": `使い方: jevtri [オプション]
        jevtri init
        jevtri --config-update
        jevtri report

設定したログを、最初に調べる価値の高い順に並べます。

  -t, --time TIME      障害の時刻（HH:MM[:SS] か YYYY-MM-DD HH:MM[:SS]）。既定は現在
  -m, --minutes N      見る範囲の分数（既定は設定の値、5）
  -i, --issue TEXT     症状。例: "Web サイトが 502 を返す"
  -c, --config FILE    設定ファイル（既定は /etc/jevtri/jevtri.conf）
  -v, --verbose        詳細を表示する
  -j, --json           JSON で出力する
      --dry-run        送る内容を表示し、何も送らない
      --lang LANG      このヘルプの言語: en、ja、zh-CN
      --version        版数を表示する
      --config-update  新しいログやコンテナを探し、設定に加えるか
                       1件ずつ尋ねる
      --group NAME     このグループと system のログだけを順位づけする（複数可）。
                       無ければ、グループが2つ以上のとき Jev が先にグループを選ぶ
  -h, --help           このヘルプを表示する

  init                 ログを選び、設定ファイルを書く
  report               過去の実行の本当の原因を報告する文書を作る（GitHub へ
                       投稿するため）。何も送らない

優先度（0〜100）は、最初に調べる順番の目安です。そのログに原因がある確率では
ありません。

対象時間帯の行は伏せ字にしたうえで Jev の API（api.typesafe.ai）へ送ります。
IP アドレスとホスト名は伏せ字にしません。--dry-run で確かめてください。
各実行は /var/log/jevtri/sent.log に記録されます。root で実行してください。

終了コード: 0 完了、1 設定・API キーの誤りまたは読めるログが無い、
2 評価できなかったログがある、3 Jev に接続できない。

詳しくは jevtri(1) を参照してください。
`,
	"zh-CN": `用法: jevtri [选项]
      jevtri init
      jevtri --config-update
      jevtri report

按照最值得优先检查的顺序，对已配置的日志进行排序。

  -t, --time TIME      故障时间（HH:MM[:SS] 或 YYYY-MM-DD HH:MM[:SS]）；默认为当前时间
  -m, --minutes N      查看范围的分钟数（默认为配置中的值，5）
  -i, --issue TEXT     症状，例如 "网站返回 502"
  -c, --config FILE    配置文件（默认为 /etc/jevtri/jevtri.conf）
  -v, --verbose        显示详细信息
  -j, --json           以 JSON 格式输出
      --dry-run        显示将要发送的内容，但不发送
      --lang LANG      本帮助的语言: en、ja 或 zh-CN
      --version        显示版本
      --config-update  查找新的日志和容器，并逐个询问是否
                       加入配置文件
      --group NAME     仅对该组和 system 日志排序（可重复）；未指定时，
                       若有两个以上的组，Jev 会先选出组
  -h, --help           显示本帮助

  init                 选择日志并写入配置文件
  report               为过去的某次运行编写真正原因的报告（用于在 GitHub
                       上提交）；不发送任何内容

优先级（0-100）表示应先检查哪个日志，并不是该日志包含故障原因的概率。

时间范围内的日志行在脱敏后发送到 Jev API（api.typesafe.ai）。
IP 地址和主机名不会被脱敏。请用 --dry-run 确认。每次运行都会记录在
/var/log/jevtri/sent.log 中。请以 root 身份运行。

退出状态: 0 完成；1 配置或 API 密钥错误，或没有可读取的日志；
2 部分日志无法评估；3 无法连接 Jev。

详见 jevtri(1)。
`,
}

// helpLanguage picks the help language: --lang first, then the locale the way
// gettext reads it (LC_ALL, then LC_MESSAGES, then LANG; the first non-empty
// one wins).
func helpLanguage(lang string, getenv func(string) string) (string, error) {
	if lang != "" {
		if _, ok := helpTexts[lang]; !ok {
			return "", fmt.Errorf("unknown language %q for --lang (en, ja or zh-CN)", lang)
		}
		return lang, nil
	}
	locale := ""
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := getenv(name); value != "" {
			locale = value
			break
		}
	}
	switch {
	case strings.HasPrefix(locale, "ja"):
		return "ja", nil
	case strings.HasPrefix(locale, "zh_CN"), strings.HasPrefix(locale, "zh_SG"), strings.HasPrefix(locale, "zh_Hans"):
		return "zh-CN", nil
	}
	return "en", nil
}

func printHelp(w io.Writer, lang string, getenv func(string) string) error {
	chosen, err := helpLanguage(lang, getenv)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(w, helpTexts[chosen])
	return err
}
