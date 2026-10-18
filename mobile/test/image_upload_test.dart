import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/chat/image_pick.dart';
import 'package:hive_mobile/chat/image_upload.dart';
import 'package:hive_mobile/chat/paste_choice.dart';

void main() {
  test('caption sits above each path, and a bare image is just the paths', () {
    expect(
      imageMessage('  看一下  ', ['/tmp/hive-mobile-a.png']),
      '看一下\n/tmp/hive-mobile-a.png',
    );
    expect(
      imageMessage('  ', ['/tmp/hive-mobile-a.png']),
      '/tmp/hive-mobile-a.png',
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

  testWidgets('gallery sheet offers several photos only in chat', (
    tester,
  ) async {
    await tester.pumpWidget(
      const MaterialApp(home: Scaffold(body: SizedBox())),
    );
    final context = tester.element(find.byType(Scaffold));

    final chat = askImageSource(context, clipboard: true);
    await tester.pumpAndSettle();
    expect(find.text('从相册选择，可以多张'), findsOneWidget);
    expect(find.text('从剪贴板粘贴'), findsOneWidget);
    expect(find.text('拍照'), findsOneWidget);
    Navigator.of(context).pop();
    await chat;

    final term = askImageSource(context);
    await tester.pumpAndSettle();
    expect(find.text('从相册选择'), findsOneWidget);
    expect(find.text('从相册选择，可以多张'), findsNothing);
    expect(find.text('从剪贴板粘贴'), findsNothing);
    Navigator.of(context).pop();
    await term;
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
      imagePath({'value': '/tmp/hive-mobile-a.png'}),
      '/tmp/hive-mobile-a.png',
    );
    expect(() => imagePath({}), throwsStateError);
  });
}
