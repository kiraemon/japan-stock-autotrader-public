# はじめてのペーパー取引の再生

組み込みの合成データを使い、仮想約定を含むセッションが、気配が届くときと途絶したときでどう変わるかを確認します。Go 1.26以上を使います。

## 1. 合成セッションを再生する

クローンしたリポジトリのルートで実行します。入力と時刻はデモに含まれているため、APIの設定は不要です。

```sh
go run ./cmd/kabu-research session-demo -direction long -variant A
```

## 2. 期待どおりの終了を見分ける

結果から次の2ケースを探します。

| ケース | 確認すること |
| --- | --- |
| `ordered_fresh_quotes` | `passed=true` と `clean_finish=true`。気配が届き、セッションが正常に完了します。 |
| `silence_with_exposure` | `passed=true` と `clean_finish=false`。仮想保有中に入力が途絶し、未解消数量を台帳に残して止まります。 |

`passed=true` は期待した動作と一致したことを表します。これで、試験結果の合格と、シミュレーションの正常終了を読み分けられます。

## 3. 保存された結果を確認する

出力の「保存」に表示された `data/research/` 配下のJSONを開きます。`cases` の各項目に、ケース名、判定結果、処理の記録、最終状態が保存されています。両ケースの `name` と `passed` を端末の表示と照合してください。

[ほかのデモを実行する](../how-to/verify-locally.md) · [アーキテクチャを読む](../explanation/architecture.md)
