# 判断AIの比較ツール

Jev・Cloudflare Clef・Clef-flashへ同じ合成ログと質問を渡し、どのログを最初に調べるべきかの順位、HTTP応答時間、入力token数、料金推計を比較する開発専用ツール。製品jevtriの設定やパッケージを変更しない。

使い方の正本はこのMarkdownです。ツールは `tools/decisionbench.py`、合成入力は `tools/decisionbench/synthetic.json` にあります。実行結果は各自の出力ディレクトリに保存されます。

## 何を測るか

- 品質：設計した優先度ラベルへのTop1/Top3一致率、nDCG、首位同点件数。
- 速度：送信直前から応答JSONの契約検査完了までのp50/p95・最短・最長。TLS・通信込み。ログ収集・jevtri全体時間を含まない。
- 費用：成功応答の入力token数×入力token単価の推計。無料枠・基本料・失敗時の課金は含まない。warmupも実際には送信するため、warmup込み成功分の推計はJSONに別記する。
- 長文：同じOOMの証拠を約36KBのログの先頭・中央・末尾へ置いた3ケース。位置ごとの順位とtoken数の変化を確認できる。

13ケースはOOM、disk-full、database、dns、tls-expired、permissions、database-lock、service-start、filesystem-readonly、network-linkと、long-oom-begin/middle/end。各ケース6ログ。正解ログと症状ログの掲載順はケースごとに変え、長文3ケースは証拠位置だけを変える。

ラベルは合成ケースを作ったときの暫定評価（無関係0・症状2・直接証拠3）。全ケースに直接証拠があるため、これだけで正常時の誤警報や原因情報がないときの品質は判断できない。合成ケースの点数が高くても実障害の原因特定精度の保証にはならない。導入採否を決める実測ではラベルの独立確認と実用ケース追加を行う。

同じscoreはsuiteの質問定義順で順位付けする。Top1/Top3はその順位に従うため、同点を見落とさず `top_tie_count` も確認する。confidenceは記録するが、モデル間で校正が同じとは仮定しない。

## まず送信せず確認する

Python 3.10以上の標準ライブラリだけで動く。Python 3.14.4/Linux arm64で検証済み。リポジトリのルートで実行する。

```sh
python3 tools/decisionbench.py
```

無指定はpreview。認証情報を読み込まずネットワークにも接続しない。入力hash、case数、呼出し予定件数、送信先、単価を表示する。既定は13ケース×3モデル×3反復＝117件。実際の本文は `tools/decisionbench/synthetic.json` で読める。

特定ケースだけ確認する例：

```sh
python3 tools/decisionbench.py --cases oom,long-oom-end --repeats 1
```

合成応答で一連の出力を確認する例：

```sh
mkdir -p .tmp/decisionbench
python3 tools/decisionbench.py --mock --repeats 1 --output .tmp/decisionbench/demo-new
```

`--mock` は39件の合成応答でツールを確認する。ラベルから返答を生成するため、満点になるのは動作確認用の仕様。速度・費用・品質を実モデルの測定結果として使わない。出力フォルダは新しい名前にする。既存フォルダやsymlinkは拒否し、結果を上書きしない。

## 出力の読み方

| ファイル | 内容 |
|---|---|
| metadata.json | 実行モード・日時・ケース/ラベル・入力hash・予定件数・モデル別名・地域/plan・単価・ツールhash |
| samples.jsonl | 1呼出し1行、case/model/順序/応答model/score/confidence/順位/token/時間/成功失敗コード |
| summary.json | 全体とモデル/ケース別の集計、成功・失敗・未実施・warmup・品質母集団・実model一覧 |
| report.md | 日本語の全体・ケース別集計 |
| report.htm | 外部ファイル不要のスマホ用HTML、横スクロールする比較表 |

p50は中央値、p95はnearest-rank。成功だけが速度の母集団。warmupと失敗は除外する。Top1/Top3/nDCGの母集団は成功かつラベルがある呼出し。反復数と独立した障害ケース数は別に扱う。試行0は未実施であり成功ではない。未完了結果も採否判断に使う前に失敗と未実施を確認する。

秘密token・入力本文・生の外部応答本文を成果物に保存しない。ただしmetadataには利用者が指定したcase/質問ID、ラベル、地域/planを、samplesにはモデル名と質問IDを記録する。独自suiteではその識別子にも秘密値を入れない。入力hashは同一性確認用であり匿名化の保証ではない。

`Ctrl+C` が問い合わせ中に入った場合はINTERRUPTEDのFAIL行と集計を保存する。ファイル書込中の中断等ではsummaryが作られない場合があるが、metadataとflush/fsync済みsamplesが残る。再開や自動再送は行わず、次回は新しい出力フォルダで条件を確認してから実行する。

## 実API測定の開始方法

必要な環境変数は `JEV_API_KEY`、`CLOUDFLARE_AUTH_TOKEN`、`CLOUDFLARE_ACCOUNT_ID`。TypeSafeとCloudflareのキーは別。account IDは32桁16進文字列。全てを最初の送信前に検査する。Jevだけの測定ではCloudflare設定は不要。

ツールは `.env` や秘密ファイルを自動読込しない。認証値は利用する環境の秘密管理機能から環境変数として実行プロセスへ供給する。チャットやコマンド引数、Git管理対象ファイルに秘密を貼り付けない。

次は認証がプロセスへ供給済みの場合の実行形で、実行すると外部APIへ送信する。API呼び出しには利用契約に応じた料金が発生する。

```sh
python3 tools/decisionbench.py --live --repeats 3 --max-requests 117 \
  --region unknown --plan unknown --output .tmp/decisionbench/live-new
```

`--region` は実クライアントの地域、`--plan` はCloudflareの利用planを記録するラベル。事実を指定する。`--max-requests` はwarmup込み総呼出し上限で、実行予定が上限を超えたら送信前に拒否する。金額の上限ではない。既定の単価はCloudflare Clef $0.24／Flash $0.09／100万入力tokens（2026-10-07確認）。Jev単価は自動推測せず、未指定は費用不明。契約を確認して `--jev-price` で100万入力token単価USDを指定できる。

速度を長い入力1ケースで50回ずつ比較する実行形：

```sh
python3 tools/decisionbench.py --live --cases long-oom-end --repeats 50 \
  --max-requests 150 --region unknown --plan unknown --output .tmp/decisionbench/latency-new
```

warmupは `--warmup 1` のように指定し、case×modelごとに追加呼出しとなる。例として全13ケース・3反復・warmup1なら156件。順序はケース・反復ごとに先頭モデルを交替させる。直列・呼出しごとに新しいTLS接続、環境proxyを使わず直接接続。接続再利用や並列性能は測らない。

`--timeout` の既定は20秒。接続/ソケット操作timeoutと、応答body読取のchunk間deadlineであり、総HTTP時間が必ず20秒以内という保証ではない。最大1MiBの応答を超えたら拒否。入力suiteは最大2MiB、ケース数100、質問数1〜64、全予定呼出し1000まで。

送信先はTypeSafeの `api.typesafe.ai/v1/systemone` とCloudflareの `api.cloudflare.com/client/v4/accounts/{account_id}/ai/run/@cf/cloudflare/{model}` に固定し、TLS証明書を検証、redirectを追わない。実行時の `--live` で選んだsuiteの本文・時刻・症状・質問を送る。最初は同梱の合成suiteだけを使う。独自の実ログsuiteを使う前には、送信範囲と送信先の承認・mask・正式計画を別に確認する。

最初のHTTP/通信/契約エラーで停止し、自動再試行しない。型・質問集合・score/confidenceの有限値/範囲・実model・usageを検査する。同一モデルの返却modelが途中で変わったらMODEL_CHANGEDで停止する。固定エラーコードのみを記録し、外部エラー本文や認証値を表示しない。Cloudflareの実応答が公開仕様と異なる場合も、互換と推測して通さず停止する。

## 独自suite

ファイル全体は `{"schema_version":1,"cases":[...]}`。ケースには以下を定義する。同梱ファイルを参考として別名のsuiteを作る。製品dry-runのrequestを直接指定する形式ではない。

- `id`：英数字・`_ . -`、最大80文字、重複不可。
- `state`：文字列、JSON object/array。全モデルへ同一値を渡す。
- `questions`：質問IDをキーとするmap。各質問のtypeはscore、instructionsは文字列、criteriaは同梱の製品と同じ4段階。モデル欄はツールが付ける。
- `grades`：質問IDごとの0〜3の優先度ラベル。質問集合と完全一致が必要。最高が0のケースは順位品質を評価できないため、gradesを省略またはnullにして速度/契約だけ確認する。

確認と測定は `--suite <ファイル>`。metadataのsuite hashはラベルとcase IDも含む。呼出しのcommon hashはstate/questionsだけで、全モデルの同一入力を確認できる。

## ツール自体の検査

```sh
python3 -m unittest discover -s tools/decisionbench -v
python3 -m py_compile tools/decisionbench.py tools/decisionbench/test_decisionbench.py
```

本番ログやAPIキーなしで実行できる。外部APIの契約と速度はmockでは確認できない。実測の結果とは区別する。

公式根拠（確認日2026-10-07）：[Clefモデル/API/料金](https://developers.cloudflare.com/workers-ai/models/clef/)、[Clef-flash](https://developers.cloudflare.com/workers-ai/models/clef-flash/)、[Cloudflare RESTの認証と応答包装](https://developers.cloudflare.com/workers-ai/get-started/rest-api/)。
