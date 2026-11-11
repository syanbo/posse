import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:image_picker/image_picker.dart' show XFile;
import 'package:posse_mobile/chat/image_pick.dart';
import 'package:posse_mobile/chat/image_upload.dart';
import 'package:posse_mobile/chat/paste_choice.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/app_state.dart';

/// 只认上传用的几条 RPC。
class _UploadRpc implements PhoneRpc {
  final methods = <String>[];
  final calls = <(String, Map<String, Object?>)>[];

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    methods.add(method);
    calls.add((method, params));
    return switch (method) {
      'clipboard.startImageUpload' => {'uploadId': 'u1'},
      'clipboard.commitImageUpload' ||
      'clipboard.saveImageAsTempFile' => {'value': '/tmp/posse-mobile-1.png'},
      _ => {},
    };
  }

  @override
  Future<T> upload<T>(Future<T> Function() body) => body();

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

/// 新电脑：开始上传时回编号，分段走二进制帧。
class _BulkRpc extends _UploadRpc {
  final chunks = <List<int>>[];
  Map<String, Object?>? startParams;

  @override
  Future<Map<String, Object?>> call(
    String method,
    Map<String, Object?> params, {
    Duration? timeout,
  }) async {
    if (method == 'clipboard.startImageUpload') {
      methods.add(method);
      startParams = params;
      return {'uploadId': 'u1', 'blob': 4};
    }
    return super.call(method, params, timeout: timeout);
  }

  @override
  Future<void> sendUploadChunk(int blob, List<int> payload) async {
    expect(blob, 4);
    chunks.add(payload);
  }
}

void main() {
  // 视频、文件：带上文件名，从文件流式读、按 64 KB 一块发
  test('a file streams in whole chunks with its name', () async {
    final rpc = _BulkRpc();
    final seen = <double>[];
    final bytes = Uint8List.fromList(
      List.generate(bulkChunk * 3 + 11, (i) => i % 251),
    );
    final file = XFile.fromData(
      bytes,
      path: '/picked/录屏 1.mov',
      length: bytes.length,
    );
    final path = await uploadFile(rpc, file, onProgress: seen.add);
    expect(path, '/tmp/posse-mobile-1.png');
    expect(rpc.startParams?['name'], '录屏 1.mov');
    expect(rpc.startParams?['expectedBytes'], bytes.length);
    expect(
      rpc.startParams?['expectedBase64Length'],
      base64.encode(bytes).length,
    );
    expect(rpc.chunks.map((c) => c.length), [
      bulkChunk,
      bulkChunk,
      bulkChunk,
      11,
    ]);
    expect(rpc.chunks.expand((c) => c).toList(), bytes);
    expect(seen.last, 1);
  });

  test('an old computer cannot take a big file', () async {
    final rpc = _UploadRpc();
    final big = XFile.fromData(
      Uint8List(0),
      path: '/picked/big.mp4',
      length: 40 << 20,
    );
    await expectLater(uploadFile(rpc, big), throwsA(isA<PhoneError>()));
    expect(rpc.methods, contains('clipboard.abortImageUpload'));
  });

  test('a new computer takes raw chunks as binary frames', () async {
    final rpc = _BulkRpc();
    final seen = <double>[];
    final bytes = Uint8List.fromList(
      List.generate(bulkChunk * 4 + 7, (i) => i % 256),
    );
    final path = await uploadImage(rpc, bytes, onProgress: seen.add);
    expect(path, '/tmp/posse-mobile-1.png');
    expect(rpc.startParams?['expectedBytes'], bytes.length);
    expect(rpc.methods, contains('clipboard.commitImageUpload'));
    expect(rpc.methods, isNot(contains('clipboard.appendImageUploadChunk')));
    expect(rpc.chunks.length, 5);
    expect(rpc.chunks.expand((c) => c).toList(), bytes);
    expect(seen.last, 1);
    expect(seen[seen.length - 2], lessThan(1));
  });

  test(
    'the base64 length picks one shot or chunks, exactly as encoded',
    () async {
      // 正好 imageChunkChars 个 base64 字符的还一次传完，多一个字节就分段
      final edge = imageChunkChars * 3 ~/ 4;
      final once = _UploadRpc();
      final small = Uint8List.fromList(List.generate(edge, (i) => i % 251));
      await uploadImage(once, small);
      expect(once.methods, ['clipboard.saveImageAsTempFile']);
      expect(once.calls.single.$2['contentBase64'], base64.encode(small));

      // 旧电脑按报的长度收 base64 分段，三种余数都要和实际编码一样长
      for (final n in [edge + 1, edge + 2, edge + 3]) {
        final rpc = _UploadRpc();
        final bytes = Uint8List.fromList(List.generate(n, (i) => i % 251));
        await uploadImage(rpc, bytes);
        final encoded = base64.encode(bytes);
        final start = rpc.calls.firstWhere(
          (c) => c.$1 == 'clipboard.startImageUpload',
        );
        expect(start.$2['expectedBase64Length'], encoded.length);
        final sent = rpc.calls
            .where((c) => c.$1 == 'clipboard.appendImageUploadChunk')
            .map((c) => c.$2['contentBase64'] as String)
            .join();
        expect(sent, encoded);
      }
    },
  );

  test(
    'a large image reports progress chunk by chunk and ends at 100%',
    () async {
      final rpc = _UploadRpc();
      final seen = <double>[];
      // 三块多一点
      final bytes = Uint8List(imageChunkChars * 3 * 3 ~/ 4 + 100);
      final path = await uploadImage(rpc, bytes, onProgress: seen.add);
      expect(path, '/tmp/posse-mobile-1.png');
      expect(
        rpc.methods
            .where((m) => m == 'clipboard.appendImageUploadChunk')
            .length,
        4,
      );
      expect(seen.length, 5);
      for (var i = 1; i < seen.length; i++) {
        expect(seen[i], greaterThanOrEqualTo(seen[i - 1]));
      }
      expect(seen[3], lessThan(1));
      expect(seen.last, 1);

      final small = <double>[];
      await uploadImage(_UploadRpc(), Uint8List(10), onProgress: small.add);
      expect(small, [1]);
    },
  );

  test('caption sits above each path, and a bare image is just the paths', () {
    expect(
      imageMessage('  看一下  ', ['/tmp/posse-mobile-a.png']),
      '看一下\n/tmp/posse-mobile-a.png',
    );
    expect(
      imageMessage('  ', ['/tmp/posse-mobile-a.png']),
      '/tmp/posse-mobile-a.png',
    );
    expect(imageMessage('看一下', ['/a.png', '/b.png']), '看一下\n/a.png\n/b.png');
    expect(imageMessage('', ['/a.png', '', '/b.png']), '/a.png\n/b.png');
    expect(imageMessage('只要字', const []), '只要字');
  });

  test('paste keeps text ahead of an image and ignores anything else', () {
    final image = Uint8List.fromList([1, 2, 3]);
    final both = choosePaste('路径', image);
    expect(both.isText, isTrue);
    expect(both.isImage, isFalse);
    expect(both.text, '路径');

    final blank = choosePaste('  ', null);
    expect(blank.isText, isTrue);

    final picture = choosePaste('', image);
    expect(picture.isImage, isTrue);
    expect(picture.image, image);

    final empty = choosePaste(null, Uint8List(0));
    expect(empty.isText, isFalse);
    expect(empty.isImage, isFalse);
  });

  testWidgets('the chat sheet offers media, camera, clipboard and files; '
      'the key bar sheet media, camera and files', (tester) async {
    await tester.pumpWidget(
      const MaterialApp(home: Scaffold(body: SizedBox())),
    );
    final context = tester.element(find.byType(Scaffold));

    final chat = askImageSource(context);
    await tester.pumpAndSettle();
    expect(find.text('从相册选择照片或视频，可以多选'), findsOneWidget);
    expect(find.text('从剪贴板粘贴'), findsOneWidget);
    expect(find.text('拍照'), findsOneWidget);
    expect(find.text('从「文件」选择'), findsOneWidget);
    Navigator.of(context).pop();
    await chat;

    final term = askFileSource(context);
    await tester.pumpAndSettle();
    expect(find.text('从相册选择照片或视频'), findsOneWidget);
    expect(find.text('拍照'), findsOneWidget);
    expect(find.text('从「文件」选择'), findsOneWidget);
    expect(find.text('从剪贴板粘贴'), findsNothing);
    await tester.tap(find.byKey(const Key('file-ask-files')));
    await tester.pumpAndSettle();
    expect(await term, FileAsk.files);
  });

  test('only pictures wait for preview; paths are escaped like a drag into Terminal', () {
    expect(isImageFile('IMG_0001.HEIC'), isTrue);
    expect(isImageFile('a.jpeg'), isTrue);
    expect(isImageFile('录屏 1.mov'), isFalse);
    expect(isImageFile('report.pdf'), isFalse);
    expect(isImageFile('noext'), isFalse);
    expect(
      shellPath('/var/folders/x/posse-mobile-1/录屏 1 (2).mov'),
      r'/var/folders/x/posse-mobile-1/录屏\ 1\ \(2\).mov',
    );
    expect(shellPath('/tmp/posse-mobile-9.jpg'), '/tmp/posse-mobile-9.jpg');
    expect(shellPath("/tmp/it's&\$x"), r"/tmp/it\'s\&\$x");
  });

  test('chunks cover the base64 text without overlap', () {
    final encoded = base64.encode(List<int>.generate(20, (i) => i));
    final chunks = imageChunks(encoded);
    expect(chunks, hasLength(1));
    expect(chunks.single.$1, 0);
    expect(chunks.single.$2, encoded);

    final big = 'A' * (imageChunkChars + 10);
    final parts = imageChunks(big);
    expect(parts.map((p) => p.$1), [0, imageChunkChars]);
    expect(parts.map((p) => p.$2.length), [imageChunkChars, 10]);
    expect(parts.map((p) => p.$2).join(), big);
  });

  test('the saved path comes back in value', () {
    expect(
      imagePath({'value': '/tmp/posse-mobile-a.png'}),
      '/tmp/posse-mobile-a.png',
    );
    expect(() => imagePath({}), throwsA(isA<PhoneError>()));
  });

  test('uploadName 去掉 image_picker 加在相册视频原名前的前缀', () {
    expect(
      uploadName(
        'image_picker_70EECD0F-FA4D-4B15-A7E2-10D8CD57F919-25622-000008DA710DD69B'
        'ScreenRecording_10-25-2026 21-00-12_1.mp4',
      ),
      'ScreenRecording_10-25-2026 21-00-12_1.mp4',
    );
    // 照片没有原名，去掉就只剩扩展名
    const photo =
        'image_picker_70EECD0F-FA4D-4B15-A7E2-10D8CD57F919-25622-000008DA710DD69B.jpg';
    expect(uploadName(photo), photo);
    expect(uploadName('报告.pdf'), '报告.pdf');
  });

  // 文件片段大小不一（奇数、比一块大、正好一块、单字节）：发出的每一块和原来按 List<int> 攒的写法逐字节一样，
  // 先发的块在后面接着攒时也不会被改动。
  test('file chunks match the old chunking for any piece sizes', () async {
    final bytes = Uint8List.fromList(
      List.generate(bulkChunk * 5 + 12345, (i) => (i * 7 + i ~/ 300) % 256),
    );
    for (final sizes in [
      [1000],
      [bulkChunk],
      [bulkChunk * 2 + 3],
      [1, 70000, 3, bulkChunk - 1],
    ]) {
      // 原来的写法
      final want = <List<int>>[];
      var buf = <int>[];
      await for (final piece in _PiecedFile(bytes, sizes).openRead()) {
        var off = 0;
        while (off < piece.length) {
          final take = (bulkChunk - buf.length).clamp(0, piece.length - off);
          buf.addAll(piece.sublist(off, off + take));
          off += take;
          if (buf.length == bulkChunk) {
            want.add(buf);
            buf = <int>[];
          }
        }
      }
      if (buf.isNotEmpty) want.add(buf);

      final rpc = _BulkRpc();
      await uploadFile(rpc, _PiecedFile(bytes, sizes));
      expect(rpc.chunks.length, want.length, reason: '$sizes');
      for (var i = 0; i < want.length; i++) {
        expect(rpc.chunks[i], want[i], reason: '$sizes chunk $i');
      }
    }
  });
}

/// 读文件时按 [sizes] 轮流切成片段交出来（openRead 交来的片段大小不固定）。
class _PiecedFile extends XFile {
  _PiecedFile(this.data, this.sizes) : super.fromData(data, name: 'clip.mov');
  final Uint8List data;
  final List<int> sizes;

  @override
  Future<int> length() async => data.length;

  @override
  Stream<Uint8List> openRead([int? start, int? end]) async* {
    var off = 0;
    for (var i = 0; off < data.length; i++) {
      final n = sizes[i % sizes.length].clamp(1, data.length - off);
      yield Uint8List.sublistView(data, off, off + n);
      off += n;
    }
  }
}
