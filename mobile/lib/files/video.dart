import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart';
import 'package:flutter/material.dart';
import 'package:video_player/video_player.dart';

import '../protocol/rpc.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';

const videoExts = {'mp4', 'mov', 'm4v', 'webm'};

/// 太大的视频不往手机上搬：走局域网也要好一会儿，还占手机空间。
const _videoMax = 500 << 20;
const _chunk = 1 << 20;

/// 同时在路上的分段数。电脑那边回复是一条条发的，排在后面的要等前面的发完，
/// 同时要得多只会让后面的白等、更容易超时。
const _inFlight = 2;

/// 一段（线上约 1.8 MB）最多等多久：经跨境隧道连电脑时，晚高峰可能只有几百 KB/s。
const _chunkTimeout = Duration(seconds: 60);

/// 一段超时后再试几次。
const _retries = 2;

/// 把工作区里的视频分段下载到手机的临时目录（[files.readRange]），下好了再播。
/// 按工作区、路径、大小和修改时间缓存，再打开同一个视频不重新下；
/// 没下完（网络断了、超时）留着下了的部分，下次接着下。
/// [onProgress] 报已经下了多少字节和总共多少。
Future<File> fetchVideo(
  PhoneRpc rpc, {
  required String worktree,
  required String path,
  required bool Function() cancelled,
  required void Function(int done, int total) onProgress,
}) async {
  final stat = await rpc.call('files.stat', {
    'worktree': worktree,
    'relativePath': path,
  });
  final size = (stat['size'] as num?)?.toInt() ?? 0;
  if (size > _videoMax) {
    throw const VideoTooLarge();
  }
  final key = sha1
      .convert(utf8.encode('$worktree\n$path\n$size\n${stat['mtime']}'))
      .toString();
  final dot = path.lastIndexOf('.');
  final dir = Directory('${Directory.systemTemp.path}/hive-video');
  await dir.create(recursive: true);
  final file = File('${dir.path}/$key${path.substring(dot)}');
  if (await file.exists() && await file.length() == size) return file;

  // 分段按顺序写进去，.part 的长度就是已经下好的字节数
  final part = File('${file.path}.part');
  var done = await part.exists() ? await part.length() : 0;
  if (done > size) {
    await part.delete();
    done = 0;
  }
  final out = await part.open(
    mode: done > 0 ? FileMode.append : FileMode.write,
  );
  try {
    onProgress(done, size);
    while (done < size) {
      if (cancelled()) throw const VideoCancelled();
      final chunks = await Future.wait([
        for (var i = 0; i < _inFlight; i++)
          if (done + i * _chunk < size)
            _readChunk(rpc, worktree, path, done + i * _chunk, cancelled),
      ]);
      var empty = false;
      for (final res in chunks) {
        final bytes = base64Decode(res['content'] as String? ?? '');
        await out.writeFrom(bytes);
        done += bytes.length;
        empty |= bytes.isEmpty;
      }
      onProgress(done, size);
      // 读的途中文件变短了（被改写），后面读不到东西，别空转
      if (empty) break;
    }
    await out.close();
    await part.rename(file.path);
    return file;
  } catch (_) {
    await out.close();
    rethrow;
  }
}

/// 取一段，超时了再试；电脑报的错（文件没了等）不重试。
Future<Map<String, Object?>> _readChunk(
  PhoneRpc rpc,
  String worktree,
  String path,
  int offset,
  bool Function() cancelled,
) async {
  for (var attempt = 0; ; attempt++) {
    try {
      return await rpc.call('files.readRange', {
        'worktree': worktree,
        'relativePath': path,
        'offset': offset,
        'limit': _chunk,
      }, timeout: _chunkTimeout);
    } on TimeoutException {
      if (attempt >= _retries || cancelled()) rethrow;
    }
  }
}

class VideoTooLarge implements Exception {
  const VideoTooLarge();
}

class VideoCancelled implements Exception {
  const VideoCancelled();
}

/// 文件页里的视频：先下载（带进度），下好了原地播放，下面是进度条和时间，点画面暂停或继续。
class VideoView extends StatefulWidget {
  const VideoView({
    super.key,
    required this.rpc,
    required this.worktree,
    required this.path,
  });

  final PhoneRpc rpc;
  final String worktree;
  final String path;

  @override
  State<VideoView> createState() => _VideoViewState();
}

class _VideoViewState extends State<VideoView> {
  VideoPlayerController? _player;
  (int, int)? _progress;
  String? _error;
  var _resumable = false;
  var _disposed = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final file = await fetchVideo(
        widget.rpc,
        worktree: widget.worktree,
        path: widget.path,
        cancelled: () => _disposed,
        onProgress: (done, total) {
          if (mounted) setState(() => _progress = (done, total));
        },
      );
      if (_disposed) return;
      final player = VideoPlayerController.file(file);
      await player.initialize();
      if (_disposed) {
        await player.dispose();
        return;
      }
      await player.play();
      setState(() => _player = player);
    } on VideoCancelled {
      return;
    } on VideoTooLarge {
      _fail('视频超过 500 MB，手机上不下载');
    } on RpcException catch (e) {
      if (e.code == 'method_not_found') {
        _fail('电脑上的 hive 版本太旧，更新后才能在手机上播放视频');
      } else if (e.code == 'closed') {
        _fail('和电脑的连接断了，下好的部分留着', resumable: true);
      } else {
        _fail('$e');
      }
    } on TimeoutException {
      _fail('网络太慢，视频没下完，下好的部分留着', resumable: true);
    } catch (e) {
      _fail('这个视频手机上放不了：$e');
    }
  }

  void _fail(String text, {bool resumable = false}) {
    if (mounted) {
      setState(() {
        _error = text;
        _resumable = resumable;
      });
    }
  }

  void _resume() {
    setState(() {
      _error = null;
      _resumable = false;
    });
    _load();
  }

  @override
  void dispose() {
    _disposed = true;
    _player?.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    if (_error != null) {
      return EmptyState(
        icon: Icons.videocam_off_outlined,
        text: _error!,
        error: true,
        action: _resumable
            ? TextButton(onPressed: _resume, child: const Text('接着下载'))
            : null,
      );
    }
    final player = _player;
    if (player == null) {
      final (done, total) = _progress ?? (0, 0);
      return Center(
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 48),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              LinearProgressIndicator(
                value: total == 0 ? null : done / total,
                borderRadius: BorderRadius.circular(2),
              ),
              const SizedBox(height: 12),
              Text(
                total == 0
                    ? '正在从电脑取视频…'
                    : '正在从电脑取视频  ${formatBytes(done)} / ${formatBytes(total)}',
                style: TextStyle(fontSize: Fs.meta, color: c.muted),
              ),
            ],
          ),
        ),
      );
    }
    return Column(
      children: [
        Expanded(
          child: GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: () =>
                player.value.isPlaying ? player.pause() : player.play(),
            child: Center(
              child: AspectRatio(
                aspectRatio: player.value.aspectRatio,
                child: Stack(
                  alignment: Alignment.center,
                  children: [
                    VideoPlayer(player),
                    _PlayBadge(player: player),
                  ],
                ),
              ),
            ),
          ),
        ),
        _Controls(player: player),
      ],
    );
  }
}

/// 暂停时画面中间的播放标记。
class _PlayBadge extends StatelessWidget {
  const _PlayBadge({required this.player});

  final VideoPlayerController player;

  @override
  Widget build(BuildContext context) {
    return ValueListenableBuilder(
      valueListenable: player,
      builder: (context, value, _) => AnimatedOpacity(
        opacity: value.isPlaying ? 0 : 1,
        duration: const Duration(milliseconds: 160),
        child: Container(
          width: 56,
          height: 56,
          decoration: const BoxDecoration(
            color: Color(0x8A000000),
            shape: BoxShape.circle,
          ),
          child: const Icon(
            Icons.play_arrow_rounded,
            size: 36,
            color: Colors.white,
          ),
        ),
      ),
    );
  }
}

class _Controls extends StatelessWidget {
  const _Controls({required this.player});

  final VideoPlayerController player;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    const tnum = [FontFeature.tabularFigures()];
    return SafeArea(
      top: false,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(8, 4, 16, 8),
        child: ValueListenableBuilder(
          valueListenable: player,
          builder: (context, value, _) => Row(
            children: [
              IconButton(
                tooltip: value.isPlaying ? '暂停' : '播放',
                onPressed: () =>
                    value.isPlaying ? player.pause() : player.play(),
                icon: Icon(
                  value.isPlaying
                      ? Icons.pause_rounded
                      : Icons.play_arrow_rounded,
                  size: 28,
                ),
              ),
              Expanded(
                child: VideoProgressIndicator(
                  player,
                  allowScrubbing: true,
                  padding: const EdgeInsets.symmetric(vertical: 12),
                  colors: VideoProgressColors(
                    playedColor: c.accent,
                    bufferedColor: c.fill,
                    backgroundColor: c.fill,
                  ),
                ),
              ),
              const SizedBox(width: 12),
              Text(
                '${_clock(value.position)} / ${_clock(value.duration)}',
                style: TextStyle(
                  fontSize: 12,
                  color: c.muted,
                  fontFeatures: tnum,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

String _clock(Duration d) {
  final s = d.inSeconds;
  final mm = (s ~/ 60).toString().padLeft(2, '0');
  final ss = (s % 60).toString().padLeft(2, '0');
  return '$mm:$ss';
}
