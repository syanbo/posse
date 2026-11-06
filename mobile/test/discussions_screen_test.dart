import 'dart:async';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:file_selector/file_selector.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/discussions/api.dart';
import 'package:posse_mobile/discussions/room.dart';
import 'package:posse_mobile/discussions/discussions.dart';
import 'package:posse_mobile/discussions/models.dart';
import 'package:posse_mobile/discussions/forms.dart';
import 'package:posse_mobile/workspace/models.dart';
import 'package:posse_mobile/l10n/l10n.dart';
import 'package:posse_mobile/protocol/rpc.dart';
import 'package:posse_mobile/ui/theme.dart';

import 'shot_fonts.dart';

class DiscRpc implements PhoneRpc {
  final calls = <({String method, DiscJson params})>[];
  bool failPost = false;
  bool old = false;
  bool archived = false;
  int seq = 1;
  Future<DiscJson> Function(String, DiscJson)? handler;
  @override
  Future<DiscJson> call(
    String method,
    DiscJson params, {
    Duration? timeout,
  }) async {
    calls.add((method: method, params: Map.of(params)));
    if (handler != null) return handler!(method, params);
    if (old) throw RpcException('method_not_found', '');
    final disc = {
      'key': 'room',
      'title': 'Design review',
      'status': archived ? 'archived' : 'active',
      'rev': seq,
      'msgSeq': seq,
    };
    if (method == 'discussion.list') {
      return {
        'self': 'self',
        'seq': seq,
        'items': [disc],
      };
    }
    if (method == 'discussion.post') {
      if (failPost) {
        failPost = false;
        throw TimeoutException('post');
      }
      seq++;
      return {};
    }
    if (method == 'discussion.show') {
      return {
        'discussion': disc,
        'members': [
          {
            'id': 'mine',
            'name': 'Reviewer',
            'agent': 'claude',
            'owner': 'self',
          },
          {
            'id': 'other',
            'name': 'Other agent',
            'agent': 'codex',
            'owner': 'other',
          },
        ],
        'messages': [
          {
            'id': 'm1',
            'seq': 1,
            'authorName': 'Reviewer',
            'body': '## Review\nWe can reuse the existing discussion service.\n\n- Keep retries idempotent\n- Preserve your reading position',
            'createdAt': '2026-11-06T10:00:00Z',
          },
        ],
        'executions': <DiscJson>[],
        'more': false,
        'moreAfter': false,
      };
    }
    return {};
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => null;
}

class DiscModel extends PosseAppModel {
  DiscModel(this.fake)
    : super(store: MemoryPairingStore(), settingsStore: MemorySettingsStore());
  final DiscRpc fake;
  @override
  PhoneRpc? get rpc => fake;
  @override
  String? get activeId => 'test-computer';
}

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));
  test(
    'upload corrects JPEG returned with a PNG name without changing bytes',
    () async {
      final dir = await Directory.systemTemp.createTemp('discussion-image-');
      addTearDown(() => dir.delete(recursive: true));
      final file = File('${dir.path}/photo.PNG');
      final rpc = DiscRpc();
      final model = DiscModel(rpc);
      addTearDown(model.dispose);
      for (final bytes in [
        [0xff, 0xd8, 0xff, 0xe1],
        [0x89, 0x50, 0x4e, 0x47],
      ]) {
        await file.writeAsBytes(bytes);
        rpc.calls.clear();
        await DiscussionApi(model).upload(XFile(file.path), (_) {});
        expect(
          rpc.calls.first.params['name'],
          bytes.first == 0xff ? 'photo.jpg' : 'photo.PNG',
        );
        final chunk = rpc.calls.singleWhere(
          (c) => c.method == 'discussion.upload.chunk',
        );
        expect(rpcBytes(chunk.params['content']), bytes);
      }
    },
  );
  testWidgets(
    'send retry keeps its request ID and @ only offers usable agents',
    (t) async {
      final rpc = DiscRpc()..failPost = true;
      final model = DiscModel(rpc);
      await t.pumpWidget(
        MaterialApp(
          home: DiscussionRoom(
            api: DiscussionApi(model),
            room: 'room',
            self: 'self',
          ),
        ),
      );
      await t.pumpAndSettle();
      await t.enterText(find.byType(TextField), '@');
      await t.pumpAndSettle();
      expect(find.text('Other agent'), findsNothing);
      await t.tap(find.text('Reviewer').last);
      await t.pumpAndSettle();
      expect(find.text('@Reviewer'), findsOneWidget);
      await t.enterText(find.byType(TextField), 'Please review');
      await t.pump();
      await t.tap(find.byTooltip('发送'));
      await t.pumpAndSettle();
      await t.tap(find.byTooltip('重试'));
      await t.pumpAndSettle();
      final posts = rpc.calls
          .where((c) => c.method == 'discussion.post')
          .toList();
      expect(posts.length, 2);
      expect(posts.first.params['clientId'], posts.last.params['clientId']);
      expect(posts.last.params['target'], 'mine');
      await t.pumpWidget(const SizedBox());
      await t.pumpAndSettle();
      model.dispose();
    },
  );
  for (final more in [false, true]) {
    testWidgets('poll keeps bottom stable with earlier row=$more', (t) async {
      t.view.physicalSize = const Size(360, 780);
      t.view.devicePixelRatio = 1;
      addTearDown(t.view.reset);
      final rpc = DiscRpc();
      var count = 4;
      rpc.handler = (_, params) async => {
        'discussion': {
          'key': 'room',
          'title': 'Review',
          'rev': count,
          'msgSeq': count,
        },
        'messages': [
          for (var i = 1; i <= count; i++)
            {
              'id': 'm$i',
              'seq': i,
              'authorName': 'Reviewer',
              'body': List.filled(i * 3, 'Message $i content').join('\n\n'),
            },
        ],
        'more': more,
      };
      final model = DiscModel(rpc);
      await t.pumpWidget(
        MaterialApp(
          theme: posseTheme(Brightness.dark),
          home: DiscussionRoom(
            api: DiscussionApi(model),
            room: 'room',
            self: 'self',
          ),
        ),
      );
      await t.pumpAndSettle();
      final controller = t.widget<ListView>(find.byType(ListView)).controller!;
      expect(controller.position.extentAfter, 0);
      final initial = controller.offset;
      final offsets = <double>[];
      void record() => offsets.add(controller.offset);
      controller.addListener(record);
      for (var i = 0; i < 3; i++) {
        await t.pump(const Duration(seconds: 2));
        await t.pumpAndSettle();
      }
      expect(
        offsets.where((offset) => (offset - initial).abs() > 0.01),
        isEmpty,
      );
      controller.removeListener(record);
      count++;
      await t.pump(const Duration(seconds: 2));
      await t.pumpAndSettle();
      expect(controller.position.extentAfter, 0);
      expect(controller.offset, greaterThan(initial));
      controller.jumpTo(controller.offset - 200);
      await t.pumpAndSettle();
      final reading = controller.offset;
      count++;
      await t.pump(const Duration(seconds: 2));
      await t.pumpAndSettle();
      expect(controller.offset, reading);
      expect(t.takeException(), isNull);
      await t.pumpWidget(const SizedBox());
      await t.pumpAndSettle();
      model.dispose();
    });
  }
  testWidgets('old desktop gives upgrade message', (t) async {
    final model = DiscModel(DiscRpc()..old = true);
    await t.pumpWidget(MaterialApp(home: DiscussionsScreen(model: model)));
    await t.pumpAndSettle();
    expect(
      find.text(lookupAppLocalizations(const Locale('zh')).errorOldDesktop),
      findsOneWidget,
    );
    await t.pumpWidget(const SizedBox());
    await t.pumpAndSettle();
    model.dispose();
  });
  testWidgets('active composer fits a narrow screen with the keyboard', (
    t,
  ) async {
    t.view.physicalSize = const Size(360, 780);
    t.view.devicePixelRatio = 1;
    t.view.viewInsets = const FakeViewPadding(bottom: 320);
    addTearDown(t.view.reset);
    final model = DiscModel(DiscRpc());
    await t.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.light),
        home: DiscussionRoom(
          api: DiscussionApi(model),
          room: 'room',
          self: 'self',
        ),
      ),
    );
    await t.pumpAndSettle();
    await t.enterText(
      find.byType(TextField),
      'First line\nSecond line\nThird line',
    );
    await t.pumpAndSettle();
    expect(t.takeException(), isNull);
    await t.pumpWidget(const SizedBox());
    await t.pumpAndSettle();
    model.dispose();
  });
  for (final unknown in [false, true]) {
    testWidgets(
      'member form project filter and retry persistence unknown=$unknown',
      (t) async {
        final rpc = DiscRpc();
        var attempts = 0;
        rpc.handler = (method, p) async {
          if (attempts++ == 0) {
            throw RpcException(
              'conflict',
              'host offline',
              unknown ? {'unknown': true, 'clientId': p['clientId']} : null,
            );
          }
          return {};
        };
        final model = DiscModel(rpc)
          ..agents = ['claude']
          ..worktrees = [
            const WorktreeRow(
              id: 'other::w1',
              repo: 'Other',
              name: 'main',
              path: '/other',
              branch: 'main',
              status: '',
              live: 0,
            ),
            const WorktreeRow(
              id: 'project::w2',
              repo: 'Project',
              name: 'main',
              path: '/project',
              branch: 'main',
              status: '',
              live: 0,
            ),
          ];
        await t.pumpWidget(
          MaterialApp(
            home: Builder(
              builder: (context) => Scaffold(
                body: TextButton(
                  onPressed: () => discussionForm(
                    context,
                    DiscussionApi(model),
                    'Add',
                    'member.add',
                    {'discussion': 'room'},
                    {'name': 'Name'},
                    workspace: true,
                    agent: true,
                    project: 'project',
                  ),
                  child: const Text('Open'),
                ),
              ),
            ),
          ),
        );
        await t.tap(find.text('Open'));
        await t.pumpAndSettle();
        expect(find.text('Other / main'), findsNothing);
        expect(find.text('Project / main'), findsOneWidget);
        await t.enterText(find.byType(TextField), 'Reviewer');
        await t.tap(find.text('保存'));
        await t.pumpAndSettle();
        expect(t.widget<TextField>(find.byType(TextField)).enabled, !unknown);
        final id = rpc.calls.single.params['clientId'];
        if (unknown) {
          await t.tap(find.text('取消'));
          await t.pumpAndSettle();
          await t.tap(find.text('Open'));
          await t.pumpAndSettle();
          expect(
            t.widget<TextField>(find.byType(TextField)).controller!.text,
            'Reviewer',
          );
          expect(t.widget<TextField>(find.byType(TextField)).enabled, false);
          await t.tap(find.text('保存'));
        } else {
          await t.enterText(find.byType(TextField), 'Edited');
          await t.tap(find.text('重试'));
        }
        await t.pumpAndSettle();
        expect(rpc.calls.last.params['workspace'], 'project::w2');
        expect(rpc.calls.last.params['clientId'] == id, unknown);
        await t.pumpWidget(const SizedBox());
        await t.pumpAndSettle();
        model.dispose();
      },
    );
  }
  for (final language in ['en', 'zh']) {
    for (final brightness in Brightness.values) {
      testWidgets('room small screen $language $brightness', (t) async {
        if (Platform.environment.containsKey('POSSE_MOBILE_PREVIEW_DIR')) {
          await loadShotFonts(t);
        }
        t.view.physicalSize = const Size(360, 780);
        t.view.devicePixelRatio = 1;
        addTearDown(t.view.reset);
        final model = DiscModel(DiscRpc()..archived = true);
        final boundary = GlobalKey();
        await t.pumpWidget(
          MaterialApp(
            theme: shotTheme(posseTheme(brightness)).copyWith(
              appBarTheme: posseTheme(brightness).appBarTheme.copyWith(
                titleTextStyle: posseTheme(brightness)
                    .appBarTheme
                    .titleTextStyle!
                    .copyWith(fontFamily: 'Roboto'),
              ),
            ),
            locale: Locale(language),
            localizationsDelegates: AppLocalizations.localizationsDelegates,
            supportedLocales: AppLocalizations.supportedLocales,
            home: RepaintBoundary(
              key: boundary,
              child: DiscussionRoom(
                api: DiscussionApi(model),
                room: 'room',
                self: 'self',
              ),
            ),
          ),
        );
        await t.pumpAndSettle();
        expect(t.takeException(), isNull);
        final l = lookupAppLocalizations(Locale(language));
        expect(find.text(l.discReadOnly), findsOneWidget);
        expect(find.byType(TextField), findsNothing);
        final dir = Platform.environment['POSSE_MOBILE_PREVIEW_DIR'];
        if (dir != null) {
          await t.runAsync(() async {
            final image =
                await (boundary.currentContext!.findRenderObject()!
                        as RenderRepaintBoundary)
                    .toImage();
            final bytes = await image.toByteData(
              format: ui.ImageByteFormat.png,
            );
            await Directory(dir).create(recursive: true);
            await File('$dir/discussion-$language-${brightness.name}.png')
                .writeAsBytes(bytes!.buffer.asUint8List());
            image.dispose();
          });
        }
        await t.pumpWidget(const SizedBox());
        await t.pumpAndSettle();
        model.dispose();
      });
    }
  }
}
