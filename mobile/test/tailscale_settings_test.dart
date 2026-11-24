import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/chat/latency_label.dart';
import 'package:posse_mobile/desk/chrome.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/protocol/tailscale_service.dart';
import 'package:posse_mobile/push/push.dart';
import 'package:posse_mobile/settings/settings.dart';
import 'package:posse_mobile/settings/tailscale_screen.dart';
import 'package:posse_mobile/ui/theme.dart';
import 'package:posse_quic/posse_quic.dart';

class _FakeTsBackend implements TailscaleBackend {
  final _ctrl = StreamController<TailscaleStatus>.broadcast();
  TailscaleStatus current = const TailscaleStatus();
  Object? startError;
  bool emitNativeErrorOnStart = false;
  TailscaleResetResult? nextResetResult;
  Completer<void>? startGate;

  final startCalls =
      <
        ({
          String stateDir,
          String hostname,
          String controlUrl,
          String authKey,
          bool disableBindInterface,
        })
      >[];
  int stopCalls = 0;
  int resetCalls = 0;
  int loginCalls = 0;

  void emit(TailscaleStatus st) {
    current = st;
    _ctrl.add(st);
  }

  @override
  Stream<TailscaleStatus> get updates => _ctrl.stream;

  @override
  TailscaleStatus status() => current;

  @override
  Future<String> stateDir() async => '/tmp/posse-ts-test';

  @override
  Future<TailscaleStatus> start({
    required String stateDir,
    String hostname = '',
    String controlUrl = '',
    String authKey = '',
    bool disableBindInterface = false,
  }) async {
    startCalls.add((
      stateDir: stateDir,
      hostname: hostname,
      controlUrl: controlUrl,
      authKey: authKey,
      disableBindInterface: disableBindInterface,
    ));
    if (startGate != null) {
      await startGate!.future;
    }
    if (emitNativeErrorOnStart) {
      // 模拟 internal/tslink/tslink.go:845 原生启动失败先发出 Enabled=true, State="error" 快照
      current = const TailscaleStatus(
        enabled: true,
        state: 'error',
        error: 'state directory unavailable',
      );
      _ctrl.add(current);
      if (startError != null) throw startError!;
      return current;
    }
    if (startError != null) throw startError!;
    current = TailscaleStatus(
      enabled: true,
      state: 'running',
      ip: '100.64.0.9',
      dnsName: 'phone.tailnet.ts.net.',
      hostname: hostname.isEmpty ? 'posse-mobile' : hostname,
      controlUrl: controlUrl,
      disableBindInterface: disableBindInterface,
    );
    _ctrl.add(current);
    return current;
  }

  @override
  Future<TailscaleStatus> stop() async {
    stopCalls++;
    current = const TailscaleStatus(enabled: false, state: 'stopped');
    _ctrl.add(current);
    return current;
  }

  @override
  Future<TailscaleResetResult> reset({String stateDir = ''}) async {
    resetCalls++;
    final res =
        nextResetResult ??
        const TailscaleResetResult(
          status: TailscaleStatus(enabled: false, state: 'disabled'),
          loggedOut: true,
          logoutError: '',
          localCleared: true,
        );
    current = res.status;
    _ctrl.add(current);
    return res;
  }

  @override
  Future<TailscaleStatus> startLogin() async {
    loginCalls++;
    current = const TailscaleStatus(
      enabled: true,
      state: 'needs_login',
      authUrl: 'https://login.tailscale.com/a/refreshed',
    );
    _ctrl.add(current);
    return current;
  }

  @override
  void wake() {}

  @override
  Future<TailscalePeerRoute> peerRoute(String ip) async =>
      const TailscalePeerRoute(kind: 'direct', addr: '1.2.3.4:41641');

  @override
  Future<TailscaleUdpBridge> openUdpBridge(String host, int port) =>
      throw UnimplementedError();

  Future<void> dispose() => _ctrl.close();
}

class _SlowPush implements PushPlatform {
  _SlowPush(this.gate);
  final Completer<void> gate;

  @override
  set onOpened(void Function()? callback) {}

  @override
  Future<PushAuthorization> authorization() async {
    await gate.future;
    return const PushAuthorization('denied');
  }

  @override
  Future<bool> request() async => false;

  @override
  Future<String> token() async => '';

  @override
  Future<String> key() async => '';

  @override
  Future<bool> sandbox() async => false;

  @override
  Future<void> setLanguage(String tag) async {}

  @override
  Future<void> setBadge(int count) async {}

  @override
  Future<void> configure({required bool alerts, required bool badge}) async {}

  @override
  Future<void> openSettings() async {}

  @override
  Future<Map<String, String>?> takeOpened() async => null;
}

Widget _wrap(Widget child, {Locale locale = const Locale('en')}) => MaterialApp(
  locale: locale,
  localizationsDelegates: AppLocalizations.localizationsDelegates,
  supportedLocales: AppLocalizations.supportedLocales,
  theme: posseTheme(Brightness.dark),
  home: child,
);

void main() {
  test(
    'routeLabel and deskPathLabel localize Tailscale Direct/DERP/Unknown',
    () {
      final en = lookupAppLocalizations(const Locale('en'));
      final zh = lookupAppLocalizations(const Locale('zh'));

      const direct = TailscalePeerRoute(kind: 'direct');
      const peerRelay = TailscalePeerRoute(kind: 'peer_relay');
      const derp = TailscalePeerRoute(kind: 'derp', relay: 'tok');
      const unknown = TailscalePeerRoute();

      expect(routeLabel(en, null), isNull);
      expect(routeLabel(en, 'hquic://192.168.1.2:6789'), 'Direct');
      expect(routeLabel(en, 'hquic://relay.example:443?relay=abc'), 'Relay');
      expect(
        routeLabel(en, 'tsquic://100.64.0.2:6789', peerRoute: direct),
        'Tailscale Direct',
      );
      expect(
        routeLabel(en, 'tsquic://100.64.0.2:6789', peerRoute: peerRelay),
        'Tailscale Direct',
      );
      expect(
        routeLabel(en, 'tsquic://100.64.0.2:6789', peerRoute: derp),
        'Tailscale Relay (DERP)',
      );
      expect(
        routeLabel(en, 'tsquic://100.64.0.2:6789', peerRoute: unknown),
        'Tailscale',
      );
      expect(routeLabel(en, 'tsquic://100.64.0.2:6789'), 'Tailscale');

      expect(
        routeLabel(zh, 'tsquic://100.64.0.2:6789', peerRoute: direct),
        'Tailscale 直连',
      );
      expect(
        routeLabel(zh, 'tsquic://100.64.0.2:6789', peerRoute: derp),
        'Tailscale 中继 (DERP)',
      );

      expect(deskPathLabel(en, 'lan'), 'Direct');
      expect(deskPathLabel(en, 'relay'), 'Relay');
      expect(deskPathLabel(en, 'punched'), 'Direct (Hole-Punched)');
      expect(deskPathLabel(en, 'tailscale'), 'Tailscale');
      expect(
        deskPathLabel(en, 'tailscale', peerRoute: direct),
        'Tailscale Direct',
      );
      expect(
        deskPathLabel(en, 'tailscale', peerRoute: peerRelay),
        'Tailscale Direct',
      );
      expect(
        deskPathLabel(en, 'tailscale', peerRoute: derp),
        'Tailscale Relay (DERP)',
      );
      expect(deskPathLabel(zh, 'tailscale', peerRoute: direct), 'Tailscale 直连');
      expect(
        deskPathLabel(zh, 'tailscale', peerRoute: derp),
        'Tailscale 中继 (DERP)',
      );
    },
  );

  test('URL validators match native ValidateControlURL and auth URL rules', () {
    expect(
      parseSafeTailscaleAuthUrl('https://login.tailscale.com/a/123?k=1')?.host,
      'login.tailscale.com',
    );
    expect(parseSafeTailscaleAuthUrl('https://@evil.test/a'), isNull);
    expect(parseSafeTailscaleAuthUrl('https://evil.test:/a'), isNull);
    expect(parseSafeTailscaleAuthUrl('javascript:alert(1)'), isNull);
    expect(parseSafeTailscaleAuthUrl('file:///etc/passwd'), isNull);

    expect(
      parseSafeTailscaleControlUrl('https://headscale.example')?.host,
      'headscale.example',
    );
    expect(parseSafeTailscaleControlUrl('http://[::1]:8080')?.port, 8080);
    expect(
      parseSafeTailscaleControlUrl('http://[fe80::1%25en0]:8443')?.port,
      8443,
    );
    expect(
      parseSafeTailscaleAuthUrl('http://[fe80::1%25en0]:8443/login')?.port,
      8443,
    );
    expect(parseSafeTailscaleControlUrl('https://büro.example'), isNotNull);
    expect(parseSafeTailscaleControlUrl('http://[fe80::1%25]:8443'), isNull);
    expect(parseSafeTailscaleControlUrl(''), isNull);
    expect(
      parseSafeTailscaleControlUrl('https://headscale.example/?x=1'),
      isNull,
    );
    expect(
      parseSafeTailscaleControlUrl('https://headscale.example/#frag'),
      isNull,
    );
    expect(parseSafeTailscaleControlUrl('https://@headscale.example'), isNull);
    expect(
      parseSafeTailscaleControlUrl('https://user:pass@headscale.example'),
      isNull,
    );
    expect(parseSafeTailscaleControlUrl('https://headscale.example:'), isNull);
    expect(parseSafeTailscaleControlUrl('https://[::1]:'), isNull);
    expect(parseSafeTailscaleControlUrl('https://headscale.example:0'), isNull);
    expect(
      parseSafeTailscaleControlUrl('https://headscale.example:08080'),
      isNull,
    );
    expect(
      parseSafeTailscaleControlUrl('https://headscale.example:65536'),
      isNull,
    );
    expect(parseSafeTailscaleControlUrl('https://bad host'), isNull);
    expect(parseSafeTailscaleControlUrl('https://headscale.local/%ZZ'), isNull);
    expect(parseSafeTailscaleControlUrl('https://headscale.local:+80'), isNull);
    expect(parseSafeTailscaleControlUrl('https://headscale%2elocal'), isNull);
  });

  test('friendlyError does not misclassify unrelated PlatformException as Tailscale error', () {
    final en = lookupAppLocalizations(const Locale('en'));
    final unrelated = PlatformException(code: 'camera_error');
    expect(friendlyError(unrelated, en), isNot(en.tailscaleErrorGeneric));
    expect(
      friendlyError(TailscaleException('state directory unavailable'), en),
      en.tailscaleErrorStateDir,
    );
  });

  testWidgets(
    'SettingsScreen shows Built-in Tailscale entry only when supported',
    (tester) async {
      final unsupported = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
      );
      await tester.pumpWidget(
        _wrap(SettingsScreen(model: unsupported, onComputers: () {})),
      );
      expect(find.byKey(const Key('settings-tailscale-entry')), findsNothing);

      final backend = _FakeTsBackend();
      addTearDown(backend.dispose);
      final supported = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
        tailscaleBackend: backend,
      );
      await tester.pumpWidget(
        _wrap(SettingsScreen(model: supported, onComputers: () {})),
      );
      await tester.scrollUntilVisible(
        find.byKey(const Key('settings-tailscale-entry')),
        200,
      );
      expect(find.byKey(const Key('settings-tailscale-entry')), findsOneWidget);
      expect(find.text('Off'), findsOneWidget);

      await tester.tap(find.byKey(const Key('settings-tailscale-entry')));
      await tester.pumpAndSettle();
      expect(find.byType(TailscaleSettingsScreen), findsOneWidget);
    },
  );

  testWidgets(
    'enable toggle starts/stops node and stays disabled on native error snapshot',
    (tester) async {
      final backend = _FakeTsBackend();
      addTearDown(backend.dispose);
      final settingsStore = MemorySettingsStore();
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: settingsStore,
        tailscaleBackend: backend,
      );

      await tester.pumpWidget(_wrap(TailscaleSettingsScreen(model: model)));

      // 原生发出 Enabled=true, State="error" 且抛错：绝不能持久化为开启，并显示本地化文案
      backend.emitNativeErrorOnStart = true;
      backend.startError = TailscaleException('state directory unavailable');
      await tester.tap(find.byKey(const Key('tailscale-enable-switch')));
      await tester.pumpAndSettle();
      expect(model.settings.tailscaleEnabled, isFalse);
      expect(AppSettings.decode(settingsStore.data).tailscaleEnabled, isFalse);
      expect(find.byKey(const Key('tailscale-error-banner')), findsOneWidget);
      expect(
        find.text('Couldn’t access the local Tailscale state directory.'),
        findsOneWidget,
      );

      // 原生正常返回 Enabled=true, State="error"（不抛异常）也不得持久化为开启
      backend.startError = null;
      await tester.tap(find.byKey(const Key('tailscale-enable-switch')));
      await tester.pumpAndSettle();
      expect(model.settings.tailscaleEnabled, isFalse);

      // 恢复正常后开启与关闭
      backend.emitNativeErrorOnStart = false;
      await tester.tap(find.byKey(const Key('tailscale-enable-switch')));
      await tester.pumpAndSettle();
      expect(model.settings.tailscaleEnabled, isTrue);
      expect(find.byKey(const Key('tailscale-ip-text')), findsOneWidget);
      expect(find.text('100.64.0.9'), findsOneWidget);

      await tester.tap(find.byKey(const Key('tailscale-enable-switch')));
      await tester.pumpAndSettle();
      expect(backend.stopCalls, 1);
      expect(model.settings.tailscaleEnabled, isFalse);
    },
  );

  testWidgets(
    'needs_login handles empty URL, validates copy & open, and drops stale launcher errors',
    (tester) async {
      final backend = _FakeTsBackend();
      addTearDown(backend.dispose);
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
        tailscaleBackend: backend,
      );
      model.updateSettings((s) => s.tailscaleEnabled = true);

      final launched = <Uri>[];
      Completer<bool>? launchCompleter;
      String? copiedText;
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            copiedText = (call.arguments as Map)['text'] as String?;
          }
          return null;
        },
      );
      addTearDown(
        () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
          SystemChannels.platform,
          null,
        ),
      );

      await tester.pumpWidget(
        _wrap(
          TailscaleSettingsScreen(
            model: model,
            launchExternalUrl: (uri) {
              launched.add(uri);
              final pending = launchCompleter;
              if (pending != null) return pending.future;
              return Future<bool>.value(true);
            },
          ),
        ),
      );

      await model.tailscale.start();

      // 空 authUrl 时仅展示刷新按钮
      backend.emit(
        const TailscaleStatus(enabled: true, state: 'needs_login', authUrl: ''),
      );
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('tailscale-open-login')), findsNothing);
      expect(find.byKey(const Key('tailscale-copy-login')), findsNothing);
      expect(find.byKey(const Key('tailscale-refresh-login')), findsOneWidget);

      // 合法链接：打开与复制
      backend.emit(
        const TailscaleStatus(
          enabled: true,
          state: 'needs_login',
          authUrl: 'https://login.tailscale.com/a/valid',
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('tailscale-open-login')));
      await tester.pumpAndSettle();
      expect(launched, [Uri.parse('https://login.tailscale.com/a/valid')]);

      await tester.tap(find.byKey(const Key('tailscale-copy-login')));
      await tester.pumpAndSettle();
      expect(copiedText, 'https://login.tailscale.com/a/valid');

      // 慢启动器在用户刷新链接后才返回 false，不得覆盖新状态
      launchCompleter = Completer<bool>();
      await tester.tap(find.byKey(const Key('tailscale-open-login')));
      await tester.pump();
      await tester.tap(find.byKey(const Key('tailscale-refresh-login')));
      await tester.pumpAndSettle();
      expect(backend.loginCalls, 1);
      launchCompleter.complete(false);
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('tailscale-error-banner')), findsNothing);
      launchCompleter = null;

      // 恶意链接同时阻止打开与复制
      launched.clear();
      copiedText = null;
      backend.emit(
        const TailscaleStatus(
          enabled: true,
          state: 'needs_login',
          authUrl: 'https://user:pass@evil.example/login',
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('tailscale-open-login')));
      await tester.pumpAndSettle();
      expect(launched, isEmpty);
      await tester.tap(find.byKey(const Key('tailscale-copy-login')));
      await tester.pumpAndSettle();
      expect(copiedText, isNull);
      expect(find.byKey(const Key('tailscale-error-banner')), findsOneWidget);
    },
  );

  testWidgets(
    'advanced settings keep draft across model ticks, clear authKey on failure, show inline feedback, and confirm reset',
    (tester) async {
      final backend = _FakeTsBackend();
      addTearDown(backend.dispose);
      final settingsStore = MemorySettingsStore();
      final model = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: settingsStore,
        tailscaleBackend: backend,
      );

      await tester.pumpWidget(_wrap(TailscaleSettingsScreen(model: model)));

      await tester.tap(find.byKey(const Key('tailscale-advanced-toggle')));
      await tester.pumpAndSettle();

      // 验证 AuthKey 遮挡且关闭 Android IME 个性化学习
      final authField = tester.widget<TextField>(
        find.byKey(const Key('tailscale-auth-key-field')),
      );
      expect(authField.obscureText, isTrue);
      expect(authField.enableIMEPersonalizedLearning, isFalse);

      // 编辑草稿期间收到模型异步通知不得冲掉草稿
      await tester.enterText(
        find.byKey(const Key('tailscale-hostname-field')),
        'draft-phone',
      );
      model.updateSettings((s) => s.theme = 'light');
      await tester.pumpAndSettle();
      expect(
        tester
            .widget<TextField>(
              find.byKey(const Key('tailscale-hostname-field')),
            )
            .controller
            ?.text,
        'draft-phone',
      );

      // 带 query 的 ControlURL 必须与 Go ValidateControlURL 一致被拒绝且不保存
      await tester.enterText(
        find.byKey(const Key('tailscale-control-url-field')),
        'https://headscale.example/?x=1',
      );
      await tester.ensureVisible(
        find.byKey(const Key('tailscale-save-advanced')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('tailscale-save-advanced')));
      await tester.pumpAndSettle();
      expect(backend.startCalls, isEmpty);
      expect(model.settings.tailscaleControlUrl, isEmpty);
      expect(
        find.byKey(const Key('tailscale-advanced-error-banner')),
        findsOneWidget,
      );

      // 原生启动失败时：不持久化修改、就地报错、且清空一次性 AuthKey 避免下次保存误发
      await tester.ensureVisible(
        find.byKey(const Key('tailscale-control-url-field')),
      );
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('tailscale-control-url-field')),
        'https://headscale.example.com',
      );
      await tester.ensureVisible(
        find.byKey(const Key('tailscale-auth-key-field')),
      );
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('tailscale-auth-key-field')),
        'tskey-auth-failed-once',
      );
      backend.emitNativeErrorOnStart = true;
      backend.startError = TailscaleException('state directory unavailable');
      await tester.ensureVisible(
        find.byKey(const Key('tailscale-save-advanced')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('tailscale-save-advanced')));
      await tester.pumpAndSettle();
      expect(model.settings.tailscaleEnabled, isFalse);
      expect(model.settings.tailscaleControlUrl, isEmpty);
      expect(
        tester
            .widget<TextField>(
              find.byKey(const Key('tailscale-auth-key-field')),
            )
            .controller
            ?.text,
        isEmpty,
      );
      expect(
        find.byKey(const Key('tailscale-advanced-error-banner')),
        findsOneWidget,
      );

      // 修复后带新 AuthKey 保存成功：自动开启、清空输入框且绝不落盘 AuthKey
      backend.emitNativeErrorOnStart = false;
      backend.startError = null;
      await tester.ensureVisible(
        find.byKey(const Key('tailscale-hostname-field')),
      );
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('tailscale-hostname-field')),
        'my-phone',
      );
      await tester.ensureVisible(
        find.byKey(const Key('tailscale-auth-key-field')),
      );
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('tailscale-auth-key-field')),
        'tskey-auth-secret-123',
      );
      await tester.ensureVisible(
        find.byKey(const Key('tailscale-disable-bind-switch')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('tailscale-disable-bind-switch')));
      await tester.pumpAndSettle();
      await tester.ensureVisible(
        find.byKey(const Key('tailscale-save-advanced')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('tailscale-save-advanced')));
      await tester.pumpAndSettle();

      final call = backend.startCalls.last;
      expect(call.hostname, 'my-phone');
      expect(call.controlUrl, 'https://headscale.example.com');
      expect(call.authKey, 'tskey-auth-secret-123');
      expect(call.disableBindInterface, isTrue);
      expect(model.settings.tailscaleEnabled, isTrue);
      expect(settingsStore.data ?? '', isNot(contains('tskey-auth-secret')));
      final decoded = AppSettings.decode(settingsStore.data);
      expect(decoded.tailscaleHostname, 'my-phone');
      expect(decoded.tailscaleControlUrl, 'https://headscale.example.com');
      expect(decoded.tailscaleDisableBind, isTrue);
      expect(jsonEncode(decoded.toJson()), isNot(contains('authKey')));

      // 重置节点身份：确认框可取消，确认后远端注销失败在当前 Advanced 视图就地可见（无需拖回顶部）
      backend.nextResetResult = const TailscaleResetResult(
        status: TailscaleStatus(enabled: false, state: 'disabled'),
        loggedOut: false,
        logoutError: 'offline',
        localCleared: true,
      );
      await tester.ensureVisible(
        find.byKey(const Key('tailscale-reset-button')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('tailscale-reset-button')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Cancel'));
      await tester.pumpAndSettle();
      expect(backend.resetCalls, 0);

      await tester.tap(find.byKey(const Key('tailscale-reset-button')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Reset Node Identity').last);
      await tester.pumpAndSettle();
      expect(backend.resetCalls, 1);
      expect(model.settings.tailscaleEnabled, isFalse);
      expect(
        find.byKey(const Key('tailscale-advanced-warning-banner')),
        findsOneWidget,
      );
      expect(find.textContaining('offline'), findsAtLeastNWidgets(1));
    },
  );

  test('cold-start restore does not supersede user-initiated start during registerPush', () async {
    final pushGate = Completer<void>();
    final backend = _FakeTsBackend();
    addTearDown(backend.dispose);
    final settingsStore = MemorySettingsStore()
      ..data = jsonEncode(
        AppSettings(
          phoneId: '0123456789abcdef0123456789abcdef',
          tailscaleEnabled: true,
        ).toJson(),
      );
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: settingsStore,
      push: _SlowPush(pushGate),
      tailscaleBackend: backend,
    );

    final restoreFuture = model.restore();
    await Future<void>.delayed(Duration.zero);
    expect(model.restored, isTrue);

    // 用户在 registerPush 完成前就通过界面发起带 AuthKey 的启动
    final applied = await model.tailscale.start(
      hostname: 'user-phone',
      authKey: 'tskey-auth-user',
    );
    expect(applied, isTrue);
    expect(backend.startCalls.length, 1);

    // 放行 registerPush，冷启动 restore 不应再次发起不带 AuthKey 的 start 覆盖用户操作
    pushGate.complete();
    await restoreFuture;
    expect(backend.startCalls.length, 1);
    expect(backend.startCalls.single.authKey, 'tskey-auth-user');
  });

  testWidgets(
    'running node restores previous config when advanced reconfigure fails',
    (tester) async {
      final reconfigBackend = _ReconfigFailOnceBackend();
      addTearDown(reconfigBackend.dispose);
      final reconfigModel = PosseAppModel(
        store: MemoryPairingStore(),
        settingsStore: MemorySettingsStore(),
        tailscaleBackend: reconfigBackend,
      );
      reconfigModel.updateSettings((s) {
        s.tailscaleEnabled = true;
        s.tailscaleHostname = 'good-phone';
        s.tailscaleControlUrl = 'https://good.example.com';
      });
      await reconfigModel.tailscale.start(
        hostname: 'good-phone',
        controlUrl: 'https://good.example.com',
      );
      expect(reconfigModel.tailscale.isRunning, isTrue);

      await tester.pumpWidget(
        _wrap(TailscaleSettingsScreen(model: reconfigModel)),
      );
      await tester.tap(find.byKey(const Key('tailscale-advanced-toggle')));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('tailscale-control-url-field')),
        'https://broken.example.com',
      );
      reconfigBackend.failNextStart = true;
      await tester.ensureVisible(
        find.byKey(const Key('tailscale-save-advanced')),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('tailscale-save-advanced')));
      await tester.pumpAndSettle();

      // 报错就地显示、持久化配置未被污染，且旧的 running 实例已自动恢复
      expect(
        find.byKey(const Key('tailscale-advanced-error-banner')),
        findsOneWidget,
      );
      expect(
        reconfigModel.settings.tailscaleControlUrl,
        'https://good.example.com',
      );
      expect(reconfigModel.tailscale.isRunning, isTrue);
      expect(
        reconfigBackend.startCalls.last.controlUrl,
        'https://good.example.com',
      );
    },
  );
}

class _ReconfigFailOnceBackend extends _FakeTsBackend {
  bool failNextStart = false;

  @override
  Future<TailscaleStatus> start({
    required String stateDir,
    String hostname = '',
    String controlUrl = '',
    String authKey = '',
    bool disableBindInterface = false,
  }) async {
    if (failNextStart) {
      failNextStart = false;
      startCalls.add((
        stateDir: stateDir,
        hostname: hostname,
        controlUrl: controlUrl,
        authKey: authKey,
        disableBindInterface: disableBindInterface,
      ));
      current = const TailscaleStatus(
        enabled: true,
        state: 'error',
        error: 'control server unreachable',
      );
      emit(current);
      throw TailscaleException('control server unreachable');
    }
    return super.start(
      stateDir: stateDir,
      hostname: hostname,
      controlUrl: controlUrl,
      authKey: authKey,
      disableBindInterface: disableBindInterface,
    );
  }
}
