import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart';
import 'package:flutter/material.dart';
import 'package:video_player/video_player.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';

const videoExts = {'mp4', 'mov', 'm4v', 'webm'};

/// 太大的文件不往手机上搬：走局域网也要好一会儿，还占手机空间。
const videoMax = 500 << 20;
const _chunk = 1 << 20;

/// 同时在路上的分段数。电脑那边回复是一条条发的，排在后面的要等前面的发完，
/// 同时要得多只会让后面的白等、更容易超时。
const _inFlight = 2;

/// 一段（线上约 1.8 MB）最多等多久：经跨境隧道连电脑时，晚高峰可能只有几百 KB/s。
const _chunkTimeout = Duration(seconds: 60);

/// 一段超时后再试几次。
const _retries = 2;

/// 把工作区里的文件分段下载到手机的临时目录（[files.readRange]），视频下好了再播，也用于存到手机。
/// 按工作区、路径、大小和修改时间缓存，再打开同一个文件不重新下；
/// 没下完（网络断了、超时）留着下了的部分，下次接着下。
/// [onProgress] 报已经下了多少字节和总共多少。
Future<File> fetchFile(
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
  if (size > videoMax) {
    throw const VideoTooLarge();
  }
  return fetchChunked(
    key: '$worktree\n$path\n$size\n${stat['mtime']}',
    ext: _extOf(path),
    size: size,
    read: (offset, limit, timeout) => rpc.call('files.readRange', {
      'worktree': worktree,
      'relativePath': path,
      'offset': offset,
      'limit': limit,
    }, timeout: timeout),
    cancelled: cancelled,
    onProgress: onProgress,
  );
}

/// 读远端文件的一段：回复里 content 是 base64，size 是文件的总大小。
typedef ChunkReader = Future<Map<String, Object?>> Function(
  int offset,
  int limit,
  Duration timeout,
);

/// 正在下的文件，按 [fetchChunked] 的 key：看视频时又点了存到手机，两边不能同时往一个 .part 里写。
final _fetching = <String, Future<void>>{};

/// 文件名的扩展名（带点）；没有扩展名的（Makefile、目录名里才有点的）是空串。
String _extOf(String path) {
  final dot = path.lastIndexOf('.');
  return dot > path.lastIndexOf('/') + 1 ? path.substring(dot) : '';
}

/// 分段下载一个远端文件到手机的临时目录，[key] 相同的只下一次。[size] 不知道时由第一段的回复带回来。
/// 分段按顺序写进 .part，没下完时留着，下次接着下。同一个 key 同时只有一个在下，后来的等前面的停了再接着下。
Future<File> fetchChunked({
  required String key,
  required String ext,
  int? size,
  required ChunkReader read,
  required bool Function() cancelled,
  required void Function(int done, int total) onProgress,
}) async {
  for (var busy = _fetching[key]; busy != null; busy = _fetching[key]) {
    await busy;
  }
  final run = _fetchChunked(
    key: key,
    ext: ext,
    size: size,
    read: read,
    cancelled: cancelled,
    onProgress: onProgress,
  );
  final settled = run.then<void>((_) {}, onError: (_) {});
  _fetching[key] = settled;
  try {
    return await run;
  } finally {
    if (identical(_fetching[key], settled)) _fetching.remove(key);
  }
}

Future<File> _fetchChunked({
  required String key,
  required String ext,
  int? size,
  required ChunkReader read,
  required bool Function() cancelled,
  required void Function(int done, int total) onProgress,
}) async {
  final name = sha1.convert(utf8.encode(key)).toString();
  final dir = Directory('${Directory.systemTemp.path}/posse-video');
  await dir.create(recursive: true);
  final file = File('${dir.path}/$name$ext');
  // 下完才改名，所以有这个文件就是下完了的
  if (await file.exists() && (size == null || await file.length() == size)) {
    return file;
  }

  // 分段按顺序写进去，.part 的长度就是已经下好的字节数
  final part = File('${file.path}.part');
  var done = await part.exists() ? await part.length() : 0;
  if (size != null && done > size) {
    await part.delete();
    done = 0;
  }
  final out = await part.open(
    mode: done > 0 ? FileMode.append : FileMode.write,
  );
  var total = size;
  try {
    onProgress(done, total ?? 0);
    while (total == null || done < total) {
      if (cancelled()) throw const VideoCancelled();
      final known = total;
      final chunks = await Future.wait([
        for (var i = 0; i < (known == null ? 1 : _inFlight); i++)
          if (known == null || done + i * _chunk < known)
            _readChunk(read, done + i * _chunk, cancelled),
      ]);
      var empty = false;
      for (final res in chunks) {
        total ??= (res['size'] as num?)?.toInt() ?? 0;
        final bytes = rpcBytes(res['content']);
        await out.writeFrom(bytes);
        done += bytes.length;
        empty |= bytes.isEmpty;
      }
      onProgress(done, total!);
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
  ChunkReader read,
  int offset,
  bool Function() cancelled,
) async {
  for (var attempt = 0; ; attempt++) {
    try {
      return await read(offset, _chunk, _chunkTimeout);
    } on TimeoutException {
      if (attempt >= _retries || cancelled()) rethrow;
    }
  }
}

/// 把视频取到手机上：[cancelled] 为真时停下，[onProgress] 报进度。
typedef VideoFetcher = Future<File> Function(
  bool Function() cancelled,
  void Function(int done, int total) onProgress,
);

class VideoTooLarge implements Exception {
  const VideoTooLarge();
}

class VideoCancelled implements Exception {
  const VideoCancelled();
}

/// 文件页、图片与视频页里的视频：先下载（带进度），下好了原地播放，下面是进度条和时间，点画面暂停或继续。
class VideoView extends StatefulWidget {
  const VideoView({super.key, required this.fetch, this.onReady});

  /// 工作区里的视频（[fetchFile]）。
  VideoView.file({
    super.key,
    required PhoneRpc rpc,
    required String worktree,
    required String path,
    this.onReady,
  }) : fetch = ((cancelled, onProgress) => fetchFile(
         rpc,
         worktree: worktree,
         path: path,
         cancelled: cancelled,
         onProgress: onProgress,
       ));

  final VideoFetcher fetch;

  /// 下好了，交出手机上的文件（顶栏的存到手机用）。
  final ValueChanged<File>? onReady;

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
      final file = await widget.fetch(() => _disposed, (done, total) {
        if (mounted) setState(() => _progress = (done, total));
      });
      if (_disposed) return;
      widget.onReady?.call(file);
      // 安卓用平台视图：默认的纹理把解码器接到 Flutter 的 Surface 上，有的机型（华为麒麟 9010 的海思解码器）
      // 在这种 Surface 上配不出输出缓冲区（setParameter 报 UnsupportedSetting），关掉 Impeller 也一样；
      // 平台视图给的是普通窗口，同一个解码器能播。iOS 的纹理没有这个问题
      final player = VideoPlayerController.file(
        file,
        viewType: Platform.isAndroid
            ? VideoViewType.platformView
            : VideoViewType.textureView,
      );
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
      _fail((l) => l.filesVideoTooLarge);
    } on RpcException catch (e) {
      if (e.code == 'method_not_found') {
        _fail((l) => l.filesVideoOldPosse);
      } else if (e.code == 'closed') {
        _fail((l) => l.filesDownloadDisconnected, resumable: true);
      } else {
        _fail((l) => friendlyError(e, l));
      }
    } on TimeoutException {
      _fail((l) => l.filesVideoSlow, resumable: true);
    } catch (e) {
      _fail((l) => l.filesVideoCantPlay(friendlyError(e, l)));
    }
  }

  /// 报错文字等确认还挂着再取（要 context 的界面语言）。
  void _fail(
    String Function(AppLocalizations l) text, {
    bool resumable = false,
  }) {
    if (mounted) {
      setState(() {
        _error = text(context.l10n);
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
    final c = PosseColors.of(context);
    if (_error != null) {
      return EmptyState(
        icon: Icons.videocam_off_rounded,
        text: _error!,
        error: true,
        action: _resumable
            ? TextButton(
                onPressed: _resume,
                child: Text(context.l10n.filesResumeDownload),
              )
            : null,
      );
    }
    final player = _player;
    if (player == null) {
      final (done, total) = _progress ?? (0, 0);
      return Center(
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 32),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              LinearProgressIndicator(
                value: total == 0 ? null : done / total,
                borderRadius: BorderRadius.circular(Rd.small),
              ),
              const SizedBox(height: 12),
              Text(
                total == 0
                    ? context.l10n.filesFetchingVideo
                    : context.l10n.filesFetchingVideoProgress(
                        formatBytes(done),
                        formatBytes(total),
                      ),
                style: TextStyle(fontSize: Fs.small, color: c.muted),
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
            color: PosseColors.scrim,
            shape: BoxShape.circle,
          ),
          child: const Icon(
            Icons.play_arrow_rounded,
            size: Ic.hero,
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
    final c = PosseColors.of(context);
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
                tooltip: value.isPlaying
                    ? context.l10n.filesPause
                    : context.l10n.filesPlay,
                onPressed: () =>
                    value.isPlaying ? player.pause() : player.play(),
                icon: Icon(
                  value.isPlaying
                      ? Icons.pause_rounded
                      : Icons.play_arrow_rounded,
                  size: Ic.large,
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
                  fontSize: Fs.small,
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
