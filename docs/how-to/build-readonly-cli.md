# Windowsで読み取りCLIをビルドする

Go 1.26以上を用意します。PowerShellでリポジトリのルートへ移動し、ビルドしてヘルプを表示します。

```powershell
go build -o bin/kabu-readonly.exe ./cmd/kabu-readonly
.\bin\kabu-readonly.exe -h
```

実データを取得する場合は、利用する接続先を準備します。

- **kabuステーション®API**（三菱UFJ eスマート証券）は、Windowsでkabuステーションにログインし、API機能を有効化します。 [公式の設定手順](https://kabu.com/kabustation/manual/system_setting15.html)
- **J-Quants API** は、対象データを利用できる契約とAPIキーが必要です。分足の個別取得モードには公式J-Quants CLIも使います。 [公式の利用範囲](https://jpx-jquants.com/ja/spec/data-spec)

市場データ、実口座の情報、APIトークン、認証設定、運用ログはローカル環境で管理します。テストやIssueの再現例には合成値を使用してください。J-Quants等から取得した原データは、そのサービスの利用条件に従ってください。
