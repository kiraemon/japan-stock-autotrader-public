# AGENTS.md

- [開発フロー](docs/explanation/development-flow.md)　目的・受入条件・検証結果をPRで追えるようにする。受入条件やマージの判断は人間が行う。
- [アーキテクチャ](docs/explanation/architecture.md)　起動CLI、戦略、仮想約定、台帳の境界を確認する。
- [ローカル検証](docs/how-to/verify-locally.md)　変更後は `python -B scripts/verify.py` を実行する。PRのCIも同じコマンドを使う。
- 公開版のデモとテストには合成データや模擬サーバーを使う。市場データ、実口座情報、認証情報、運用ログは公開ファイルやPRに含めない。
