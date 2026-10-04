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

Configuration file: /etc/jevtri/jevtri.conf (default).
Use -c, --config FILE to choose another file.

  -t, --time TIME      incident time (HH:MM[:SS] or YYYY-MM-DD HH:MM[:SS]); default now
  -m, --minutes N      window in minutes (default from config, 5)
  -i, --issue TEXT     symptom, e.g. "The website returns 502"
  -c, --config FILE    configuration file (default /etc/jevtri/jevtri.conf)
  -v, --verbose        show details
  -j, --json           JSON output
      --show           show the configuration path and active settings; send nothing
                       mask patterns are hidden; only their count is shown
                       supports --json and -c FILE
      --dry-run        show what would be sent; send nothing
      --lang LANG      language for --help and --show: en, ja or zh-CN
      --version        show the version
      --config-update  look for new logs and containers and ask whether
                       to add each to the configuration
                       then choose registrations to remove; files are kept by default
                       Linux can delete safe host logs after separate confirmation
                       Docker logs or unknown Docker state prevent file deletion
      --group NAMES    rank named groups, system and ungrouped logs directly;
                       names separated by commas; repeatable
      --all-groups     rank all configured logs directly;
                       cannot be combined with --group
                       without either option, Jev first chooses groups when
                       two or more service groups have lines in the window
  -h, --help           show this help

  init                 choose the logs and write the configuration
  report               describe the real cause of a past run, for an issue on
                       GitHub; sends nothing

Docker Compose logs are grouped by Compose project. Registration reads the
actual project label and saves each container's verified path and group;
standalone containers use their names, and custom groups are also supported.
After adding or recreating containers or changing storage, run --config-update.
Start with jevtri, then use --group shop to examine the registered project.
Use --group shop,billing for several groups or --all-groups for every log.

The priority (0-100) ranks where to look first. It is not the probability
that a log holds the cause.

Lines in the window are masked and sent to the Jev API (api.typesafe.ai);
IP addresses and host names are not masked. Check with --dry-run. Every run
is recorded in /var/log/jevtri/sent.log. Run as root.

Exit status: 0 done; 1 configuration or API key error, or no readable log;
2 some logs not evaluated; 3 Jev could not be reached.

Show the configuration: jevtri --show
Use another file: jevtri --show -c /path/to/jevtri.conf

See jevtri(1).
`,
	"ja": `使い方: jevtri [オプション]
        jevtri init
        jevtri --config-update
        jevtri report

設定したログを、最初に調べる価値の高い順に並べます。

設定ファイル: /etc/jevtri/jevtri.conf（既定）。
-c, --config FILE で別のファイルを指定できます。

  -t, --time TIME      障害の時刻（HH:MM[:SS] か YYYY-MM-DD HH:MM[:SS]）。既定は現在
  -m, --minutes N      見る範囲の分数（既定は設定の値、5）
  -i, --issue TEXT     症状。例: "Web サイトが 502 を返す"
  -c, --config FILE    設定ファイル（既定は /etc/jevtri/jevtri.conf）
  -v, --verbose        詳細を表示する
  -j, --json           JSON で出力する
      --show           設定の場所と有効な設定を表示し、何も送らない
                       マスクのパターンは表示せず、件数だけ表示する
                       --json と -c FILE を利用できる
      --dry-run        送る内容を表示し、何も送らない
      --lang LANG      --help と --show の言語: en、ja、zh-CN
      --version        版数を表示する
      --config-update  新しいログやコンテナを探し、設定に加えるか
                       1件ずつ尋ねる
                       最後に不要な登録を選んで削除できる。ログは既定で保持
                       Linuxでは安全なホストログを別の確認で削除できる
                       Docker管理ログやDocker状態不明時は実ログを削除しない
      --group NAMES    指定グループ・system・未設定のログを直接評価する。
                       カンマ区切りで複数指定でき、繰り返し指定も可
      --all-groups     設定したすべてのログを直接評価する。
                       --group との併用は不可
                       両方とも無ければ、時間帯に行のあるサービスグループが
                       2つ以上のとき Jev が先にグループを選ぶ
  -h, --help           このヘルプを表示する

  init                 ログを選び、設定ファイルを書く
  report               過去の実行の本当の原因を報告する文書を作る（GitHub へ
                       投稿するため）。何も送らない

Docker Compose のログは Composeプロジェクト単位です。登録時に実際の
プロジェクトラベルを読み、各コンテナの検証済みパスとグループを保存します。
単独コンテナはその名前を使い、独自グループも設定できます。
コンテナの追加・再作成や保存場所の変更後は --config-update を実行してください。
まず jevtri で対象を絞り、--group shop で登録済みプロジェクトを詳しく調べます。
複数なら --group shop,billing、全ログなら --all-groups を使います。

優先度（0〜100）は、最初に調べる順番の目安です。そのログに原因がある確率では
ありません。

対象時間帯の行は伏せ字にしたうえで Jev の API（api.typesafe.ai）へ送ります。
IP アドレスとホスト名は伏せ字にしません。--dry-run で確かめてください。
各実行は /var/log/jevtri/sent.log に記録されます。root で実行してください。

終了コード: 0 完了、1 設定・API キーの誤りまたは読めるログが無い、
2 評価できなかったログがある、3 Jev に接続できない。

設定を表示: jevtri --show
別のファイルを表示: jevtri --show -c /path/to/jevtri.conf

詳しくは jevtri(1) を参照してください。
`,
	"zh-CN": `用法: jevtri [选项]
      jevtri init
      jevtri --config-update
      jevtri report

按照最值得优先检查的顺序，对已配置的日志进行排序。

配置文件: /etc/jevtri/jevtri.conf（默认）。
可用 -c, --config FILE 指定其他文件。

  -t, --time TIME      故障时间（HH:MM[:SS] 或 YYYY-MM-DD HH:MM[:SS]）；默认为当前时间
  -m, --minutes N      查看范围的分钟数（默认为配置中的值，5）
  -i, --issue TEXT     症状，例如 "网站返回 502"
  -c, --config FILE    配置文件（默认为 /etc/jevtri/jevtri.conf）
  -v, --verbose        显示详细信息
  -j, --json           以 JSON 格式输出
      --show           显示配置位置和有效设置，不发送任何内容
                       不显示脱敏规则内容，仅显示数量
                       支持 --json 和 -c FILE
      --dry-run        显示将要发送的内容，但不发送
      --lang LANG      --help 和 --show 的语言: en、ja 或 zh-CN
      --version        显示版本
      --config-update  查找新的日志和容器，并逐个询问是否
                       加入配置文件
                       最后可选择删除不需要的注册项；默认保留日志文件
                       Linux可在单独确认后删除安全的主机日志
                       Docker管理日志或Docker状态未知时拒绝删除文件
      --group NAMES    直接评估指定组、system 和未分组的日志；
                       组名用逗号分隔，也可重复指定
      --all-groups     直接评估所有已配置的日志；
                       不能与 --group 同时使用
                       两者均未指定时，如果时间范围内有日志行的服务组
                       有两个以上，Jev 会先选出组
  -h, --help           显示本帮助

  init                 选择日志并写入配置文件
  report               为过去的某次运行编写真正原因的报告（用于在 GitHub
                       上提交）；不发送任何内容

Docker Compose 日志按 Compose 项目分组。注册时读取实际的项目标签，
保存每个容器经过验证的路径和组名；独立容器使用容器名，也支持自定义组。
添加或重新创建容器，或更改存储位置后，请运行 --config-update。
先运行 jevtri 缩小范围，再用 --group shop 详细检查已注册的项目。
多个组用 --group shop,billing，所有日志用 --all-groups。

优先级（0-100）表示应先检查哪个日志，并不是该日志包含故障原因的概率。

时间范围内的日志行在脱敏后发送到 Jev API（api.typesafe.ai）。
IP 地址和主机名不会被脱敏。请用 --dry-run 确认。每次运行都会记录在
/var/log/jevtri/sent.log 中。请以 root 身份运行。

退出状态: 0 完成；1 配置或 API 密钥错误，或没有可读取的日志；
2 部分日志无法评估；3 无法连接 Jev。

显示配置: jevtri --show
显示其他文件: jevtri --show -c /path/to/jevtri.conf

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
