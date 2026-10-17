import 'package:flutter/material.dart';

import 'app_state.dart';
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
    final light = ColorScheme.fromSeed(seedColor: const Color(0xFFC15F3C));
    final dark = ColorScheme.fromSeed(
      seedColor: const Color(0xFFD97757),
      brightness: Brightness.dark,
    );
    return MaterialApp(
      title: 'Hive',
      theme: ThemeData(
        colorScheme: light,
        scaffoldBackgroundColor: const Color(0xFFF7F5F2),
        useMaterial3: true,
      ),
      darkTheme: ThemeData(
        colorScheme: dark,
        scaffoldBackgroundColor: const Color(0xFF1C1A17),
        useMaterial3: true,
      ),
      home: widget.model.offer == null
          ? PairScreen(model: widget.model)
          : WorkspaceScreen(model: widget.model),
    );
  }
}
