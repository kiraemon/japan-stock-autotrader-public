# ローカルで検証する

リポジトリのルートで実行します。Goは1.26以上、Pythonは3.12を使用します。検証には合成データを使います。

## Goのテストと静的検査を実行する

```sh
go test ./...
go vet ./...
```

元の研究計画を照合するテストは、計画ファイルが手元にない場合にスキップします。合成入力を使うテストは通常どおり実行されます。

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
