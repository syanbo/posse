# 参与开发 / Contributing

欢迎提 issue 和 PR。开发约定、架构和各模块的注意事项都在 [`AGENTS.md`](AGENTS.md)（人和 agent 共用，中文），各功能的行为见 [`docs/features/`](docs/features)。

## 构建和测试

```sh
export GOPROXY=https://goproxy.cn,direct   # 国内网络
go build -o bin/posse ./cmd/posse
go vet ./... && go test ./... && go tool mygo vet && go tool mygo i18n check
```

改了界面就用离屏预览自查：`POSSE_PREVIEW_DIR=/tmp/posse-preview go test ./internal/app -run TestPreview`。

手机端在 `mobile/`，只用 [fvm](https://fvm.app) 固定的 Flutter 版本：

```sh
cd mobile && fvm install && fvm flutter analyze && fvm flutter test
```

## 提 PR 之前

- 一个 PR 只做一件事，上面的测试都要过。
- 界面文字不写死：桌面写进 `locales/en.json` 和 `zh-Hans.json`（改完跑 `go tool mygo generate`），手机写进 `mobile/lib/l10n/app_en.arb` 和 `app_zh.arb`（改完跑 `fvm flutter gen-l10n`），生成的文件一起提交。
- 提交信息写成 `type(scope): 描述`，type 用 feat、fix、docs、style、refactor、perf、test、build、ci、chore、revert 之一。
- 改了功能的行为，同步改 `docs/features/` 里对应的文档。
- 安全问题不要开公开 issue，见 [`SECURITY.md`](SECURITY.md)。

---

Issues and PRs are welcome. Conventions, architecture and per-module notes live in [`AGENTS.md`](AGENTS.md) (shared by people and agents, in Chinese); feature behavior is described in [`docs/features/`](docs/features).

## Build and test

```sh
go build -o bin/posse ./cmd/posse
go vet ./... && go test ./... && go tool mygo vet && go tool mygo i18n check
```

After UI changes, check the off-screen previews: `POSSE_PREVIEW_DIR=/tmp/posse-preview go test ./internal/app -run TestPreview`.

The mobile app is in `mobile/` and uses the Flutter version pinned by [fvm](https://fvm.app):

```sh
cd mobile && fvm install && fvm flutter analyze && fvm flutter test
```

## Before opening a PR

- Keep each PR to one change, and make sure the tests above pass.
- Don’t hard-code UI text: on the desktop add it to `locales/en.json` and `zh-Hans.json` (then run `go tool mygo generate`); on mobile add it to `mobile/lib/l10n/app_en.arb` and `app_zh.arb` (then run `fvm flutter gen-l10n`); commit the generated files too.
- Write commit messages as `type(scope): description`, where type is one of feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert.
- If you change how a feature behaves, update the matching doc in `docs/features/`.
- Don’t report security issues in public issues; see [`SECURITY.md`](SECURITY.md).
