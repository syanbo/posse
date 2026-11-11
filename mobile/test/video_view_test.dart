import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:plugin_platform_interface/plugin_platform_interface.dart';
import 'package:posse_mobile/files/video.dart';
import 'package:video_player_platform_interface/video_player_platform_interface.dart';

/// 原生播放器的替身：记下建了、放了哪些；[failInit] 时初始化报错，[playGate] 卡住 play。
class _Player extends VideoPlayerPlatform with MockPlatformInterfaceMixin {
  _Player({this.failInit = false});
  final bool failInit;
  Completer<void>? playGate;
  final created = <int>[];
  final disposed = <int>[];
  var _next = 1;

  @override
  Future<void> init() async {}
  @override
  Future<int?> createWithOptions(VideoCreationOptions options) async {
    final id = _next++;
    created.add(id);
    return id;
  }

  @override
  Future<void> dispose(int playerId) async => disposed.add(playerId);
  @override
  Stream<VideoEvent> videoEventsFor(int playerId) => failInit
      ? Stream.error(PlatformException(code: 'VideoError', message: 'bad file'))
      : Stream.value(
          VideoEvent(
            eventType: VideoEventType.initialized,
            duration: const Duration(seconds: 3),
            size: const Size(64, 36),
          ),
        );
  @override
  Future<void> play(int playerId) async => playGate?.future;
  @override
  Future<void> pause(int playerId) async {}
  @override
  Future<void> setLooping(int playerId, bool looping) async {}
  @override
  Future<void> setVolume(int playerId, double volume) async {}
  @override
  Future<void> setPlaybackSpeed(int playerId, double speed) async {}
  @override
  Future<void> seekTo(int playerId, Duration position) async {}
  @override
  Future<Duration> getPosition(int playerId) async => Duration.zero;
  @override
  Future<void> setMixWithOthers(bool mixWithOthers) async {}
  @override
  Widget buildViewWithOptions(VideoViewOptions options) => const SizedBox();
}

void main() {
  late VideoPlayerPlatform saved;
  late File file;
  setUp(() {
    saved = VideoPlayerPlatform.instance;
    file = File(
      '${Directory.systemTemp.createTempSync('posse-video').path}/a.mp4',
    )..writeAsBytesSync([0]);
  });
  tearDown(() => VideoPlayerPlatform.instance = saved);

  Future<void> open(WidgetTester tester) async {
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: VideoView(fetch: (_, _) async => file)),
      ),
    );
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 50)),
    );
    await tester.pump();
  }

  testWidgets('a player that fails to initialize is released', (tester) async {
    final player = _Player(failInit: true);
    VideoPlayerPlatform.instance = player;
    await open(tester);
    expect(player.created, isNotEmpty);
    expect(player.disposed, player.created);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('closing the page while play is starting releases the player', (
    tester,
  ) async {
    final player = _Player()..playGate = Completer<void>();
    VideoPlayerPlatform.instance = player;
    await open(tester);
    expect(player.created, isNotEmpty);
    await tester.pumpWidget(const SizedBox());
    player.playGate!.complete();
    await tester.runAsync(
      () => Future<void>.delayed(const Duration(milliseconds: 50)),
    );
    await tester.pump();
    expect(player.disposed, player.created);
  });
}
