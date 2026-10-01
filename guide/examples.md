# Examples

English | [日本語](examples.ja.md) | [简体中文](examples.zh-CN.md)

Three cases from the 0.1.0 tests. Each case mixes the lines of a cause into
routine logs, with their times moved to a few minutes before the incident. The
priorities are the values Jev actually returned.

**1. Mail from cron jobs is not delivered** (logs: messages, secure, cron, maillog, httpd)

```
Sep 28 07:51:52 web01 postfix/local[822]: error: open database /etc/aliases.db: No such file or directory
Sep 28 07:51:52 web01 postfix/local[822]: C5410107B88: to=<root@web01.localdomain>, orig_to=<root>, relay=local, delay=0.05, delays=0.03/0.01/0/0.01, dsn=4.3.0, status=deferred (alias database unavailable)
```

Result: maillog 99, cron 27, secure 26. maillog is first.

**2. A configuration change does not take effect after reload** (logs: messages, secure, cron, nginx, php-fpm)

```
2026/09/28 03:02:05 [emerg] 27#27: unknown directive "proxy_passs" in /etc/nginx/conf.d/default.conf:3
```

Result: nginx 99, messages 30, secure 23. nginx is first.

**3. Some pages show a database connection error** (logs: messages, secure, nginx, php-fpm, postgresql, redis)

```
2026-09-28 03:03:08.289 JST [79] FATAL:  remaining connection slots are reserved for roles with the SUPERUSER attribute
```

Result: postgresql 98, messages 25, nginx 23. postgresql is first.

The values above are from the first run. The same 40 cases were run three
times with the same build.

| Run | Right log first | In the top two |
|---|---|---|
| 1 | 38/40 | 40/40 |
| 2 | 37/40 | 40/40 |
| 3 | 37/40 | 40/40 |

Jev's values vary a little between runs (for the same log, the median spread over
the three runs was 1 point and the largest 6). In most misses, another log that
was also accepted (such as nginx for a 504) came first and the right log second.
In one case in runs 2 and 3 ("a customer cannot reach the site from their office"),
fail2ban and nginx scored almost the same and nginx, which was not accepted, came
first. When scores are close, read the top logs together.
