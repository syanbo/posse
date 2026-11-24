import 'dart:io';

import 'package:code_assets/code_assets.dart';
import 'package:hooks/hooks.dart';

// 裁剪构建标签与 internal/tslink/tslink.go 保持一致（由 featuretags --min --add=... 生成）：
// 保留 netstack、nattraversal、cachenetmap、useproxy（Android 另保留 androidbin/androiddns），
// 排除 logtail 和 ipnbus。
const _tsTagsDarwin =
    'ts_omit_ace,ts_omit_acme,ts_omit_advertiseexitnode,ts_omit_advertiseroutes,'
    'ts_omit_androidbin,ts_omit_androiddns,ts_omit_appconnectors,ts_omit_aws,'
    'ts_omit_bakedroots,ts_omit_bird,ts_omit_c2n,ts_omit_captiveportal,'
    'ts_omit_capture,ts_omit_cliconndiag,ts_omit_clientmetrics,ts_omit_clientupdate,'
    'ts_omit_cloud,ts_omit_colorable,ts_omit_completion,ts_omit_completion_scripts,'
    'ts_omit_conn25,ts_omit_connreject,ts_omit_dbus,ts_omit_debug,'
    'ts_omit_debugeventbus,ts_omit_debugportmapper,ts_omit_desktop_sessions,'
    'ts_omit_dns,ts_omit_dnsresolvecache,ts_omit_doctor,ts_omit_drive,'
    'ts_omit_exitnodehealth,ts_omit_favorites,ts_omit_flashappliance,ts_omit_gro,'
    'ts_omit_health,ts_omit_hujsonconf,ts_omit_identityfederation,ts_omit_ipnbus,'
    'ts_omit_iptables,ts_omit_kube,ts_omit_linkspeed,ts_omit_linuxdnsfight,'
    'ts_omit_listenrawdisco,ts_omit_logtail,ts_omit_netlog,ts_omit_networkmanager,'
    'ts_omit_oauthkey,ts_omit_osrouter,ts_omit_outboundproxy,ts_omit_peerapiclient,'
    'ts_omit_peerapiserver,ts_omit_portlist,ts_omit_portmapper,ts_omit_posture,'
    'ts_omit_qrcodes,ts_omit_relayserver,ts_omit_remoteconfig,ts_omit_resolved,'
    'ts_omit_routecheck,ts_omit_runtimemetrics,ts_omit_sdnotify,ts_omit_serve,'
    'ts_omit_serviceclientprefs,ts_omit_ssh,ts_omit_synology,ts_omit_syslog,'
    'ts_omit_syspolicy,ts_omit_systray,ts_omit_taildrop,ts_omit_tailnetlock,'
    'ts_omit_tap,ts_omit_tpm,ts_omit_tundevstats,ts_omit_unixsocketidentity,'
    'ts_omit_useexitnode,ts_omit_usermetrics,ts_omit_useroutes,ts_omit_wakeonlan,'
    'ts_omit_webbrowser,ts_omit_webclient';

const _tsTagsAndroid =
    'ts_omit_ace,ts_omit_acme,ts_omit_advertiseexitnode,ts_omit_advertiseroutes,'
    'ts_omit_appconnectors,ts_omit_aws,'
    'ts_omit_bakedroots,ts_omit_bird,ts_omit_c2n,ts_omit_captiveportal,'
    'ts_omit_capture,ts_omit_cliconndiag,ts_omit_clientmetrics,ts_omit_clientupdate,'
    'ts_omit_cloud,ts_omit_colorable,ts_omit_completion,ts_omit_completion_scripts,'
    'ts_omit_conn25,ts_omit_connreject,ts_omit_dbus,ts_omit_debug,'
    'ts_omit_debugeventbus,ts_omit_debugportmapper,ts_omit_desktop_sessions,'
    'ts_omit_dns,ts_omit_dnsresolvecache,ts_omit_doctor,ts_omit_drive,'
    'ts_omit_exitnodehealth,ts_omit_favorites,ts_omit_flashappliance,ts_omit_gro,'
    'ts_omit_health,ts_omit_hujsonconf,ts_omit_identityfederation,ts_omit_ipnbus,'
    'ts_omit_iptables,ts_omit_kube,ts_omit_linkspeed,ts_omit_linuxdnsfight,'
    'ts_omit_listenrawdisco,ts_omit_logtail,ts_omit_netlog,ts_omit_networkmanager,'
    'ts_omit_oauthkey,ts_omit_osrouter,ts_omit_outboundproxy,ts_omit_peerapiclient,'
    'ts_omit_peerapiserver,ts_omit_portlist,ts_omit_portmapper,ts_omit_posture,'
    'ts_omit_qrcodes,ts_omit_relayserver,ts_omit_remoteconfig,ts_omit_resolved,'
    'ts_omit_routecheck,ts_omit_runtimemetrics,ts_omit_sdnotify,ts_omit_serve,'
    'ts_omit_serviceclientprefs,ts_omit_ssh,ts_omit_synology,ts_omit_syslog,'
    'ts_omit_syspolicy,ts_omit_systray,ts_omit_taildrop,ts_omit_tailnetlock,'
    'ts_omit_tap,ts_omit_tpm,ts_omit_tundevstats,ts_omit_unixsocketidentity,'
    'ts_omit_useexitnode,ts_omit_usermetrics,ts_omit_useroutes,ts_omit_wakeonlan,'
    'ts_omit_webbrowser,ts_omit_webclient';

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
          '-tags=$_tsTagsDarwin',
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
          '-tags=$_tsTagsDarwin',
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
        '-tags=$_tsTagsAndroid',
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
    for (final dir in [
      'mobile/packages/posse_quic/go',
      'internal/mquic',
      'internal/deskrelay',
      'internal/mermaid',
      'internal/tslink',
    ]) {
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
