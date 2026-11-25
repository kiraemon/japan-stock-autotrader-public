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

## Windowsで読み取りCLIをビルドする

```powershell
go build -o bin/kabu-readonly.exe ./cmd/kabu-readonly
.\bin\kabu-readonly.exe -h
```

合成デモだけを試す場合は、kabuステーションの設定は不要です。読み取り接続を行う場合は、各自の利用環境と接続条件を確認し、認証値をGitに登録しないでください。

## 扱うデータ

市場データ、実口座の情報、APIトークン、認証設定、運用ログはローカル環境で管理します。テストやIssueの再現例には合成値を使用してください。J-Quants等から取得した原データは、そのサービスの利用条件に従ってください。
