# 起動手順

## 合成入力で動かす

Go 1.26 以上を用意し、クローンしたディレクトリのルートで実行します。

```sh
go run ./cmd/kabu-research session-demo -direction long -variant A
go run ./cmd/kabu-research session-demo -direction short -variant B
go run ./cmd/kabu-research paper-demo
```

実行結果の `passed` を確認します。出力は `data/research/` にローカル保存されます。調査用の合成テストとコード検査も、同じディレクトリから実行できます。

```sh
go test ./...
go vet ./...
```

過去の研究計画を照合するテストは、その計画ファイルがない場合にスキップします。合成入力で動くテストは通常どおり実行します。

## Pythonで研究コードを試す

Python 3.12で確認しています。リポジトリのルートから、標準ライブラリの `unittest` を使って実行できます。

```sh
python -B -m unittest discover -s research/python -t research/python -v
```

[MACDバックテスト](../research/python/macd_backtest)は合成分足をメモリ上で生成し、足の集約、シグナル、仮想売買の費用と約定順序を検証します。

## Windowsで読み取りCLIをビルドする

```powershell
go build -o bin/kabu-readonly.exe ./cmd/kabu-readonly
.\bin\kabu-readonly.exe -h
```

実データを取得する場合は、利用する接続先を準備します。

- **kabuステーション®API**（三菱UFJ eスマート証券）は、Windowsでkabuステーションにログインし、API機能を有効化します。 [公式の設定手順](https://kabu.com/kabustation/manual/system_setting15.html)
- **J-Quants API** は、対象データを利用できる契約とAPIキーが必要です。分足の個別取得モードには公式J-Quants CLIも使います。 [公式の利用範囲](https://jpx-jquants.com/ja/spec/data-spec)

## 扱うデータ

市場データ、実口座の情報、APIトークン、認証設定、運用ログはローカル環境で管理します。テストやIssueの再現例には合成値を使用してください。J-Quants等から取得した原データは、そのサービスの利用条件に従ってください。
