# 判定の例

[English](examples.md) | 日本語 | [简体中文](examples.zh-CN.md)

0.1.0 の試験で使った題材から3つを示します。題材は、平常時のログに原因の行を混ぜて作り、
原因の行の時刻は障害の時刻の数分前にずらしています。優先度は Jev が実際に返した値です。

**1. cron のメールが届かない**（ログ: messages、secure、cron、maillog、httpd）

```
Sep 28 07:51:52 web01 postfix/local[822]: error: open database /etc/aliases.db: No such file or directory
Sep 28 07:51:52 web01 postfix/local[822]: C5410107B88: to=<root@web01.localdomain>, orig_to=<root>, relay=local, delay=0.05, delays=0.03/0.01/0/0.01, dsn=4.3.0, status=deferred (alias database unavailable)
```

結果: maillog 99、cron 27、secure 26。maillog が1位です。

**2. 設定を変えて reload しても反映されない**（ログ: messages、secure、cron、nginx、php-fpm）

```
2026/09/28 03:02:05 [emerg] 27#27: unknown directive "proxy_passs" in /etc/nginx/conf.d/default.conf:3
```

結果: nginx 99、messages 30、secure 23。nginx が1位です。

**3. 一部のページでデータベースの接続エラーが出る**（ログ: messages、secure、nginx、php-fpm、postgresql、redis）

```
2026-09-28 03:03:08.289 JST [79] FATAL:  remaining connection slots are reserved for roles with the SUPERUSER attribute
```

結果: postgresql 98、messages 25、nginx 23。postgresql が1位です。

上の値は1回目のものです。同じ40題材を、同じビルドで3回実行しました。

| 回 | 正解のログが1位 | 2位以内 |
|---|---|---|
| 1 | 38/40 | 40/40 |
| 2 | 37/40 | 40/40 |
| 3 | 37/40 | 40/40 |

Jev の値は実行ごとに少し変わります（同じログで、3回の差の中央値は 1 ポイント、最大 6 ポイント）。
1位を外した題材の多くは、許容した別のログ（例: 504 のときの nginx）が1位で、正解は2位でした。
2回目と3回目の1件（「事務所からサイトに届かない」）は、fail2ban と nginx がほぼ同点で、
許容しなかった nginx が1位になりました。値が近いときは、上位のログを両方見てください。
