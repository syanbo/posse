import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/files/save.dart';
import 'package:posse_mobile/files/video.dart';
import 'package:posse_mobile/git/git.dart';
import 'package:posse_mobile/git/status.dart';
import 'package:posse_mobile/protocol/rpc.dart';

/// 只认 files.stat 和 files.readRange，内容来自 [data]。
class _FileRpc implements PhoneRpc {
  _FileRpc(this.data);

  final Uint8List data;
  final calls = <String>[];

  /// 这些 offset 的分段每次都超时，用来模拟网络慢到下不完。
  final timeoutAt = <int>{};

  /// 这些 offset 的分段只超时一次，重试就好。
  final timeoutOnce = <int>{};
  final offsets = <int>[];

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    calls.add(method);
    if (method == 'files.stat') {
      return {'size': data.length, 'mtime': 42};
    }
    final offset = params['offset'] as int;
    offsets.add(offset);
    if (timeoutAt.contains(offset) || timeoutOnce.remove(offset)) {
      throw TimeoutException(method);
    }
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
    final file = await fetchFile(
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
    final again = await fetchFile(
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

  test('two fetches of one file take turns instead of both writing', () async {
    final data = Uint8List.fromList(
      List.generate((2.5 * (1 << 20)).toInt(), (i) => i % 251),
    );
    final rpc = _FileRpc(data);
    final path = 'twice-${DateTime.now().microsecondsSinceEpoch}.mp4';
    Future<File> fetch() => fetchFile(
      rpc,
      worktree: 'w',
      path: path,
      cancelled: () => false,
      onProgress: (_, _) {},
    );
    final both = await Future.wait([fetch(), fetch()]);
    expect(both[1].path, both[0].path);
    expect(await both[0].readAsBytes(), data);
    // 后来的等前面的下完，直接用缓存，不再取一遍
    expect(rpc.offsets, [0, 1 << 20, 2 << 20]);
    await both[0].delete();
  });

  test('pasted images get an extension from their bytes', () {
    expect(sniffImageExt(_png), 'png');
    expect(sniffImageExt(Uint8List.fromList([0xff, 0xd8, 0xff, 0xe0])), 'jpg');
    expect(sniffImageExt(Uint8List.fromList(utf8.encode('GIF89a'))), 'gif');
    expect(
      sniffImageExt(
        Uint8List.fromList([
          ...utf8.encode('RIFF'),
          0,
          0,
          0,
          0,
          ...utf8.encode('WEBP'),
        ]),
      ),
      'webp',
    );
    expect(sniffImageExt(Uint8List.fromList(utf8.encode('hello'))), '');
  });

  test('photos and common videos go to the album, the rest is shared', () {
    expect(savesToAlbum('a/shot.PNG'), isTrue);
    expect(savesToAlbum('rec.mov'), isTrue);
    expect(savesToAlbum('rec.webm'), isFalse);
    expect(savesToAlbum('icon.bmp'), isFalse);
    expect(savesToAlbum('notes.md'), isFalse);
    expect(savesToAlbum('粘贴'), isFalse);
    expect(savesToAlbum('粘贴', image: true), isTrue);
  });

  test('a chunk that times out once is retried', () async {
    final data = Uint8List.fromList(
      List.generate((2.5 * (1 << 20)).toInt(), (i) => i % 251),
    );
    final rpc = _FileRpc(data)..timeoutOnce.add(1 << 20);
    final file = await fetchFile(
      rpc,
      worktree: 'w',
      path: 'retry-${DateTime.now().microsecondsSinceEpoch}.mp4',
      cancelled: () => false,
      onProgress: (_, _) {},
    );
    expect(await file.readAsBytes(), data);
    expect(rpc.offsets.where((o) => o == 1 << 20).length, 2);
    await file.delete();
  });

  test('an unfinished download keeps what it got and resumes', () async {
    final data = Uint8List.fromList(
      List.generate((4.5 * (1 << 20)).toInt(), (i) => i % 251),
    );
    final path = 'resume-${DateTime.now().microsecondsSinceEpoch}.mp4';
    // 第二批（2 MB 起）怎么都下不来：前两段留着，报超时
    final slow = _FileRpc(data)..timeoutAt.add(2 << 20);
    await expectLater(
      fetchFile(
        slow,
        worktree: 'w',
        path: path,
        cancelled: () => false,
        onProgress: (_, _) {},
      ),
      throwsA(isA<TimeoutException>()),
    );
    // 网络好了再打开：从 2 MB 接着下，不再取前两段
    final rpc = _FileRpc(data);
    final seen = <int>[];
    final file = await fetchFile(
      rpc,
      worktree: 'w',
      path: path,
      cancelled: () => false,
      onProgress: (done, _) => seen.add(done),
    );
    expect(seen.first, 2 << 20);
    expect(rpc.offsets, [2 << 20, 3 << 20, 4 << 20]);
    expect(await file.readAsBytes(), data);
    await file.delete();
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
