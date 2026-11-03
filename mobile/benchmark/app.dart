// 在手机上跑 e2ee_bench：结果显示在屏幕上，也写进 App 的临时目录（e2ee-bench.txt）和日志。
import 'dart:io';

import 'package:flutter/material.dart';

import 'e2ee_bench.dart';

void main() => runApp(const MaterialApp(home: _Bench()));

class _Bench extends StatefulWidget {
  const _Bench();

  @override
  State<_Bench> createState() => _BenchState();
}

class _BenchState extends State<_Bench> {
  final _lines = <String>['running…'];

  @override
  void initState() {
    super.initState();
    // 先画出第一帧再开始，量的是 UI 线程上的同步耗时，和 App 里收发消息时一样
    Future<void>.delayed(const Duration(seconds: 1), _run);
  }

  void _run() {
    final rows = runE2eeBench();
    final out = [
      '${Platform.operatingSystem} ${Platform.operatingSystemVersion}',
      ...rows.map((r) => '$r'),
      ...speedups(rows),
    ];
    final file = File('${Directory.systemTemp.path}/e2ee-bench.txt');
    file.writeAsStringSync('${out.join('\n')}\n');
    for (final line in out) {
      debugPrint('E2EE-BENCH $line');
    }
    debugPrint('E2EE-BENCH done ${file.path}');
    setState(
      () => _lines
        ..clear()
        ..addAll(out)
        ..add('done'),
    );
  }

  @override
  Widget build(BuildContext context) => Scaffold(
    body: SafeArea(
      child: ListView(
        padding: const EdgeInsets.all(8),
        children: [
          for (final line in _lines)
            Text(
              line,
              style: const TextStyle(fontFamily: 'Menlo', fontSize: 9),
            ),
        ],
      ),
    ),
  );
}
