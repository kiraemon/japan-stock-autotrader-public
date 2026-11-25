# ローカルで検証する

リポジトリのルートで実行します。Goは1.26以上、Pythonは3.12を使用します。検証には合成データを使います。

## Macで開発する

Macでは戦略やデータ処理を開発し、Goのテスト・静的検査、Pythonのテスト、合成データでのペーパー取引を実行できます。以下のコマンドでは環境に応じて `python3` を使います。

kabuステーションへの実接続はWindowsで行います。このリポジトリのJ-Quants実データ取得も、Windowsの資格情報保護（DPAPI）と多重起動防止を使用し、銘柄指定の分足取得では固定版の `jquants.exe` を実行します。実データはWindowsで取得し、利用条件に従ってローカルでMacへ渡せます。テレメトリーのファイルロックなどWindows固有の挙動はWindowsで検証します。

## 共通の検証を実行する

```sh
python -B scripts/verify.py
```

PRのGitHub Actionsもこのコマンドを実行します。Goのテスト・静的検査、合成セッションと数量台帳のデモ、Pythonのテストを順に実行し、失敗した項目を表示して終了します。環境に応じて `python3` を使います。

## Goのテストと静的検査を実行する

```sh
go test ./...
go vet ./...
```

元の研究計画を照合するテストは、計画ファイルが手元にない場合にスキップします。合成入力を使うテストは通常どおり実行されます。

## Goのカバレッジを測る

Gitとgobco v1.3.4を使います。インストール先のGoの `bin` ディレクトリを `PATH` に追加します。場所は `go env GOBIN` で確認できます。空の場合は `go env GOPATH` が示すディレクトリの `bin` です。

```sh
go install github.com/rillig/gobco@v1.3.4
python -B scripts/go_coverage.py --report-only
```

| 指標 | 計測方法 | 目標 |
| --- | --- | ---: |
| C0（文網羅） | `go test -coverpkg=./...` の実行文数を集計 | 100% |
| C1（分岐網羅） | 各パッケージを `gobco -branch` で計測し、実行済み分岐数と全分岐数を合算 | 90% |

計測対象は実行OSでビルドされるGoコードです。CIの基準はWindowsの結果です。gobcoの分母には `select` の各ケースと `range` のループ分岐が含まれません。C1は各パッケージ自身のテストで測り、他パッケージのテストで通った分岐は集計されません。gobcoには、実行OSと異なるGoファイルを外した一時コピーを渡します。Pythonの戦略分析には既存のテストを使います。

個別に確認するときは `--metric c0` または `--metric c1` を指定します。C0だけの計測にはgobcoのインストールは不要です。C1はパッケージごとのテストを再実行するため、C0より時間がかかります。

[Goカバレッジのワークフロー](../../.github/workflows/go-coverage.yml)では、C0とC1を既存のVerifyから独立したジョブで並列に実行します。導入時は `--report-only` で未達と実測値を表示し、両指標が基準に達したらこのフラグを外します。通常モードでは丸め前の件数で判定し、計測した指標のいずれかが未達なら終了コード1になります。計測エラーは両モードで終了コード2です。

## 既存のデモを再生する

売り方向のセッションを確認する場合は1行目を、仮想注文の数量台帳を確認する場合は2行目を実行します。

```sh
go run ./cmd/kabu-research session-demo -direction short -variant B
go run ./cmd/kabu-research paper-demo
```

`passed` の表示と、保存された `data/research/` 配下の結果を確認します。

## PythonのMACDバックテストを検証する

```sh
python -B -m unittest discover -s research/python -t research/python -v
```

環境に応じて `python3` を使います。標準ライブラリだけで、[MACDバックテスト](../../research/python/macd_backtest)のテストを実行できます。

[主要な検証コマンドと結果](../reference/verification-commands.md)
