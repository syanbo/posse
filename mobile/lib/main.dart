import 'dart:async';

import 'package:flutter/material.dart';

import 'app_state.dart';
import 'ui/keyboard.dart';
import 'ui/splash.dart';
import 'ui/theme.dart';
import 'update/update.dart';
import 'workspace/workspace.dart';

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
  bool _splash = true;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    widget.model.addListener(_rebuild);
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
        _away = true;
      case AppLifecycleState.resumed:
        if (_away) {
          unawaited(widget.model.wake());
          startUpdateChecks();
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
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Posse',
      debugShowCheckedModeBanner: false,
      navigatorObservers: [_keyboard],
      // 闪屏盖在首页上面，首页在底下照常建好，淡出后直接露出来。
      // 一直包一层 Stack：闪屏退了换成直接返回 child 的话，Navigator 换了位置会重建
      builder: (context, child) => Stack(
        children: [
          child!,
          if (_splash)
            PosseSplash(
              ready: widget.model.restored,
              onDone: () => setState(() => _splash = false),
            ),
        ],
      ),
      theme: posseTheme(Brightness.light),
      darkTheme: posseTheme(Brightness.dark),
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
