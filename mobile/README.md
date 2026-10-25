# posse_mobile

Posse 手机客户端。Flutter 用 fvm 固定在 3.47.7（Dart 3.13.5），在这个目录里执行：

```sh
fvm flutter run
fvm flutter test
fvm flutter analyze
```

不要用全局 `flutter`。本机 `fvm default` 仍是 3.35.7，给别的工程用。

构建时 `packages/posse_quic` 的构建钩子会调 `go build` 把 QUIC 客户端编进 App，本机要装 Go（版本照仓库根目录的 go.mod；找不到时设 `GO` 指向 go 可执行文件），安卓还要 NDK。改了 `packages/posse_quic/go`、`internal/mquic` 后它自己重编。`packages/posse_quic` 自己的测试（起一个 Go 写的回显服务，经 FFI 收发）在那个目录里跑 `fvm dart test`。
