import 'dart:async';

import 'package:flutter/material.dart';

import 'app_state.dart';
import 'background.dart';
import 'l10n/l10n.dart';
import 'ui/keyboard.dart';
import 'ui/splash.dart';
import 'ui/theme.dart';
import 'update/update.dart';
import 'push/push.dart';
import 'workspace/workspace.dart';

/// 点通知打开会话时从这里拿到导航，见 [PosseAppModel.onOpenSession]。
final _navigator = GlobalKey<NavigatorState>();

void main() {
  runApp(PosseApp(model: PosseAppModel()));
}

class PosseApp extends StatefulWidget {
  const PosseApp({super.key, required this.model});

  final PosseAppModel model;

  @override
  State<PosseApp> createState() => _PosseAppState();
}

class _PosseAppState extends State<PosseApp> with WidgetsBindingObserver {
  final _keyboard = KeyboardDismisser();
  // 主题只看深浅，建一次：模型每次通知根部都重建，不用每次都重新算两套
  final _light = posseTheme(Brightness.light);
  final _dark = posseTheme(Brightness.dark);
  bool _splash = true;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    widget.model.addListener(_rebuild);
    widget.model.onOpenSession = (tab) {
      final nav = _navigator.currentState;
      if (nav == null) return;
      nav.popUntil((route) => route.isFirst);
      openSession(nav.context, widget.model, tab);
    };
    widget.model.restore();
    startUpdateChecks();
  }

  var _away = false;

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    // 拉下控制中心、弹权限框只到 inactive，连接没被冻住，不用查
    switch (state) {
      case AppLifecycleState.hidden:
      case AppLifecycleState.paused:
        if (!_away) widget.model.presence(foreground: false);
        _away = true;
      case AppLifecycleState.resumed:
        if (_away) {
          widget.model.presence(foreground: true);
          unawaited(widget.model.wake());
          startUpdateChecks();
          // 可能刚去系统设置里打开了通知
          if (widget.model.pushStatus == PushStatus.denied) {
            unawaited(widget.model.registerPush());
          }
        }
        _away = false;
      case AppLifecycleState.inactive:
      case AppLifecycleState.detached:
        break;
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    widget.model.removeListener(_rebuild);
    widget.model.dispose();
    super.dispose();
  }

  void _rebuild() {
    syncKeepAlive(widget.model.settings.backgroundKeep);
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Posse',
      debugShowCheckedModeBanner: false,
      navigatorKey: _navigator,
      navigatorObservers: [_keyboard],
      locale: chosenLocale(widget.model.settings.language),
      localizationsDelegates: AppLocalizations.localizationsDelegates,
      supportedLocales: AppLocalizations.supportedLocales,
      // 界面语言定下后记下，变了再报给电脑（见 PosseAppModel.localeChanged）。
      // 闪屏盖在首页上面，首页在底下照常建好，淡出后直接露出来。
      // 一直包一层 Stack：闪屏退了换成直接返回 child 的话，Navigator 换了位置会重建
      builder: (context, child) {
        if (setPosseLocale(Localizations.localeOf(context))) {
          WidgetsBinding.instance.addPostFrameCallback(
            (_) => widget.model.localeChanged(),
          );
        }
        return Stack(
          children: [
            child!,
            if (_splash)
              PosseSplash(
                ready: widget.model.restored,
                onDone: () => setState(() => _splash = false),
              ),
          ],
        );
      },
      theme: _light,
      darkTheme: _dark,
      // 默认深色，可在设置里换成浅色或跟随系统
      themeMode: switch (widget.model.settings.theme) {
        'light' => ThemeMode.light,
        'system' => ThemeMode.system,
        _ => ThemeMode.dark,
      },
      // 配对过电脑就留在首页，换电脑、重连时不闪回配对页
      home: widget.model.offer == null && widget.model.computers.isEmpty
          ? PairScreen(model: widget.model)
          : WorkspaceScreen(model: widget.model),
    );
  }
}
