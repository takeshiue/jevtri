# 发送前的脱敏示例

[English](masking.md) | [日本語](masking.ja.md) | 简体中文

jevtri 在把日志行发送给 Jev 之前会对敏感信息进行脱敏。以下是 0.1.0 的 `jevtri --dry-run`
读取测试用日志行后的实际输出（未发送任何内容）。

原始日志：

```
2026-09-28 03:01:10 ERROR db connect failed: postgres://app:S3cretPass@db01:5432/shop
2026-09-28 03:01:11 WARN login retry user=alice password=hunter2 from 203.0.113.7
2026-09-28 03:01:12 INFO request Authorization: Bearer example-tok1
2026-09-28 03:01:13 ERROR mail to taro.yamada@example.com failed
2026-09-28 03:01:14 INFO payment card=4000 0000 0000 0002 api_key=example-key1
```

将要发送的内容（摘自 `--dry-run` 的输出）：

```
2026-09-28T03:01:10.000+09:00 ERROR db connect failed: postgres://app:[MASKED:url-credential]@db01:5432/shop
2026-09-28T03:01:11.000+09:00 WARN login retry user=alice password=[MASKED:secret] from 203.0.113.7
2026-09-28T03:01:12.000+09:00 INFO request Authorization: Bearer [MASKED:authorization]
2026-09-28T03:01:13.000+09:00 ERROR mail to [MASKED:email]@example.com failed
2026-09-28T03:01:14.000+09:00 INFO payment card=[MASKED:card] api_key=[MASKED:secret]
```

- 连接 URL 中的密码、`password=` 和 `api_key=` 的值、`Authorization` 头、邮件地址 `@` 之前的部分
  以及卡号，会被替换为表示类型的 `[MASKED:…]`。
- 时间统一为同一时区的写法。
- **IP 地址（`203.0.113.7`）、用户名（`alice`）和主机名（`db01`）不会被脱敏**，因为它们常常是
  原因的线索。其他要删除的字符串可以用配置中的 `mask` 以正则表达式指定。
- 脱敏基于模式匹配，可能有遗漏。请先用 `--dry-run` 确认。

完整列表请参阅 README 的[发送哪些数据，发送到哪里](../README.zh-CN.md#发送哪些数据发送到哪里)。
