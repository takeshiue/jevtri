# 判断示例

[English](examples.md) | [日本語](examples.ja.md) | 简体中文

以下是 0.1.0 测试所用案例中的3个。每个案例都把原因的日志行混入日常日志，
并把这些行的时间移到故障前几分钟。优先级是 Jev 实际返回的值。

**1. cron 发出的邮件收不到**（日志：messages、secure、cron、maillog、httpd）

```
Sep 28 07:51:52 web01 postfix/local[822]: error: open database /etc/aliases.db: No such file or directory
Sep 28 07:51:52 web01 postfix/local[822]: C5410107B88: to=<root@web01.localdomain>, orig_to=<root>, relay=local, delay=0.05, delays=0.03/0.01/0/0.01, dsn=4.3.0, status=deferred (alias database unavailable)
```

结果：maillog 99、cron 27、secure 26。maillog 排第一。

**2. 修改配置并 reload 后没有生效**（日志：messages、secure、cron、nginx、php-fpm）

```
2026/09/28 03:02:05 [emerg] 27#27: unknown directive "proxy_passs" in /etc/nginx/conf.d/default.conf:3
```

结果：nginx 99、messages 30、secure 23。nginx 排第一。

**3. 部分页面出现数据库连接错误**（日志：messages、secure、nginx、php-fpm、postgresql、redis）

```
2026-09-28 03:03:08.289 JST [79] FATAL:  remaining connection slots are reserved for roles with the SUPERUSER attribute
```

结果：postgresql 98、messages 25、nginx 23。postgresql 排第一。

以上数值来自第1次运行。同样的40个案例用同一构建运行了3次。

| 次 | 正确的日志排第一 | 前两位以内 |
|---|---|---|
| 1 | 38/40 | 40/40 |
| 2 | 37/40 | 40/40 |
| 3 | 37/40 | 40/40 |

Jev 的数值每次运行会略有变化（同一日志3次之间差值的中位数为 1 分，最大 6 分）。
未排第一的案例大多是另一个也被接受的日志（例如 504 时的 nginx）排第一，正确的日志排第二。
第2、3次中有1个案例（"客户从办公室无法访问网站"），fail2ban 与 nginx 几乎同分，
不被接受的 nginx 排了第一。分数接近时，请一起查看排名靠前的日志。
