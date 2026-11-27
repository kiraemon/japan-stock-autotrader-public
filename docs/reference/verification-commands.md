# 検証コマンドリファレンス

| コマンド | 検証対象・入力 | 主な結果 |
| --- | --- | --- |
| `python -B scripts/verify.py` | Goのテスト・静的検査、合成セッションと数量台帳のデモ、Pythonのテスト。 | 最初の失敗を表示して異常終了。全項目に成功すると正常終了。 |
| `go run ./cmd/kabu-research session-demo` | 組み込みの合成時系列8ケース。`-direction` は `long` または `short`、`-variant` は `A` または `B`。 | 各ケースの `passed`、完了状態、停止理由、未解消の仮想数量。ケース別結果と実行記録は `data/research/` に保存。 |
| `go run ./cmd/kabu-research paper-demo` | 組み込みの仮想注文・約定イベント。 | 数量台帳の状態、検証結果、チェックポイントの照合結果。デモ結果と実行記録は `data/research/` に保存。 |
| `go run ./cmd/kabu-research paper-recovery` | 停止した合成セッションの復元と、観測残高との照合。 | 照合状態、残数量、仮想決済の結果。実行例は[停止後照合](#合成ペーパー取引の停止後照合)を参照。 |
| `go test ./...` | Goのテスト。アーキテクチャの依存境界の検査を含む。 | パッケージごとのテスト結果。元の研究計画に依存するケースは対象ファイルがある場合に実行。 |
| `go vet ./...` | Goパッケージの静的検査。 | 診断があれば対象箇所と内容を表示。 |
| `python -B -m unittest discover -s research/python -t research/python -v` | メモリ上の合成分足を使ったMACD計算、分足集約、費用、約定順序のテスト。 | テスト単位の結果と合否。 |

セッションデモの `passed` は期待した状態との一致、`clean_finish` はシミュレーションが正常終了したかを示します。各行末の `open_quantity` は、最終状態の数量台帳に残っている未解消の仮想数量です。ペーパー台帳の `checkpoint_verified` は、保存したチェックポイントを照合できたかを示します。

## 合成ペーパー取引の停止後照合

`paper-recovery` は、仮銘柄100株の決済で40株だけ約定し、残りの決済状態が不明になったケースを扱います。PC内の合成データだけで動き、各コマンドを別のプロセスで実行できます。

```sh
go run ./cmd/kabu-research paper-recovery -dir data/research/recovery-demo prepare
go run ./cmd/kabu-research paper-recovery -dir data/research/recovery-demo inspect
```

`prepare` は新規フォルダを作り、停止時点を `checkpoint.json` に保存します。すでにあるフォルダを使うと準備は失敗します。直後の `inspect` は残り60株、`position_known=false`、`reconciliation_required=true` を表示します。復元した約定IDは `processed_fills` に表示します。

累計70株の決済が約定し、残りは取消済み、観測残高は30株という合成照合結果を、例えば `data/research/confirmation.json` に保存します。金額の整数表現は1円が10,000です。

```json
{"id":"confirm-30","kind":"reconcile","order_id":"exit","cumulative_filled":70,"average_price_scaled":990000,"resolved_state":"canceled","observed_position_quantity":30}
```

```sh
go run ./cmd/kabu-research paper-recovery -dir data/research/recovery-demo -request data/research/confirmation.json apply
```

残高0株を照合するときは、`cumulative_filled` を100、`resolved_state` を `filled` にし、観測値を `"observed_position_quantity":0` と明示します。観測値が欠落するか算出値と一致しない場合、終了コード1を返し、照合待ちを維持します。

残高30株を確定した後は、新しい要求ファイルに `{"id":"close-remaining","kind":"close","quantity":30}` を保存し、同様に `apply` で処理できます。正常時は `virtual_fill=30` と `open_quantity=0` を表示します。新規エントリーの停止は維持されます。同じ約定の再受信は `{"id":"repeat-exit-fill","kind":"repeat_fill","fill_event_id":"quote-fill-6"}` で確認できます。

操作結果を `attempt-*.json` に追加保存し、`inspect` は元のチェックポイントと操作の記録を再生して検証します。保存済みの操作と同じID・内容を送ると、記録済みの結果を返します。実行には同一フォルダ内でハードリンクを作成できるローカルファイルシステムが必要です。

[ローカルでの実行手順](../how-to/verify-locally.md)
