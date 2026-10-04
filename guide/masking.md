# Masking before sending: an example

English | [日本語](masking.ja.md) | [简体中文](masking.zh-CN.md)

jevtri masks secrets before it sends log lines to Jev. Below is the real output of
`jevtri --dry-run` (0.1.0) on test lines. Nothing was sent.

The log:

```
2026-09-28 03:01:10 ERROR db connect failed: postgres://app:S3cretPass@db01:5432/shop
2026-09-28 03:01:11 WARN login retry user=alice password=hunter2 from 203.0.113.7
2026-09-28 03:01:12 INFO request Authorization: Bearer example-tok1
2026-09-28 03:01:13 ERROR mail to taro.yamada@example.com failed
2026-09-28 03:01:14 INFO payment card=4000 0000 0000 0002 api_key=example-key1
```

What would be sent (from the `--dry-run` output):

```
2026-09-28T03:01:10.000+09:00 ERROR db connect failed: postgres://app:[MASKED:url-credential]@db01:5432/shop
2026-09-28T03:01:11.000+09:00 WARN login retry user=alice password=[MASKED:secret] from 203.0.113.7
2026-09-28T03:01:12.000+09:00 INFO request Authorization: Bearer [MASKED:authorization]
2026-09-28T03:01:13.000+09:00 ERROR mail to [MASKED:email]@example.com failed
2026-09-28T03:01:14.000+09:00 INFO payment card=[MASKED:card] api_key=[MASKED:secret]
```

- The password in a connection URL, the values of `password=` and `api_key=`, the
  `Authorization` header, the part of an email address before `@`, and the card
  number become `[MASKED:…]`, naming the kind.
- Times are written in one time zone.
- **IP addresses (`203.0.113.7`), user names (`alice`) and host names (`db01`) are
  not masked**, because they often point to the cause. Add your own regular
  expressions with `mask` in the configuration to remove other strings.
- Masking is pattern-based and can miss things. Check with `--dry-run` first.

See [What is sent, and where](../README.md#what-is-sent-and-where) in the README for the full list.

<a id="additional-masks"></a>

## Configure extra masks

To mask patterns in addition to the built-in masks, add `mask` lines to the target log's `[log NAME]` section in `/etc/jevtri/jevtri.conf`. For multiple patterns, write one regular expression per line.

```ini
[log app]
path = /var/log/myapp/app.log
time_format = iso-space
mask = order-\d+
mask = CUSTOMER-[0-9]+
mask = \b[a-f0-9]{48}\b
```

This example adds all three patterns to the built-in masks. Each entire matching string is masked. These settings apply only to this log; add them to other log sections if needed.

Before sending, use `sudo jevtri --dry-run` to check that the masks cover what you intend.

## Multi-line private keys

Private-key block state is tracked before window and retention filtering. Docker
JSON `log` values are decoded, with separate stdout/stderr state. If END is
missing, subsequent text in that stream is hidden until END. After seeking or
rotation, trailing base64-only payloads of 16–76 characters are conservatively
hidden; some non-secret base64 is therefore masked. Arbitrary short fragments
or other encodings are not guaranteed to be recognized. Keep custom masks and
review a dry run. Reading is capped at 128 MiB of decompressed input per source;
truncation is reported and the last incomplete line at the cap is omitted.
