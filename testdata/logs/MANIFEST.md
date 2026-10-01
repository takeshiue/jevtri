# 実ログのサンプル（正解つき）

各ソフトウェアを検証サーバーの Podman コンテナで実際に動かし、障害を意図的に起こして出力させたログである。ネット上のログはコピーしていない。生成手順は [`experiments/log-samples/collect-batch1.sh`](../../experiments/log-samples/collect-batch1.sh)。

- 生成日: 2026-09-28（コンテナのタイムゾーンは `Asia/Tokyo`）
- IP アドレス `10.0.2.100` はコンテナ網の内部アドレスで、実在の利用者ではない
- 「起こした障害」が各ログの正解である。ログに現れなかったものは「現れず」と明記する

## 起こした障害と、ログへの現れ方

| ソフトウェア | ファイル | 起こした障害 | ログに現れた行（例） |
|---|---|---|---|
| nginx 1.x | `nginx/error.log` | 存在しないファイルへのアクセス | `[error] ... open() "/usr/share/nginx/html/missing.html" failed (2: No such file or directory)` |
| | | 設定の書き間違い（`proxy_passs`）で reload | `[emerg] 27#27: unknown directive "proxy_passs"` |
| | | 接続先（upstream）の停止 | `[error] ... connect() failed (111: Connection refused) while connecting to upstream` |
| Apache httpd 2.4 | `httpd/error_log` | 権限 000 のファイルへのアクセス | `[core:error] ... AH00132: file permissions deny server access` |
| | | インデックスの無いディレクトリへのアクセス | 現れず（既定の LogLevel では記録されない） |
| MySQL 8.4 | `mysql/error.log` | 誤ったパスワード、存在しないユーザー | `[Note] [MY-010926] [Server] Access denied for user 'root'@'localhost'` |
| | | 接続数の上限（`max_connections=3`） | 現れず（クライアントへのエラーのみ） |
| | | mysqld の強制終了（SIGKILL）と再起動 | `Crash recovery finished in InnoDB engine` など |
| MariaDB 11 | `mariadb/error.log` | 誤ったパスワード | `[Warning] Access denied for user 'root'@'localhost'` |
| | | 接続数の上限 | 現れず（上限値 3 は最小値 10 に補正された） |
| | | 強制終了と再起動 | 起動のやり直しの行 |
| PostgreSQL 17 | `postgres/postgresql.log` | SQL の構文誤り | `ERROR:  syntax error at or near "selec"` |
| | | 接続数の上限（`max_connections=5`） | `FATAL:  remaining connection slots are reserved for roles with the SUPERUSER attribute` |
| | | 誤ったパスワード | 現れず（公式イメージは 127.0.0.1 からの接続を認証なしで許可するため、失敗しなかった） |
| | | 強制終了と再起動 | 再起動の行 |
| Redis 7.4 | `redis/redis.log` | メモリ上限（`maxmemory 2mb`、追い出し無し） | 現れず（コマンドへのエラーのみ） |
| | | 保存先を存在しないディレクトリへ変更 | 現れず（設定変更が拒否された） |
| PHP-FPM 8.3 | `php-fpm/php-fpm.log` | ワーカーの強制終了（SIGKILL） | `WARNING: [pool www] child 3 exited on signal 9 (SIGKILL)` |

「現れず」は、その障害がそのソフトのログだけでは見つけられないことを示す。jevtri の評価では、正解のログにも証拠が無い題材として扱う。

## 時刻の形式（カタログへの影響）

| ソフトウェア | 形式の例 | カタログ（仕様書 12.0） |
|---|---|---|
| nginx | `2026/09/28 03:02:04` | `slash-ymd` で読める |
| Apache httpd | `[Mon Sep 28 03:02:10.715837 2026]` | `apache-error` で読める |
| MySQL 8.4 | `2026-09-27T18:02:14.945835Z` | `rfc3339` で読める。**`TZ` を設定しても UTC で出力される**（`log_timestamps` の既定が UTC） |
| MariaDB 11 | `2026-09-28  3:02:36` | **読めない**。時が1桁のとき空白で埋める（`  3:02`）。タイムゾーンの表記も無い |
| PostgreSQL 17 | `2026-09-28 03:03:07.591 JST` | **読めない**。タイムゾーンを略称（`JST`）で書く |
| Redis 7.4 | `1:C 28 Sep 2026 03:03:22.943` | **読めない**。行頭に `pid:役割` があり、日付が `日 月名 年` の順 |
| PHP-FPM 8.3 | `[28-Sep-2026 03:03:34]` | **読めない**。`日-月名-年` の順 |

## 第2弾: syslog 系（2026-09-28）

生成手順は [`experiments/log-samples/collect-batch2.sh`](../../experiments/log-samples/collect-batch2.sh)。各 OS のコンテナで rsyslog、sshd、cron、Postfix を動かした。ホスト名は `web01`。SSH の接続元はコンテナ内の `127.0.0.1`。コンテナには systemd とカーネルのログが無いため、OOM やディスク満杯などカーネル由来の障害は含まない。

| OS | ファイル | 起こした障害 | ログに現れた行（例） |
|---|---|---|---|
| Alma 9 | `alma9/secure` | 存在しないユーザー（admin、test、oracle）と、実在ユーザー deploy の誤パスワードでの SSH | `sshd-session[750]: Failed password for invalid user admin from 127.0.0.1` |
| | | sudo の誤パスワード | 現れず（標準入力からの入力が端末なしで失敗し、記録されなかった） |
| | `alma9/cron` | 毎分失敗するバックアップ（存在しないファイルの cp） | `CROND[829]: (root) CMD (/usr/local/bin/backup.sh)`。失敗の中身は cron には出ない |
| | `alma9/maillog` | 存在しないドメインへのメール | `status=bounced (Host or domain name not found...)`。cron の出力メールの配送失敗（`/etc/aliases.db` が無い）も記録された |
| Ubuntu 24.04 | `ubuntu2404/auth.log` | SSH の総当たり（同上） | `sshd[4349]: Failed password for invalid user admin from 127.0.0.1` |
| | | sudo の誤パスワード | `sudo: pam_unix(sudo:auth): authentication failure; ... user=deploy` |
| | `ubuntu2404/syslog` | 失敗するバックアップ | `CRON[4409]: (root) CMD (/usr/local/bin/backup.sh)`（Ubuntu は cron を syslog に書く） |
| | `ubuntu2404/mail.log` | 存在しないドメインへのメール | `status=deferred (Host or domain name not found...)` |
| Debian 12 | `debian12/auth.log`、`cron.log`、`mail.log` | Ubuntu と同じ | Ubuntu とほぼ同じ |

注意: `debian12/syslog` の先頭には、rootless コンテナでログファイルの所有者を変えられなかった rsyslog のエラーが5行ある（コンテナ特有で、実機では出ない）。

### 時刻の形式

| 出どころ | 形式の例 | カタログ |
|---|---|---|
| Alma 9 の rsyslog（messages、secure、cron、maillog） | `Sep 28 07:51:24` | `syslog` で読める（年とタイムゾーンは無い） |
| Ubuntu 24.04・Debian 12 の rsyslog | `2026-09-28T07:29:16.929554+09:00` | `rfc3339` で読める |
| Alma 9 の dnf.log | `2026-09-28T07:50:51+0900` | **読めない**。タイムゾーンが `+0900`（コロン無し）で、RFC 3339 と異なる |
| Ubuntu の dpkg.log | `2026-09-11 02:03:30` | `iso-space` で読める |

同じ種類のログでも、RHEL 系は伝統形式、Debian 系は RFC 3339 と、OS で形式が違う。ログの場所も異なる（`secure` と `auth.log`、`maillog` と `mail.log`、cron は RHEL 系が専用ファイル、Ubuntu は `syslog`）。

## 第3弾: Java・ロードバランサー・JSON 形式（2026-09-28）

生成手順は [`experiments/log-samples/collect-batch3.sh`](../../experiments/log-samples/collect-batch3.sh)。

| ソフトウェア | ファイル | 起こした障害 | ログに現れた行（例） |
|---|---|---|---|
| Tomcat 10.1（Java 21） | `tomcat/catalina.2026-09-28.log` | 壊れた `web.xml` のアプリを配置 | `28-Sep-2026 09:44:02.397 SEVERE [Catalina-utility-1] ... Parse fatal error`。続けてタブ字下げのスタックトレースが複数行 |
| | | 例外を投げる JSP へのアクセス | 現れず（catalina のログには記録されなかった） |
| HAProxy 3.0 | `haproxy/haproxy.log` | 振り分け先2台の停止 | `[WARNING]  (3) : Server app/app1 is DOWN, reason: Layer4 connection problem`、`[ALERT] ... backend 'app' has no server available!`、アクセス行の `503 ... SC--` |
| MongoDB 8.0 | `mongodb/mongod.log` | 誤ったパスワード | `"msg":"Failed to authenticate"` |
| | | mongod の強制終了と再起動 | `"msg":"Detected unclean shutdown - Lock file is not empty"`、`"Recovering data from the last clean checkpoint."` |
| | | 索引の無い全件検索 | 現れず（遅いクエリの閾値に達しなかった） |

### 時刻の形式

| 出どころ | 形式の例 | カタログ |
|---|---|---|
| Tomcat（catalina） | `28-Sep-2026 09:44:02.397` | **読めない**。`日-月名-年`（PHP-FPM と同系統で、角括弧が無い） |
| Tomcat（アクセスログ） | `[28/Sep/2026:09:44:10 +0900]` | `apache-access` で読める |
| HAProxy（アクセス行） | `[28/Sep/2026:09:44:15.502]` | **読めない**。`apache-access` に似るがミリ秒付きでタイムゾーンが無い |
| HAProxy（イベント行） | 時刻なし（`[WARNING]  (3) : ...`） | 標準出力へ直接出すと時刻が付かない。実運用では syslog 経由で syslog の時刻が付く |
| MongoDB | `{"t":{"$date":"2026-09-28T09:44:40.194+09:00"},...}` | 行の中の `rfc3339` として読める。1行1つの JSON |

## 第4弾: 実機のカーネル・systemd 由来の障害（2026-09-28）

検証サーバーの実機で、影響範囲を絞って障害を起こした。メモリ不足は上限 64MB の cgroup の中だけ、ディスク満杯は 32MB のループ用ファイルシステムだけで起こし、終了後にすべて片付けた。生成手順は [`experiments/log-samples/collect-host-ubuntu.sh`](../../experiments/log-samples/collect-host-ubuntu.sh) と [`collect-host-alma8.sh`](../../experiments/log-samples/collect-host-alma8.sh)。

抽出したのは障害を起こした約20秒間の行だけである。自分たちの SSH 接続、ユーザー名、IP アドレスを含む行は除き、ホスト名は `web01` に置き換えた。journal の JSON のマシン ID とブート ID は 0 に置き換えた。

| 障害 | Ubuntu 24.04（`ubuntu2404-host/`） | Alma 8（`alma8-host/`） |
|---|---|---|
| メモリ不足（cgroup 内） | `kernel: Memory cgroup out of memory: Killed process 267858 (python3)` と、`systemd[1]: jt-oom.service: Failed with result 'oom-kill'` | 現れず（cgroup v1 ではメモリ上限が効かず、確保に成功した） |
| セグメンテーション違反 | `kernel: python3[267862]: segfault at 0 ip ... error 4 in libc.so.6`、`Failed with result 'core-dump'` | `kernel: platform-python[13811]: segfault at 0 ...`。`audit.log` に `type=ANOM_ABEND ... sig=11` |
| ディスク満杯 | `sh[267881]: dd: error writing '/mnt/jt-full/export.dat': No space left on device`。カーネルは何も出さない | 同じ |
| サービスの失敗の繰り返し | `Scheduled restart job, restart counter is at 1` の後、`Start request repeated too quickly.` | 同じ。`audit.log` に `SERVICE_STOP ... res=failed` |

| ファイル | 形式の例 | カタログ |
|---|---|---|
| `ubuntu2404-host/syslog`、`kern.log` | `2026-09-28T10:36:59.236386+09:00` | `rfc3339` |
| `alma8-host/messages` | `Sep 28 10:38:31` | `syslog` |
| `*/journal-short.log`（`journalctl -o short`） | `Sep 28 10:36:59` | `syslog` |
| `*/journal.json`（`journalctl -o json`） | `"__REALTIME_TIMESTAMP":"1790559419169934"`（マイクロ秒の UNIX 時刻） | **読めない**。`epoch` は秒単位。ただし jevtri が journal を扱うなら `journalctl --since/--until` に任せればよい |
| `alma8-host/audit.log` | `msg=audit(1790559511.383:2267)` | `epoch` |
| `*/dmesg.log` | `[241254.433679]`（起動からの秒数） | **読めない**。実時刻ではない。`dmesg -T`（`dmesg-T.log`）なら `[Mon Sep 28 10:36:58 2026]` で `apache-error` に近い形式だが、秒単位でずれることがある |

気づいた点:
- ディスク満杯はカーネルのログには出ない。書き込もうとしたアプリの出力（ここでは dd）にだけ現れる
- Alma 8 の回転済みファイルは日付付きの名前（`messages-20260927`）で、Ubuntu の番号付き（`syslog.1`）と異なる。回転済みファイルを読む処理は両方に対応する必要がある

## 第5弾: 平常時の運用（障害なし、2026-09-29）

題材の平常時の行（`testdata/scenarios/kinds.toml` の `normal`）の材料。障害は起こしていない。検証サーバーの Podman で、普通の運用を480秒続けた。生成手順は [`experiments/log-samples/collect-batch4-routine.sh`](../../experiments/log-samples/collect-batch4-routine.sh)（ファイル名の batch4 は手順の通し番号で、本書の弾の番号とは別）。置き場所は `routine/<名前>/`。

- 時刻: アプリ6種と alma9・ubuntu2404・debian12 は 08:08〜08:18、alma8 は 08:19〜08:27（alma8 は初回にパッケージ名の誤りで導入が失敗し、直して取り直した）
- 取り込んだのは運用中のログだけ。コンテナ作成時のパッケージ導入の記録（dnf、dpkg、apt、bootstrap）は除いた
- IP アドレスは `127.0.0.1` とコンテナ網の `10.0.2.100` だけ（ほかに `0.0.0.0` の待ち受け表示）。利用者名は コンテナ内で作った `deploy` と `root` だけ。SSH の鍵の指紋はコンテナ内で使い捨てに作った鍵のもの
- ubuntu2404・debian12 の postfix の `message-id` にはコンテナ ID（12桁の16進）が入っている

| 対象 | ファイル | 行っていた操作 | 平常時の行の例 |
|---|---|---|---|
| alma8、alma9 | `messages`、`secure`、`cron`、`maillog` | 定期ジョブ3本（毎分・2分・3分）、deploy ユーザーの公開鍵での SSH ログインと `sudo -n`、ローカル宛てのメール配送、`su` | `CROND[175]: (root) CMD (/usr/local/bin/healthcheck.sh)`、`Accepted publickey for deploy from 127.0.0.1`、`postfix/local[216]: ... status=sent (delivered to mailbox)` |
| ubuntu2404、debian12 | `syslog`、`auth.log`、`mail.log`（debian12 は `cron.log` も） | 同上 | 同上の RFC 3339 形式 |
| nginx | `error.log`（info）、`access.log` | 約11秒ごとのアクセス | `[info] 3#3: *1 client 10.0.2.100 closed keepalive connection` |
| httpd | `error_log`（LogLevel info）、`access_log` | 約13秒ごとのアクセス | `[core:info] ... AH00128: File does not exist: .../favicon.ico` |
| MySQL 8.4 | `mysql/error.log` | 挿入と集計（`--log-error-verbosity=3`） | 運用中の行は無し。起動・停止の行だけ |
| PostgreSQL 17 | `postgres/postgresql.log` | 接続・切断、書き込み | `connection authorized`、`disconnection: session time`、`checkpoint starting: time`、自動 VACUUM・ANALYZE |
| Redis 7.4 | `redis/redis.log` | 書き込み（`save "30 1"`） | `1 changes in 30 seconds. Saving...`、`Background saving terminated with success` |
| PHP-FPM 8.3 | `php-fpm/php-fpm.log` | ondemand での要求 | 起動・停止の行だけ（子プロセスの起動・終了は記録されなかった） |

気づいた点:
- rootless コンテナのため、平常時でもエラーに見える行が出る（`pam_systemd(...): Failed to connect to system bus`、`pam_lastlog(...): unable to open /var/log/btmp`、Debian 系の `rsyslogd: error during config processing: omfile: chown ... failed`）。実機の平常時には出ないので、題材の平常時の行には使わない
- 同じ本文の行が繰り返し出る（cron、su、メール配送）。題材の確認では本文だけでなく時刻でも照合する必要があった（`testdata/scenarios/README.md`）
