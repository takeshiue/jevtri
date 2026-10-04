# 送る前の伏せ字の例

[English](masking.md) | 日本語 | [简体中文](masking.zh-CN.md)

jevtri は、ログの行を Jev へ送る前に秘密を伏せ字にします。次は、0.1.0 の `jevtri --dry-run` に
試験用の行を読ませた実際の出力です（何も送っていません）。

元のログ:

```
2026-09-28 03:01:10 ERROR db connect failed: postgres://app:S3cretPass@db01:5432/shop
2026-09-28 03:01:11 WARN login retry user=alice password=hunter2 from 203.0.113.7
2026-09-28 03:01:12 INFO request Authorization: Bearer example-tok1
2026-09-28 03:01:13 ERROR mail to taro.yamada@example.com failed
2026-09-28 03:01:14 INFO payment card=4000 0000 0000 0002 api_key=example-key1
```

送る内容（`--dry-run` の表示から抜粋）:

```
2026-09-28T03:01:10.000+09:00 ERROR db connect failed: postgres://app:[MASKED:url-credential]@db01:5432/shop
2026-09-28T03:01:11.000+09:00 WARN login retry user=alice password=[MASKED:secret] from 203.0.113.7
2026-09-28T03:01:12.000+09:00 INFO request Authorization: Bearer [MASKED:authorization]
2026-09-28T03:01:13.000+09:00 ERROR mail to [MASKED:email]@example.com failed
2026-09-28T03:01:14.000+09:00 INFO payment card=[MASKED:card] api_key=[MASKED:secret]
```

- 接続 URL のパスワード、`password=`・`api_key=` の値、`Authorization` ヘッダー、メールアドレスの
  `@` より前、カード番号が、種類を示す `[MASKED:…]` に置き換わります。
- 時刻は1つのタイムゾーンの表記に揃えます。
- **IP アドレス（`203.0.113.7`）、ユーザー名（`alice`）、ホスト名（`db01`）は伏せ字にしません。**
  原因の手がかりになることが多いためです。消したい文字列は設定の `mask` に正規表現で書けます。
- 伏せ字はパターンによるため、見落とすことがあります。最初は `--dry-run` で確かめてください。

対象の一覧は [README の「何をどこへ送るか」](../README.ja.md#何をどこへ送るか)を参照してください。

<a id="additional-masks"></a>

## 追加マスクの設定

標準マスクに加えて伏せたいパターンは、`/etc/jevtri/jevtri.conf` の対象ログの `[log 名前]` セクションに `mask` として記載します。複数指定する場合は、1行に1つの正規表現を書きます。

```ini
[log app]
path = /var/log/myapp/app.log
time_format = iso-space
mask = order-\d+
mask = CUSTOMER-[0-9]+
mask = \b[a-f0-9]{48}\b
```

この例では、3つのパターンすべてを標準マスクに追加します。各パターンに一致した文字列全体を伏せます。設定はこのログだけに適用されるため、他のログにも適用する場合は、そのログのセクションにも記載してください。

送信前に `sudo jevtri --dry-run` で、伏せる範囲が意図どおりか確認してください。

## 複数行に分かれた秘密鍵

秘密鍵ブロックは時間窓や保持量による切捨てより前に状態を追跡します。
Docker JSONのlog本文を復号し、stdoutとstderrは別の状態で処理します。
ENDが欠落した場合、そのstreamの後続本文はENDまで安全優先で伏せます。
seekやログ回転でBEGINが失われる場合に備え、行末の16〜76文字のbase64だけの
本文候補も保守的に伏せます。秘密でないbase64も伏せられる場合があります。
任意の短断片や別encodingの検出を保証しません。独自maskとdry-run確認を併用してください。
各sourceの読取は展開後128MiBまでです。上限到達は未読範囲として表示し、
上限で切れた末尾の不完全行は送信しません。
