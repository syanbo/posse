import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/files/video.dart';
import 'package:hive_mobile/git/git.dart';
import 'package:hive_mobile/git/status.dart';
import 'package:hive_mobile/protocol/rpc.dart';

/// 只认 files.stat 和 files.readRange，内容来自 [data]。
class _FileRpc implements PhoneRpc {
  _FileRpc(this.data);

  final Uint8List data;
  final calls = <String>[];

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params,
  ) async {
    calls.add(method);
    if (method == 'files.stat') {
      return {'size': data.length, 'mtime': 42};
    }
    final offset = params['offset'] as int;
    final end = (offset + (params['limit'] as int)).clamp(0, data.length);
    return {
      'content': base64Encode(data.sublist(offset.clamp(0, end), end)),
      'size': data.length,
    };
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

// 1×1 的透明 PNG
final _png = base64Decode(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==',
);

void main() {
  test('video downloads in chunks, then comes from the cache', () async {
    final data = Uint8List.fromList(
      List.generate((2.5 * (1 << 20)).toInt(), (i) => i % 251),
    );
    final rpc = _FileRpc(data);
    final path = 'rec-${DateTime.now().microsecondsSinceEpoch}.mp4';
    final seen = <int>[];
    final file = await fetchVideo(
      rpc,
      worktree: 'w',
      path: path,
      cancelled: () => false,
      onProgress: (done, _) => seen.add(done),
    );
    expect(await file.readAsBytes(), data);
    expect(rpc.calls.where((m) => m == 'files.readRange').length, 3);
    expect(seen.last, data.length);

    rpc.calls.clear();
    final again = await fetchVideo(
      rpc,
      worktree: 'w',
      path: path,
      cancelled: () => false,
      onProgress: (_, _) {},
    );
    expect(again.path, file.path);
    expect(rpc.calls, ['files.stat']);
    await file.delete();
    expect(File('${file.path}.part').existsSync(), isFalse);
  });

  testWidgets('image diff shows both sides instead of "binary"', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        home: DiffViewer(
          file: parseGitStatus({
            'files': [
              {'path': 'logo.png', 'binary': true},
            ],
          }).files.single,
          load: () async => parseGitDiff({
            'path': 'logo.png',
            'diff': 'Binary files differ\n',
            'images': {
              'old': null,
              'new': {'size': _png.length, 'content': base64Encode(_png)},
            },
          }),
        ),
      ),
    );
    await tester.pump();
    expect(find.text('改之前'), findsOneWidget);
    expect(find.text('新文件，之前没有'), findsOneWidget);
    expect(find.text('现在'), findsOneWidget);
    expect(find.byType(Image), findsOneWidget);
    expect(find.textContaining('二进制'), findsNothing);
  });

  testWidgets('old desktop without images still says binary', (tester) async {
    await tester.pumpWidget(
      MaterialApp(
        home: DiffViewer(
          file: parseGitStatus({
            'files': [
              {'path': 'a.bin', 'binary': true},
            ],
          }).files.single,
          load: () async => parseGitDiff({'path': 'a.bin', 'diff': ''}),
        ),
      ),
    );
    await tester.pump();
    expect(find.text('二进制文件，手机上看不了差异'), findsOneWidget);
  });
}
