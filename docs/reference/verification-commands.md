# 検証コマンドリファレンス

| コマンド | 検証対象・入力 | 主な結果 |
| --- | --- | --- |
| `go run ./cmd/kabu-research session-demo` | 組み込みの合成時系列8ケース。`-direction` は `long` または `short`、`-variant` は `A` または `B`。 | 各ケースの `passed`、完了状態、停止理由。ケース別結果と実行記録は `data/research/` に保存。 |
| `go run ./cmd/kabu-research paper-demo` | 組み込みの仮想注文・約定イベント。 | 数量台帳の状態、検証結果、チェックポイントの照合結果。デモ結果と実行記録は `data/research/` に保存。 |
| `go test ./...` | Goのテスト。アーキテクチャの依存境界の検査を含む。 | パッケージごとのテスト結果。元の研究計画に依存するケースは対象ファイルがある場合に実行。 |
| `go vet ./...` | Goパッケージの静的検査。 | 診断があれば対象箇所と内容を表示。 |
| `python -B -m unittest discover -s research/python -t research/python -v` | メモリ上の合成分足を使ったMACD計算、分足集約、費用、約定順序のテスト。 | テスト単位の結果と合否。 |

セッションデモの `passed` は期待した状態との一致、`clean_finish` はシミュレーションが正常終了したかを示します。ペーパー台帳の `checkpoint_verified` は、保存したチェックポイントを照合できたかを示します。

[ローカルでの実行手順](../how-to/verify-locally.md)
