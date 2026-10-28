import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:url_launcher/url_launcher.dart';

import '../app_state.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';

/// 版本号和构建号由 scripts/ios-adhoc.sh 打包时用 --dart-define 写进来；
/// 构建号是 0 说明不是 Ad Hoc 包（调试、安卓），不查更新。
const appVersion = String.fromEnvironment(
  'POSSE_VERSION',
  defaultValue: '0.1.0',
);
const appBuild = int.fromEnvironment('POSSE_BUILD');

// 测试时可以用 --dart-define=POSSE_UPDATE_URL 指到本机
const _latestUrl = String.fromEnvironment(
  'POSSE_UPDATE_URL',
  defaultValue: 'https://posse.shaoyan.uk/ios/latest.json',
);

/// 线上最新的 iOS 安装包，来自发布脚本传到 R2 的 latest.json。
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

  /// itms-services 链接：打开后系统弹安装确认，和安装页的按钮一样。
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

/// 查有没有新的 iOS 安装包：启动时查一次，回到前台时隔 6 小时以上再查。
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

  @override
  void dispose() {
    _reset?.cancel();
    super.dispose();
  }

  // 系统要过好几秒才弹安装确认，期间按钮转圈、点不动，免得连点弹出好几次；
  // 在系统弹框里取消的话 App 收不到消息，过一会儿恢复成能再点
  Future<void> _install() async {
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
            label: Text(_busy ? '正在请求安装…' : '更新'),
          ),
          const SizedBox(height: 8),
          Text(
            '确认安装后 App 会关闭，回到主屏幕看进度，配对不会丢。',
            textAlign: TextAlign.center,
            style: TextStyle(fontSize: Fs.caption, color: c.faint),
          ),
        ],
      ),
    );
  }
}

/// iOS 才查：只有 Ad Hoc 包有 itms-services 安装页。
void startUpdateChecks() {
  if (Platform.isIOS) unawaited(updates.check());
}
