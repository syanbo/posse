import 'dart:io';

import 'package:code_assets/code_assets.dart';
import 'package:hooks/hooks.dart';

/// 用 go build 把 go/ 编成动态库。Go 不支持给 iOS 编动态库，iOS 先编静态库再用 clang 链成动态库。
/// 苹果平台留出头部空间：打包时要改库的 install name。
/// go/ 在仓库的 Go 模块里（引用 internal/mquic、internal/mermaid），所以在仓库根目录下编。
void main(List<String> args) async {
  await build(args, (input, output) async {
    if (!input.config.buildCodeAssets) return;
    final code = input.config.code;
    final os = code.targetOS;
    final arch = code.targetArchitecture;
    final pkg = input.packageRoot;
    final repo = Directory.fromUri(pkg.resolve('../../../')).absolute.path;
    final out = input.outputDirectory;
    final go = _goBinary();

    final env = <String, String>{
      'CGO_ENABLED': '1',
      'GOFLAGS': '-trimpath',
      if (!Platform.environment.containsKey('GOPROXY'))
        'GOPROXY': 'https://goproxy.cn,direct',
    };
    final ldflags = <String>['-s', '-w'];
    final String libName;
    final String file;

    if (os == OS.macOS || os == OS.iOS) {
      final sdk = os == OS.macOS
          ? 'macosx'
          : code.iOS.targetSdk == IOSSdk.iPhoneOS
          ? 'iphoneos'
          : 'iphonesimulator';
      final sysroot = _run('xcrun', ['--sdk', sdk, '--show-sdk-path']);
      final clang = _run('xcrun', ['--sdk', sdk, '-f', 'clang']);
      final target = _appleTarget(os, arch, sdk, code);
      final flags = '-isysroot $sysroot -target $target';
      env.addAll({
        'GOOS': os == OS.macOS ? 'darwin' : 'ios',
        'GOARCH': _goArch(arch),
        'CC': clang,
        'CGO_CFLAGS': flags,
        'CGO_LDFLAGS': flags,
      });
      libName = 'libposse_quic.dylib';
      file = out.resolve(libName).toFilePath();
      if (os == OS.macOS) {
        await _go(go, repo, env, [
          'build',
          '-buildmode=c-shared',
          '-ldflags=${ldflags.join(' ')} -extldflags=-Wl,-install_name,@rpath/$libName,-headerpad_max_install_names',
          '-o',
          file,
          './mobile/packages/posse_quic/go',
        ]);
      } else {
        final archive = out.resolve('libposse_quic.a').toFilePath();
        await _go(go, repo, env, [
          'build',
          '-buildmode=c-archive',
          '-ldflags=${ldflags.join(' ')}',
          '-o',
          archive,
          './mobile/packages/posse_quic/go',
        ]);
        await _exec(clang, [
          '-isysroot', sysroot, '-target', target,
          '-dynamiclib', '-Wl,-all_load', archive,
          '-framework', 'CoreFoundation', '-framework', 'Security',
          '-lresolv', '-install_name', '@rpath/$libName',
          '-Wl,-headerpad_max_install_names',
          '-o', file,
        ], repo, const {});
      }
    } else if (os == OS.android) {
      final cc = code.cCompiler?.compiler.toFilePath();
      if (cc == null) {
        throw StateError('posse_quic: no Android NDK compiler configured');
      }
      final triple = switch (arch) {
        Architecture.arm64 => 'aarch64-linux-android',
        Architecture.arm => 'armv7a-linux-androideabi',
        Architecture.x64 => 'x86_64-linux-android',
        Architecture.ia32 => 'i686-linux-android',
        _ => throw UnsupportedError('posse_quic: android $arch'),
      };
      final api = code.android.targetNdkApi;
      env.addAll({
        'GOOS': 'android',
        'GOARCH': _goArch(arch),
        if (arch == Architecture.arm) 'GOARM': '7',
        'CC': cc,
        'CGO_CFLAGS': '--target=$triple$api',
        'CGO_LDFLAGS': '--target=$triple$api',
      });
      libName = 'libposse_quic.so';
      file = out.resolve(libName).toFilePath();
      // 16 KB 页对齐：Android 15 起的设备要求
      await _go(go, repo, env, [
        'build',
        '-buildmode=c-shared',
        '-ldflags=${ldflags.join(' ')} -extldflags=-Wl,-soname,$libName,-z,max-page-size=16384',
        '-o',
        file,
        './mobile/packages/posse_quic/go',
      ]);
    } else {
      return;
    }

    output.assets.code.add(
      CodeAsset(
        package: input.packageName,
        name: 'posse_quic.dart',
        linkMode: DynamicLoadingBundled(),
        file: Uri.file(file),
      ),
    );
    for (final dir in ['mobile/packages/posse_quic/go', 'internal/mquic', 'internal/deskrelay', 'internal/mermaid']) {
      for (final f in Directory('$repo/$dir').listSync()) {
        if (f is File && f.path.endsWith('.go') && !f.path.endsWith('_test.go')) {
          output.dependencies.add(f.uri);
        }
      }
    }
    // Pele 由 go:embed 编进去
    output.dependencies.add(Uri.file('$repo/internal/mermaid/pele.js.gz'));
    output.dependencies.add(Uri.file('$repo/go.mod'));
    output.dependencies.add(Uri.file('$repo/go.sum'));
  });
}

String _goArch(Architecture arch) => switch (arch) {
  Architecture.arm64 => 'arm64',
  Architecture.x64 => 'amd64',
  Architecture.arm => 'arm',
  Architecture.ia32 => '386',
  _ => throw UnsupportedError('posse_quic: $arch'),
};

String _appleTarget(OS os, Architecture arch, String sdk, CodeConfig code) {
  final a = arch == Architecture.arm64 ? 'arm64' : 'x86_64';
  if (os == OS.macOS) return '$a-apple-macos${code.macOS.targetVersion}';
  final suffix = sdk == 'iphonesimulator' ? '-simulator' : '';
  return '$a-apple-ios${code.iOS.targetVersion}$suffix';
}

/// Xcode 跑构建脚本时 PATH 很短，找不到 Homebrew 装的 go。
String _goBinary() {
  final fromEnv = Platform.environment['GO'];
  if (fromEnv != null && fromEnv.isNotEmpty) return fromEnv;
  for (final p in ['/opt/homebrew/bin/go', '/usr/local/go/bin/go', '/usr/local/bin/go']) {
    if (File(p).existsSync()) return p;
  }
  return 'go';
}

String _run(String cmd, List<String> args) {
  final r = Process.runSync(cmd, args);
  if (r.exitCode != 0) {
    throw ProcessException(cmd, args, '${r.stderr}', r.exitCode);
  }
  return (r.stdout as String).trim();
}

Future<void> _go(String go, String repo, Map<String, String> env, List<String> args) =>
    _exec(go, args, repo, env);

Future<void> _exec(String cmd, List<String> args, String dir, Map<String, String> env) async {
  final r = await Process.run(cmd, args, workingDirectory: dir, environment: env);
  if (r.exitCode != 0) {
    throw ProcessException(cmd, args, '${r.stdout}\n${r.stderr}', r.exitCode);
  }
}
