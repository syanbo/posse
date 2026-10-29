import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:url_launcher/url_launcher.dart';

import '../app_state.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';

/// 版本号和构建号由 scripts/ios-adhoc.sh、scripts/android-release.sh 打包时用 --dart-define 写进来；
/// 构建号是 0 说明不是发布的包（调试、自己编的），不查更新。
const appVersion = String.fromEnvironment(
  'POSSE_VERSION',
  defaultValue: '0.1.0',
);
const appBuild = int.fromEnvironment('POSSE_BUILD');

// 测试时可以用 --dart-define=POSSE_UPDATE_URL 指到本机
const _updateUrl = String.fromEnvironment('POSSE_UPDATE_URL');

String get _latestUrl => _updateUrl.isNotEmpty
    ? _updateUrl
    : 'https://posse.shaoyan.uk/${Platform.isAndroid ? 'android' : 'ios'}/latest.json';

/// 线上最新的安装包，来自发布脚本传到 R2 的 latest.json。
class AppRelease {
  const AppRelease({
    required this.version,
    required this.build,
    required this.size,
    required this.notes,
    required this.install,
  });

  final String version;
  final int build;

  /// 已经排好的大小，如「14.5 MB」。
  final String size;

  /// mobile/CHANGELOG.md 里这个版本的条目，一行一条。
  final List<String> notes;

  /// iOS 是 itms-services 链接：打开后系统弹安装确认，和安装页的按钮一样；安卓是 APK 的地址。
  final String install;

  static AppRelease? parse(Object? json) {
    if (json is! Map) return null;
    final version = json['version'];
    final build = json['build'];
    final install = json['install'];
    if (version is! String || build is! int || install is! String) return null;
    final notes = json['notes'];
    return AppRelease(
      version: version,
      build: build,
      size: json['size'] is String ? json['size'] as String : '',
      notes: [
        if (notes is List)
          for (final n in notes)
            if (n is String && n.isNotEmpty) n,
      ],
      install: install,
    );
  }
}

/// 查有没有新的安装包：启动时查一次，回到前台时隔 6 小时以上再查。
/// 比自己的构建号新就记在 [latest]，首页和设置页据此显示更新。
class UpdateChecker extends ChangeNotifier {
  UpdateChecker({this.build = appBuild, this.fetch = _fetchLatest});

  final int build;
  final Future<Object?> Function() fetch;

  AppRelease? latest;
  DateTime? _checked;

  bool get enabled => build > 0;

  Future<void> check({bool force = false}) async {
    if (!enabled) return;
    final now = DateTime.now();
    if (!force &&
        _checked != null &&
        now.difference(_checked!) < const Duration(hours: 6)) {
      return;
    }
    _checked = now;
    try {
      final release = AppRelease.parse(await fetch());
      final next = release != null && release.build > build ? release : null;
      if (next?.build != latest?.build) {
        latest = next;
        notifyListeners();
      }
    } catch (_) {
      // 网络不通下次再查，不打扰
      _checked = null;
    }
  }
}

Future<Object?> _fetchLatest() async {
  final client = HttpClient()..connectionTimeout = const Duration(seconds: 10);
  try {
    final req = await client.getUrl(Uri.parse(_latestUrl));
    req.headers.set(HttpHeaders.cacheControlHeader, 'no-cache');
    final res = await req.close().timeout(const Duration(seconds: 15));
    if (res.statusCode != 200) return null;
    return jsonDecode(await res.transform(utf8.decoder).join());
  } finally {
    client.close();
  }
}

/// 全 App 共用一个，main 里启动。
final updates = UpdateChecker();

/// 首页列表最上面的更新提示：点开看更新内容，× 之后这个构建不再提示（设置页还能找到）。
class UpdateBanner extends StatelessWidget {
  const UpdateBanner({super.key, required this.model, this.checker});

  final PosseAppModel model;
  final UpdateChecker? checker;

  @override
  Widget build(BuildContext context) {
    final checker = this.checker ?? updates;
    return ListenableBuilder(
      listenable: checker,
      builder: (context, _) {
        final release = checker.latest;
        if (release == null ||
            model.settings.skippedUpdateBuild == release.build) {
          return const SizedBox.shrink();
        }
        final c = PosseColors.of(context);
        return Padding(
          padding: const EdgeInsets.fromLTRB(10, 0, 10, 10),
          child: Material(
            color: c.accent.withValues(alpha: c.dark ? 0.16 : 0.10),
            borderRadius: BorderRadius.circular(Rd.card),
            clipBehavior: Clip.antiAlias,
            child: InkWell(
              onTap: () => showUpdateSheet(context, release),
              child: Padding(
                // 和下面的用量条一样矮：版本用正文色、大小用灰字，强调色只留给图标
                padding: const EdgeInsets.fromLTRB(14, 4, 4, 4),
                child: Row(
                  children: [
                    Icon(
                      Icons.system_update_rounded,
                      size: 18,
                      color: c.accent,
                    ),
                    const SizedBox(width: 10),
                    Expanded(
                      child: Text.rich(
                        TextSpan(
                          text: '新版本 ${release.version}（${release.build}）',
                          style: TextStyle(
                            fontWeight: FontWeight.w500,
                            color: c.text,
                          ),
                          children: [
                            if (release.size.isNotEmpty)
                              TextSpan(
                                text: ' · ${release.size}',
                                style: TextStyle(
                                  fontWeight: FontWeight.w400,
                                  color: c.muted,
                                ),
                              ),
                          ],
                        ),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(fontSize: Fs.meta),
                      ),
                    ),
                    // IconButton 会被撑到最小点击尺寸，条就比用量条高一截
                    Tooltip(
                      message: '不再提示这个版本',
                      child: InkResponse(
                        radius: 18,
                        onTap: () => model.updateSettings(
                          (s) => s.skippedUpdateBuild = release.build,
                        ),
                        child: SizedBox(
                          width: 36,
                          height: 28,
                          child: Icon(
                            Icons.close_rounded,
                            size: 16,
                            color: c.muted,
                          ),
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ),
        );
      },
    );
  }
}

/// 更新内容和「更新」按钮。
Future<void> showUpdateSheet(BuildContext context, AppRelease release) =>
    showModalBottomSheet<void>(
      context: context,
      showDragHandle: true,
      isScrollControlled: true,
      useSafeArea: true,
      constraints: BoxConstraints(
        maxHeight: MediaQuery.sizeOf(context).height * 0.85,
      ),
      builder: (_) => _UpdateSheet(release: release),
    );

class _UpdateSheet extends StatefulWidget {
  const _UpdateSheet({required this.release});

  final AppRelease release;

  @override
  State<_UpdateSheet> createState() => _UpdateSheetState();
}

class _UpdateSheetState extends State<_UpdateSheet> {
  var _busy = false;
  Timer? _reset;

  /// 安卓下载 APK 的进度，0 到 1；不知道总大小时为 null。
  double? _progress;
  String? _error;

  @override
  void dispose() {
    _reset?.cancel();
    super.dispose();
  }

  // 系统要过好几秒才弹安装确认，期间按钮转圈、点不动，免得连点弹出好几次；
  // 在系统弹框里取消的话 App 收不到消息，过一会儿恢复成能再点
  Future<void> _install() async {
    if (Platform.isAndroid) return _installApk();
    setState(() => _busy = true);
    _reset = Timer(const Duration(seconds: 15), () {
      if (mounted) setState(() => _busy = false);
    });
    final ok = await launchUrl(
      Uri.parse(widget.release.install),
      mode: LaunchMode.externalApplication,
    );
    if (!ok && mounted) setState(() => _busy = false);
  }

  // 下完交给系统安装器，确认后覆盖安装、App 被换掉；在安装器里取消会回到这里，按钮恢复。
  // 弹窗关了下载照样接着下，但不弹安装器：重新打开点「更新」接上同一次下载或直接用下好的包
  Future<void> _installApk() async {
    setState(() {
      _busy = true;
      _progress = _apkProgress.value;
      _error = null;
    });
    void follow() {
      if (mounted) setState(() => _progress = _apkProgress.value);
    }

    _apkProgress.addListener(follow);
    try {
      final path = await _fetchApk(widget.release);
      if (!mounted) return;
      setState(() => _progress = 1);
      final how = await _updateChannel.invokeMethod<String>('install', path);
      if (!mounted) return;
      setState(() {
        _busy = false;
        if (how == 'denied') _error = '要在系统设置里允许 Posse 安装应用，才能更新';
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = e is PlatformException
            ? '打不开安装：${e.message ?? e.code}'
            : '下载失败，稍后再试';
      });
    } finally {
      _apkProgress.removeListener(follow);
    }
  }

  String get _busyLabel {
    if (!Platform.isAndroid) return '正在请求安装…';
    final p = _progress;
    if (p == null) return '正在下载…';
    if (p >= 1) return '正在打开安装…';
    return '正在下载 ${(p * 100).floor()}%';
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final r = widget.release;
    final bottom = MediaQuery.paddingOf(context).bottom;
    return Padding(
      padding: EdgeInsets.fromLTRB(20, 0, 20, 16 + bottom),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            '新版本 ${r.version}',
            style: TextStyle(
              fontSize: Fs.cardTitle,
              fontWeight: FontWeight.w600,
              color: c.text,
            ),
          ),
          const SizedBox(height: 4),
          Text(
            [
              '构建 ${r.build}',
              if (r.size.isNotEmpty) r.size,
              '当前 $appVersion（$appBuild）',
            ].join(' · '),
            style: TextStyle(fontSize: Fs.caption, color: c.muted),
          ),
          const SizedBox(height: 14),
          Flexible(
            child: SingleChildScrollView(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  for (final note in r.notes)
                    Padding(
                      padding: const EdgeInsets.only(bottom: 8),
                      child: Row(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            '•  ',
                            style: TextStyle(fontSize: Fs.body, color: c.muted),
                          ),
                          Expanded(
                            child: Text(
                              note,
                              style: TextStyle(
                                fontSize: Fs.body,
                                height: 1.45,
                                color: c.text,
                              ),
                            ),
                          ),
                        ],
                      ),
                    ),
                ],
              ),
            ),
          ),
          const SizedBox(height: 12),
          FilledButton.icon(
            onPressed: _busy ? null : _install,
            icon: _busy
                ? const HexSpinner(size: 16, color: Colors.white)
                : const Icon(Icons.system_update_rounded, size: 20),
            label: Text(_busy ? _busyLabel : '更新'),
          ),
          if (_error case final error?) ...[
            const SizedBox(height: 8),
            Text(
              error,
              textAlign: TextAlign.center,
              style: TextStyle(fontSize: Fs.caption, color: c.danger),
            ),
          ],
          const SizedBox(height: 8),
          Text(
            Platform.isAndroid
                ? '下载完系统会弹出安装确认，覆盖安装，配对不会丢。第一次更新要在系统设置里允许 Posse 安装应用。'
                : '点安装后在后台下载，回到主屏幕能在图标上看到进度，装好后 App 会自动换成新版，配对不会丢。',
            textAlign: TextAlign.center,
            style: TextStyle(fontSize: Fs.caption, color: c.faint),
          ),
        ],
      ),
    );
  }
}

const _updateChannel = MethodChannel('posse/update');

/// 正在下的安装包，同一个构建只下一份：弹窗关了再开、连点都接上它，不会两份同时写一个文件。
Future<String>? _apkTask;
int _apkTaskBuild = 0;
final _apkProgress = ValueNotifier<double?>(0);

Future<String> _fetchApk(AppRelease release) {
  if (_apkTask case final task? when _apkTaskBuild == release.build) {
    return task;
  }
  _apkProgress.value = 0;
  final task = downloadApk(release, onProgress: (p) => _apkProgress.value = p);
  _apkTask = task;
  _apkTaskBuild = release.build;
  task.whenComplete(() {
    if (identical(_apkTask, task)) _apkTask = null;
  }).ignore();
  return task;
}

/// 把安卓的安装包下载到原生层给的更新目录（文件名带构建号，原生层据此清理装过的旧包），返回路径。
/// 先写 .part，下完、大小对上再改名，半截的包不交给安装器。
Future<String> downloadApk(
  AppRelease release, {
  void Function(double? progress)? onProgress,
}) async {
  final dir = await _updateChannel.invokeMethod<String>('dir');
  if (dir == null) throw StateError('no update dir');
  final file = File('$dir/Posse-${release.build}.apk');
  // 只有下完、大小对上才改成这个名字，上次下好没装的直接用
  if (await file.exists()) return file.path;
  final part = File('${file.path}.part');
  final client = HttpClient()..connectionTimeout = const Duration(seconds: 15);
  try {
    final req = await client.getUrl(Uri.parse(release.install));
    final res = await req.close();
    if (res.statusCode != 200) {
      throw HttpException('HTTP ${res.statusCode}', uri: req.uri);
    }
    final total = res.contentLength;
    final sink = part.openWrite();
    var got = 0;
    var shown = -1;
    try {
      // 一段时间收不到数据就算断了，免得按钮一直转
      await for (final chunk in res.timeout(const Duration(seconds: 30))) {
        sink.add(chunk);
        got += chunk.length;
        if (total > 0) {
          final pct = got * 100 ~/ total;
          if (pct != shown) {
            shown = pct;
            onProgress?.call(got / total);
          }
        } else if (shown < 0) {
          shown = 0;
          onProgress?.call(null);
        }
      }
    } finally {
      await sink.close();
    }
    if (total > 0 && got != total) {
      throw HttpException('short download $got/$total', uri: req.uri);
    }
    return (await part.rename(file.path)).path;
  } catch (_) {
    if (await part.exists()) await part.delete();
    rethrow;
  } finally {
    client.close();
  }
}

/// iOS 的 Ad Hoc 包和安卓的发布包查更新，构建号是 0 的不查（见 [UpdateChecker.enabled]）。
void startUpdateChecks() {
  if (Platform.isIOS || Platform.isAndroid) unawaited(updates.check());
}
