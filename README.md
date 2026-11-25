# Japan Stock Autotrader

日本株の市場データ収集とペーパー取引を題材にした Go プロジェクトです。分足の受理、戦略判断、時刻付きの仮想約定、数量台帳の照合を、合成入力からローカルで再現できます。

## 試す

Go 1.26 以上が必要です。合成デモとテストは、証券口座やAPIキーなしで実行できます。

```sh
go run ./cmd/kabu-research session-demo -direction long -variant A
go run ./cmd/kabu-research paper-demo
go test ./...
go vet ./...
```

セッションデモでは、部分約定、期限切れ、データ途絶、時計の逆行など8ケースを実行します。`passed=true` は期待した状態との一致を示します。たとえば入力が途絶えたケースでは、残った仮想ポジションを台帳に保持して停止します。デモ結果はローカルの `data/research/` に出力されます。

## 構成

| 領域 | 主な役割 |
| --- | --- |
| [市場データ](internal/marketdata) | 価格・数量・時刻の型、分足や気配の正規化 |
| [入力ゲート](internal/features/streamsignals) | 確定足の受理、重複判定、入力待ちの期限 |
| [戦略](internal/strategy) | 入力された足からの判断 |
| [ペーパー取引](internal/features/papertrade) | 新しい気配を使った仮想約定、数量台帳と復元 |
| [記録と照合](internal/paperrecordingv3) | サイズ上限を持つイベント記録と、検証できた範囲の読み戻し |
| [起動CLI](cmd) | オフライン研究、読み取り収集、ローカル表示の組み立て |

詳しい流れは [アーキテクチャ](docs/ARCHITECTURE.md) と [起動手順](docs/QUICKSTART.md) にあります。

コードと合成テストを公開しています。利用者が取得した市場データや運用記録は各自のローカル環境で扱います。プロジェクトのソースコードにはオープンソースライセンスを付与しておらず、権利は権利者に留保されます。GitHub上での閲覧・フォークにはGitHubの利用規約が適用されます。
