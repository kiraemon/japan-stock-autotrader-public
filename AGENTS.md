# AGENTS.md

- [開発フロー](docs/explanation/development-flow.md)　動作や設計を変えるときは、[Inceptionテンプレート](.github/ISSUE_TEMPLATE/inception.md)を使い、Issueで目的と受入条件を人間と合意する。PRにIssueへのリンクと検証結果を残し、マージは人間が判断する。
- [アーキテクチャ](docs/explanation/architecture.md)　起動CLI、戦略、仮想約定、台帳の境界を確認する。
- [ローカル検証](docs/how-to/verify-locally.md)　変更後は `python -B scripts/verify.py` を実行する。PRのCIも同じコマンドを使う。
- [Goカバレッジ](docs/how-to/verify-locally.md#goのカバレッジを測る)　C0はGo標準の文網羅100%、C1はgobcoが計測する分岐90%以上を目標とする。CIは計測結果を報告する段階から始め、両方が基準に達したら未達で失敗する設定に切り替える。
- 公開版のデモとテストには合成データや模擬サーバーを使う。市場データ、実口座情報、認証情報、運用ログは公開ファイルやPRに含めない。
