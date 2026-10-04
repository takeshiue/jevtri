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

<a id="additional-masks"></a>

## 配置额外脱敏规则

若需在内置脱敏规则之外添加规则，请在 `/etc/jevtri/jevtri.conf` 中目标日志的 `[log 名称]` 节内添加 `mask`。需要多个规则时，每行写一个正则表达式。

```ini
[log app]
path = /var/log/myapp/app.log
time_format = iso-space
mask = order-\d+
mask = CUSTOMER-[0-9]+
mask = \b[a-f0-9]{48}\b
```

此例将三个规则全部添加到内置规则中。每个规则匹配到的整个字符串都会被脱敏。这些设置仅用于该日志；若其他日志也需要，请在相应日志节内填写。

发送前，请用 `sudo jevtri --dry-run` 确认脱敏范围是否符合预期。

## 多行私钥

私钥块的状态在时间窗口和保留量裁剪之前跟踪。Docker JSON的log正文会先解码，
stdout与stderr分别处理。缺少END时，该流的后续正文会隐藏到END出现为止。
为防止seek或日志轮转丢失BEGIN，行末仅由base64组成的16–76字符正文候选也会
保守地隐藏，因此某些非秘密base64可能被隐藏。不保证识别任意短片段或其他编码；
请配合自定义mask和dry-run检查。每个source最多读取解压后128MiB，达到上限会
提示未读范围，上限处被截断的最后一行不会发送。
