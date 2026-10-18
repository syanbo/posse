# Hive 带的 flterm

pub.dev 上 flterm 0.0.5 的副本（MIT），`pubspec.yaml` 用 `dependency_overrides` 指向这里。只留了 lib、LICENSE、README、CHANGELOG 和 pubspec.yaml（去掉 `resolution: workspace`，它只在上游的 workspace 里成立）。

## 改动

改动处的注释都带 `Hive`。

- `lib/src/widgets/terminal_input_client.dart`：键盘右下角从"换行"改成"发送"（`inputAction: .send`），`performAction` 收到 send 也当回车。终端里回车就是把这一行交给程序，用户要求写"发送"。

升级 flterm 时从 pub 缓存重新复制，再把上面的改动补回来。
