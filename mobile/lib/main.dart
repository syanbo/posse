import 'package:flutter/material.dart';

import 'app_state.dart';
import 'ui/keyboard.dart';
import 'ui/theme.dart';
import 'workspace/workspace.dart';

void main() {
  runApp(HiveApp(model: HiveAppModel()));
}

class HiveApp extends StatefulWidget {
  const HiveApp({super.key, required this.model});

  final HiveAppModel model;

  @override
  State<HiveApp> createState() => _HiveAppState();
}

class _HiveAppState extends State<HiveApp> {
  final _keyboard = KeyboardDismisser();

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_rebuild);
    widget.model.restore();
  }

  @override
  void dispose() {
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
      title: 'Hive',
      debugShowCheckedModeBanner: false,
      navigatorObservers: [_keyboard],
      theme: hiveTheme(Brightness.light),
      darkTheme: hiveTheme(Brightness.dark),
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
