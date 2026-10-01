# jevtri

[English](README.md) | 日本語 | [简体中文](README.zh-CN.md)

jevtri は、Linux サーバーで障害が起きたとき、最初にどのログを読むかを決める手助けをします。
設定したログから障害時刻の前後の行を集め、機密を伏せ字にしたうえで、
[Jev](https://docs.typesafe.ai) の API に各ログを調べる価値を判定させます。

```
$ sudo jevtri -t 03:02 -i "Web サイトが 502 を返す"
Reference time: 2026-09-28 03:02:00 JST
Window:         02:57:00 .. 03:07:00 (5 min)
Symptom:        Web サイトが 502 を返す

Investigation priority (0-100; not the probability of being the cause):
   1. /var/log/nginx/error.log   97
   2. /var/log/messages          33
   3. /var/log/secure             0

Start with: /var/log/nginx/error.log
```

数字は**調査優先度**で、そのログに原因がある確率ではありません。jevtri が示すのは
どこから調べ始めるかのおすすめです。原因の特定、復旧、サーバーの監視は行いません。
障害によっては、どのログにも痕跡が残りません。全てのログの値が低い場合は、その旨を示し、
設定していないログ、アプリケーション、ほかのホストを確かめるよう案内します。

結果の表示とエラーメッセージは英語です。

## 導入

x86_64 と arm64 のパッケージを、`SHA256SUMS` とともに
[リリースのページ](https://github.com/takeshiue/jevtri/releases)に置いています。
`SHA256SUMS` には、このリポジトリとリリースのページにある鍵 `jevtri-signing-key.asc`
で署名（`SHA256SUMS.asc`）しています。鍵のフィンガープリントは
`EE2D 0814 C5CE 3F1F 76CE  4B2E B376 0451 3E19 3961` です。

```sh
gpg --import jevtri-signing-key.asc
gpg --verify SHA256SUMS.asc SHA256SUMS
sha256sum -c --ignore-missing SHA256SUMS
# AlmaLinux、Rocky Linux、RHEL 8・9・10
sudo dnf install ./jevtri_0.1.0_x86_64.rpm
# Ubuntu 22.04・24.04、Debian 12
sudo apt install ./jevtri_0.1.0_amd64.deb
```

パッケージは x86_64 の AlmaLinux 8・9・10、Ubuntu 22.04・24.04、Debian 12 で試験しています。
arm64 のパッケージは同じ方法で作っていますが、arm64 のサーバーでは試験していません。

パッケージが入れるのは `/usr/bin/jevtri`、`/usr/share/jevtri/` の設定の見本、man ページ、
`/etc/logrotate.d/jevtri` です。`/etc/jevtri/jevtri.conf` は入れません。そのサーバー向けの
設定は `jevtri init` が書きます。パッケージを削除しても、設定、API キー、送信記録は残ります。

## 初期設定

1. Jev の API キーを、`/etc/jevtri/api-key` に1行だけ書きます。

   ```sh
   sudo install -m 0600 -o root -g root /dev/null /etc/jevtri/api-key
   sudo vi /etc/jevtri/api-key
   ```

   このファイルが無い場合や、権限が `0600` より緩い場合、jevtri は何も送りません。

2. 設定を書きます。

   ```sh
   sudo jevtri init
   ```

   `init` は、このサーバーにある既知のログ（`/var/log/messages`、`/var/log/nginx/error.log`、
   PostgreSQL や Tomcat のログなど）を一覧にし、番号で選ばせ、各ログの時刻の形式を末尾の行で
   確かめます。ほかのパスも追加できます。jevtri が時刻の形式を知らないログは、先頭の10行
   （伏せ字にしたもの）を Jev へ送って形式を判定させてよいか尋ねます。送った場合も、その形式で
   実際に行を読めたときだけ登録します。

   設定が無い状態で `jevtri` を実行すると、端末からなら `init` が始まり、端末でない場合
   （cron、systemd タイマー、パイプ）はエラーで終わります。

ログを読めるよう、jevtri は root で実行します。

## 使い方

```
jevtri [options]
jevtri init
```

| オプション | 意味 |
|---|---|
| `-t`, `--time TIME` | 障害の時刻。`HH:MM[:SS]`（今日。未来になるなら前日）か `YYYY-MM-DD HH:MM[:SS]`。既定は現在 |
| `-m`, `--minutes N` | 見る範囲の分数。`-t` があればその前後、無ければ直前 N 分。既定は 5（設定の `minutes`） |
| `-i`, `--issue TEXT` | 症状。言語は問いません。例: `"Web サイトが 502 を返す"` |
| `-c`, `--config FILE` | 設定ファイル。既定は `/etc/jevtri/jevtri.conf` |
| `-v`, `--verbose` | 対象時間帯、ログごとの行数とバイト数、伏せ字の数、Jev の生の値も表示する |
| `-j`, `--json` | JSON で出力する。`priority` は 0〜1 の値 |
| `--dry-run` | 送る内容をそのまま表示し、何も送らない |
| `--lang LANG` | `--help` の言語: `en`、`ja`、`zh-CN` |
| `--version`, `-h`, `--help` | 版数とヘルプ |

最初は `--dry-run` で、サーバーの外へ出る内容を確かめてください。

cron や systemd タイマーからも実行できます。`-t` が無いときは直前 `-m` 分を見るので、
間隔と揃えます。10分おきなら `-m 10` です。

### 終了コード

| コード | 意味 |
|---|---|
| 0 | 完了。時間帯に行のあるログが1つも無い場合も 0（何も送らない） |
| 1 | 設定や API キーの誤り、または読めるログが1つも無い。何も送らない |
| 2 | 完了したが、評価できなかったログがある（読めない、時刻が見つからない）。それらは順位を付けずに示す |
| 3 | Jev に接続できない、または要求を拒まれた。優先度は作らない |

## 実際の事例の報告

障害の本当の原因が分かったら、`sudo jevtri report` で、送信記録にあるその実行から
報告を作れます。実行と、本当の原因が出ていたログを選んでください。報告は送信記録と
同じ場所に `0600` で作られ、ホスト名、人のユーザー名（UID 1000 以上）、IP アドレスは `[host-1]` のような
記号に置き換わります。何も送りません。表示されるリンクの Issue フォームから投稿してください。
Issue は公開されるため、ほかのホスト名などは先に消してください。報告された事例は試験になり、
版ごとにそのうち何件を正しく並べられるかを公表します。

## 設定

`/etc/jevtri/jevtri.conf` は INI 形式です。誤りは行番号つきで示します。
`/usr/share/jevtri/jevtri.conf.example` も参照してください。

```ini
[general]
minutes = 5
max_bytes = 48000
sent_log = /var/log/jevtri/sent.log

[log nginx-error]
path = /var/log/nginx/error.log
time_format = slash-ymd

[log tomcat]
path = /var/log/tomcat/catalina.*.log
time_format = dmy-month
timezone = Asia/Tokyo
read_compressed = yes
mask = order-\d+
```

| キー | 意味 |
|---|---|
| `minutes` | `-m` の既定値 |
| `max_bytes` | 1回に送るログの行のバイト数。ログの間で分ける。ERROR・WARN・fail などを含む行と、障害時刻に近い行を優先して残す |
| `sent_log` | 実行ごとの記録を書く場所 |
| `path` | ログ。`*`・`?`・`[` でファイル名に日付の入ったログに一致させられる。時間帯がさかのぼる場合は回転済みのファイル（`.1`、`-20260928`）も読む |
| `time_format` | 下の表の名前か、`%` 記号によるパターン |
| `timezone` | タイムゾーンの書かれていないログのタイムゾーン。既定はサーバーのもの |
| `read_compressed` | `yes` なら回転済みの `.gz` も読む |
| `mask` | 追加で伏せ字にする正規表現。複数書ける |

時刻の形式。秒の小数部とタイムゾーン（`+09:00`、`Z`、`JST`）は有っても無くてもよく、
時刻は行のどこにあってもかまいません。

| 名前 | 例 |
|---|---|
| `syslog` | `Sep 28 07:51:24` |
| `rfc3339` | `2026-09-28T07:29:16.929554+09:00` |
| `iso-space` | `2026-09-28 03:03:07.591 JST` |
| `iso-comma` | `2026-09-28 15:47:01,123` |
| `slash-ymd` | `2026/09/28 03:02:04` |
| `apache-access` | `[28/Sep/2026:09:44:10 +0900]` |
| `apache-error` | `[Mon Sep 28 03:02:10.715837 2026]` |
| `dmy-month` | `28-Sep-2026 09:44:02.397` |
| `slash-mdy` | `09/28/2026 15:47:01` |
| `slash-dmy` | `28/09/2026 15:47:01` |
| `epoch` | `1790532211` |

時刻の無い行（スタックトレースなど）は、直前の行の続きとして扱います。

2つの高速化は、ログが古い順に時刻順で書かれていることを前提とします。8MiB を超える非圧縮のファイルでは、読み取りの開始位置を二分探索で探します。ファイル内で時刻が昇順に並ぶことが前提です。回転済みのファイルは更新日時の新しい順に読み、時間帯より古い行があったファイルで読むのをやめます。古いファイルほど古い時刻であることが前提です。新しい順に書くログ、時刻が乱れたログ、時刻の範囲が重なる回転済みのファイルでは、時間帯の記録を読み飛ばすことがあります。この欠落は、省略の警告に必ずしも表示されません。

1つのログで保持するテキストは、回転済みのファイルを合わせて 64MiB までです。超えた分は時刻の古いものから省き、警告を表示します。

## 何をどこへ送るか

jevtri は TypeSafe のサービスである Jev（`https://api.typesafe.ai/v1/systemone`）へ
データを送ります。ほかへは接続しません。

- **通常の実行**では、時間帯に行のあるログごとに、そのパスと、伏せ字にした行
  （`max_bytes` の範囲内）を送ります。障害の時刻と、`-i` で渡した症状も送ります。行の中の時刻は
  1つのタイムゾーンの表記に揃えます。時間帯に行の無いログは送りません。
- **`jevtri init`** は、時刻の形式が分からないログの先頭10行を、伏せ字にしたうえで、
  同意を得た場合だけ送ります。
- **送る前に伏せ字にするもの:** パスワードなど `キー=値` 形式の秘密、トークン、
  `Authorization` ヘッダー、Cookie、秘密鍵のブロック、URL 中の認証情報、既知の形式のトークン、
  メールアドレスの `@` より前、カード番号、設定の `mask`。伏せ字はパターンによるため、
  見落とすことがあります。
- **伏せ字にしないもの:** IP アドレス、ホスト名、ユーザー名、パス、そのほか行に含まれる全て。
  `--dry-run` で確かめてください。
- **記録:** 各実行（`--dry-run` を含む）の送った内容と応答を、`/var/log/jevtri/sent.log`
  （root だけが読める `0600`）に追記します。API キーは記録しません。logrotate で30日分を残します。

TypeSafe の[プライバシーポリシー](https://typesafe.ai/legal/privacy-policy)（最終更新
2025-11-19、2026-09-27 に確認）によると、入力はモデルの学習・微調整に使われず、委託先を除いて
第三者へ開示されず、依頼に応じて削除されます。保存期間は「合理的に必要な期間」とだけ書かれて
おり、具体的な期間は分かりません。

Jev の料金は入力100万トークンあたり 0.042 米ドルで、出力は無料です
（[models](https://docs.typesafe.ai/models)、2026-09-28 に確認）。1回の実行は 1 セント
よりずっと安くなります。

## ソースからのビルド

Go の標準ライブラリだけを使っています。ビルドと試験にネットワークは要らず、試験は Jev へ
何も送りません。

```sh
go test ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=0.1.0" -o jevtri ./cmd/jevtri
```

## ヘルプとマニュアル

`jevtri --help` と `man jevtri` は英語・日本語・中国語（簡体字）に対応し、`LC_ALL`・
`LC_MESSAGES`・`LANG` で切り替わります。`jevtri --help --lang ja` のように指定もできます。
結果の表示とエラーメッセージは英語です。翻訳の修正を歓迎します。

## ライセンス

MIT。[LICENSE](LICENSE) を参照してください。
