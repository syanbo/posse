import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/settings/custom_keys.dart';
import 'package:posse_mobile/settings/custom_keys_screen.dart';
import 'package:posse_mobile/terminal/terminal.dart';
import 'package:posse_mobile/ui/theme.dart';

import 'shot_fonts.dart';

void main() {
  for (final brightness in Brightness.values) {
    testWidgets(
      'terminal drag order syncs with settings and persists ($brightness)',
      (tester) async {
        final shots = Platform.environment['POSSE_SHOTS_DIR'] ?? '';
        if (shots.isNotEmpty) await loadShotFonts(tester);
        await tester.binding.setSurfaceSize(const Size(390, 844));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final store = MemorySettingsStore();
        final model = PosseAppModel(
          store: MemoryPairingStore(),
          settingsStore: store,
        );
        model.settings.barKeys = defaultBarKeys([
          const CustomKey(kind: 'ctrl', label: '', value: 'r'),
        ]);
        final sent = <List<int>>[];
        final actions = <String>[];
        final boundaryKey = GlobalKey();
        final nav = GlobalKey<NavigatorState>();

        Future<void> showBar(PosseAppModel current) async {
          await tester.pumpWidget(
            MaterialApp(
              debugShowCheckedModeBanner: false,
              navigatorKey: nav,
              theme: posseTheme(brightness),
              home: Scaffold(
                bottomNavigationBar: RepaintBoundary(
                  key: boundaryKey,
                  child: TerminalKeyBar(
                    model: current,
                    onKey: (bytes) async => sent.add(bytes),
                    onCustom: (key) => sent.add(key.bytes),
                    onExit: () => actions.add('exit'),
                    onClear: () => actions.add('clear'),
                    onImage: () => actions.add('image'),
                    onPaste: () => actions.add('paste'),
                    keyboardUp: false,
                    onKeyboard: () => actions.add('keyboard'),
                  ),
                ),
              ),
            ),
          );
          await tester.pumpAndSettle();
        }

        Future<void> shot(String phase) async {
          if (shots.isEmpty) return;
          await tester.runAsync(() async {
            final boundary =
                boundaryKey.currentContext!.findRenderObject()!
                    as RenderRepaintBoundary;
            final image = await boundary.toImage(pixelRatio: 3);
            final bytes = await image.toByteData(
              format: ui.ImageByteFormat.png,
            );
            File('$shots/terminal-keys-$phase-${brightness.name}.png')
                .writeAsBytesSync(bytes!.buffer.asUint8List());
          });
        }

        Future<void> drag(Offset from, Offset to, {bool hold = true}) async {
          final gesture = await tester.startGesture(from);
          await tester.pump(Duration(milliseconds: hold ? 600 : 100));
          for (var i = 1; i <= 10; i++) {
            await gesture.moveTo(Offset.lerp(from, to, i / 10)!);
            await tester.pump(const Duration(milliseconds: 16));
          }
          await tester.pump(const Duration(milliseconds: 300));
          await gesture.up();
          await tester.pumpAndSettle();
        }

        List<Object?> order() => [
          for (final key in model.settings.barKeys) key.toJson(),
        ];
        final bar = find.byType(ReorderableListView);
        Finder barIcon(IconData icon) =>
            find.descendant(of: bar, matching: find.byIcon(icon));
        final keyboard = barIcon(Icons.keyboard_rounded);
        final image = barIcon(Icons.attach_file_rounded);
        final paste = barIcon(Icons.content_paste_rounded);

        await showBar(model);
        final barElement = tester.element(find.byType(TerminalKeyBar));
        final keyboardAt = tester.getCenter(keyboard);
        await shot('before');
        await drag(
          tester.getCenter(find.text('Esc')),
          tester.getCenter(image) - const Offset(15, 0),
        );
        expect(model.settings.barKeys.first.builtin, 'Esc');
        expect(
          tester.getCenter(find.text('Esc')).dx,
          lessThan(tester.getCenter(image).dx),
        );
        expect(tester.getCenter(keyboard), keyboardAt);
        expect(sent, isEmpty);
        expect(actions, isEmpty);
        expect(
          AppSettings.decode(await store.read()).barKeys
              .map((key) => key.toJson())
              .toList(),
          order(),
        );
        await shot('after');
        final beforeHold = order();
        await tester.longPress(find.text('Esc'));
        await tester.pumpAndSettle();
        expect(order(), beforeHold);
        expect(sent, isEmpty);

        nav.currentState!.push(
          MaterialPageRoute<void>(
            builder: (_) => CustomKeysScreen(model: model),
          ),
        );
        await tester.pumpAndSettle();
        expect(
          tester.getCenter(find.text('Esc：中断、退出')).dy,
          lessThan(tester.getCenter(find.text('发送文件（照片、视频、文件）')).dy),
        );
        final handles = find.byIcon(Icons.drag_handle_rounded);
        // 设置里把粘贴放到最前，返回的工具条实例未被替换。
        await drag(
          tester.getCenter(handles.at(2)),
          tester.getCenter(handles.at(0)) - const Offset(0, 30),
          hold: false,
        );
        expect(model.settings.barKeys.first.builtin, 'paste');
        nav.currentState!.pop();
        await tester.pumpAndSettle();
        expect(tester.element(find.byType(TerminalKeyBar)), same(barElement));
        expect(
          tester.getCenter(paste).dx,
          lessThan(tester.getCenter(find.text('Esc')).dx),
        );

        // 快速横滑只滚动；键盘开关没有拖动监听器。
        final beforeScroll = order();
        await tester.drag(bar, const Offset(-180, 0));
        await tester.pumpAndSettle();
        expect(order(), beforeScroll);
        expect(sent, isEmpty);
        expect(actions, isEmpty);
        final scroll = tester.state<ScrollableState>(
          find.descendant(of: bar, matching: find.byType(Scrollable)),
        );
        expect(scroll.position.pixels, greaterThan(0));
        scroll.position.jumpTo(0);
        await tester.pumpAndSettle();
        expect(
          find.ancestor(
            of: keyboard,
            matching: find.byType(ReorderableDelayedDragStartListener),
          ),
          findsNothing,
        );

        // 自定义 Ctrl 组合键也能拖，只有轻点才发控制字符。
        await drag(
          tester.getCenter(find.text('^R')),
          tester.getCenter(paste) - const Offset(15, 0),
        );
        expect(model.settings.barKeys.first.custom?.shown, '^R');
        expect(sent, isEmpty);
        expect(actions, isEmpty);
        await tester.tap(find.text('^R'));
        await tester.pumpAndSettle();
        expect(sent, [
          [0x12],
        ]);
        final saved = await store.read();
        expect(
          AppSettings.decode(saved).barKeys.map((key) => key.toJson()).toList(),
          order(),
        );
        await tester.pumpWidget(const SizedBox());
        await tester.pumpAndSettle();
        final reopened = PosseAppModel(
          store: MemoryPairingStore(),
          settingsStore: store,
        )..settings = AppSettings.decode(saved);
        await showBar(reopened);
        expect(
          tester.getCenter(find.text('^R')).dx,
          lessThan(tester.getCenter(paste).dx),
        );
        expect(
          tester.getCenter(keyboard).dx,
          lessThan(tester.getCenter(find.text('^R')).dx),
        );
        expect(tester.takeException(), isNull);
        await shot('reopened');
      },
    );
  }

  testWidgets('built-in keys are listed for sorting and the order resets', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1170, 4000);
    addTearDown(tester.view.reset);
    final model = PosseAppModel(
      store: MemoryPairingStore(),
      settingsStore: MemorySettingsStore(),
    );
    await tester.pumpWidget(
      MaterialApp(
        theme: posseTheme(Brightness.dark),
        home: CustomKeysScreen(model: model),
      ),
    );

    // 内置键都在表里，默认顺序时没有"恢复默认顺序"
    expect(find.text('Esc：中断、退出'), findsOneWidget);
    expect(find.text('发送文件（照片、视频、文件）'), findsOneWidget);
    expect(find.text('恢复默认顺序'), findsNothing);
    // 内置键点了不打开编辑面板
    await tester.tap(find.text('Esc：中断、退出'));
    await tester.pumpAndSettle();
    expect(find.text('编辑快捷键'), findsNothing);

    // 把 Esc 拖到最上面
    final handles = find.byIcon(Icons.drag_handle_rounded);
    final esc = tester.getCenter(handles.at(2));
    final top = tester.getCenter(handles.at(0));
    final drag = await tester.startGesture(esc);
    await tester.pump(const Duration(milliseconds: 100));
    // 一步拖太远认不出拖动，分几步挪上去
    for (var i = 1; i <= 10; i++) {
      await drag.moveTo(Offset.lerp(esc, top - const Offset(0, 30), i / 10)!);
      await tester.pump(const Duration(milliseconds: 16));
    }
    await tester.pump(const Duration(milliseconds: 300));
    await drag.up();
    await tester.pumpAndSettle();
    expect(model.settings.barKeys.first.builtin, 'Esc');

    await tester.tap(find.text('恢复默认顺序'));
    await tester.pumpAndSettle();
    expect([for (final k in model.settings.barKeys) k.builtin], builtinBarKeys);
    expect(find.text('恢复默认顺序'), findsNothing);
  });
}
