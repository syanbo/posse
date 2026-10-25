import 'dart:async';
import 'dart:io';
import 'dart:typed_data';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../files/save.dart';
import '../files/video.dart';
import '../history/models.dart' show historyTime;
import '../protocol/rpc.dart';
import '../ui/loading.dart';
import '../ui/media_pager.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';

/// 能列图片和视频的 agent，同桌面图片面板。
const mediaAgents = {'claude', 'codex', 'grok', 'cursor'};

bool hasMedia(SessionTab tab) =>
    mediaAgents.contains(tab.agent) && (tab.sessionId?.isNotEmpty ?? false);

/// 会话里的一张图片或一个视频（`session.media` 的一项）。
class MediaItem {
  const MediaItem({
    required this.id,
    required this.video,
    required this.label,
    required this.source,
    this.at,
    this.size,
  });

  factory MediaItem.fromJson(Map<String, Object?> j) {
    final at = (j['at'] as num?)?.toInt() ?? 0;
    return MediaItem(
      id: j['id'] as String? ?? '',
      video: j['kind'] == 'video',
      label: j['label'] as String? ?? '',
      source: j['source'] as String? ?? '',
      at: at == 0 ? null : DateTime.fromMillisecondsSinceEpoch(at),
      size: (j['size'] as num?)?.toInt(),
    );
  }

  final String id;
  final bool video;
  final String label;

  /// 读的文件或返回它的工具。
  final String source;
  final DateTime? at;

  /// 磁盘上的文件才知道大小；会话文件里内嵌的图片读第一段时才知道。
  final int? size;
}

class MediaList {
  const MediaList({
    this.made = const [],
    this.madeAll = 0,
    this.pasted = const [],
  });

  factory MediaList.fromJson(Map<String, Object?> j) {
    List<MediaItem> items(Object? v) => [
      for (final e in (v as List?) ?? const [])
        if (e is Map) MediaItem.fromJson(e.cast<String, Object?>()),
    ];
    final made = items(j['made']);
    return MediaList(
      made: made,
      madeAll: (j['madeAll'] as num?)?.toInt() ?? made.length,
      pasted: items(j['pasted']),
    );
  }

  final List<MediaItem> made;
  final int madeAll;
  final List<MediaItem> pasted;

  bool get isEmpty => made.isEmpty && pasted.isEmpty;
}

Map<String, Object?> _sessionArgs(SessionTab tab) => {
  'agent': tab.agent,
  'sessionId': tab.sessionId,
  if (tab.transcriptPath != null) 'transcriptPath': tab.transcriptPath,
};

/// 缩略图按会话和 ID 记在内存里，来回进出这一页不重新要；一张约 30 KB，最多留 [_thumbMax] 张。
final _thumbs = <String, Uint8List>{};
const _thumbMax = 120;

/// 会话的"图片与视频"：agent 生成的在上（截图、生成的图片、录屏），粘贴的在下，都是新的在前。
/// 开着时每 3 秒重列一次（同桌面面板），新截的图自己出现。点开看大图或播放。
class MediaScreen extends StatefulWidget {
  const MediaScreen({super.key, required this.model, required this.tab});

  final HiveAppModel model;
  final SessionTab tab;

  @override
  State<MediaScreen> createState() => _MediaScreenState();
}

class _MediaScreenState extends State<MediaScreen> {
  MediaList? _list;
  String? _error;
  Timer? _timer;
  var _busy = false;

  @override
  void initState() {
    super.initState();
    _refresh();
    _timer = Timer.periodic(const Duration(seconds: 3), (_) => _refresh());
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> _refresh() async {
    final rpc = widget.model.rpc;
    if (_busy || rpc == null) return;
    _busy = true;
    try {
      final res = await rpc.call('session.media', _sessionArgs(widget.tab));
      if (mounted && _list == null) await routeSettled(context);
      if (mounted) {
        setState(() {
          _list = MediaList.fromJson(res);
          _error = null;
        });
      }
    } on RpcException catch (e) {
      if (e.code == 'method_not_found') {
        _timer?.cancel();
        if (mounted) {
          setState(() => _error = '电脑上的 hive 版本太旧，更新后才能在手机上看会话里的图片和视频');
        }
      } else if (mounted && _list == null) {
        setState(() => _error = friendlyError(e));
      }
    } catch (e) {
      // 列过一次之后断线不打扰，下一轮再列
      if (mounted && _list == null) setState(() => _error = friendlyError(e));
    } finally {
      _busy = false;
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(titleSpacing: 0, title: const Text('图片与视频')),
      body: _body(context),
    );
  }

  Widget _body(BuildContext context) {
    final c = HiveColors.of(context);
    final list = _list;
    if (list == null) {
      if (_error != null) {
        return EmptyState(
          icon: Icons.error_outline_rounded,
          text: _error!,
          error: true,
        );
      }
      return const Center(child: HiveLoader());
    }
    if (list.isEmpty) {
      return const EmptyState(
        icon: Icons.photo_library_outlined,
        text: '这个会话还没有图片或视频',
      );
    }
    final now = DateTime.now();
    // 点开后按这个顺序左右翻：先生成的、再粘贴的，和网格一致
    final all = [...list.made, ...list.pasted];
    return CustomScrollView(
      slivers: [
        if (list.made.isNotEmpty) ...[
          _header(c, 'Agent 生成', list.madeAll),
          _grid(all, 0, list.made.length, now),
          if (list.madeAll > list.made.length)
            SliverToBoxAdapter(
              child: Padding(
                padding: const EdgeInsets.fromLTRB(16, 10, 16, 0),
                child: Text(
                  '只列出最近 ${list.made.length} 项',
                  style: TextStyle(fontSize: Fs.caption, color: c.muted),
                ),
              ),
            ),
        ],
        if (list.pasted.isNotEmpty) ...[
          _header(c, '粘贴的', list.pasted.length),
          _grid(all, list.made.length, list.pasted.length, now),
        ],
        const SliverSafeArea(
          top: false,
          sliver: SliverToBoxAdapter(child: SizedBox(height: 24)),
        ),
      ],
    );
  }

  Widget _header(HiveColors c, String title, int n) => SliverToBoxAdapter(
    child: Padding(
      padding: const EdgeInsets.fromLTRB(16, 18, 16, 10),
      child: Row(
        children: [
          Text(
            title,
            style: TextStyle(
              fontSize: Fs.meta,
              fontWeight: FontWeight.w600,
              color: c.text,
            ),
          ),
          const SizedBox(width: 6),
          Text(
            '$n',
            style: TextStyle(
              fontSize: Fs.caption,
              color: c.muted,
              fontFeatures: const [FontFeature.tabularFigures()],
            ),
          ),
        ],
      ),
    ),
  );

  /// [all] 里从 [start] 起的 [count] 项。
  Widget _grid(List<MediaItem> all, int start, int count, DateTime now) =>
      SliverPadding(
        padding: const EdgeInsets.symmetric(horizontal: 12),
        sliver: SliverGrid.builder(
          gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
            crossAxisCount: 2,
            mainAxisSpacing: 12,
            crossAxisSpacing: 10,
            childAspectRatio: 0.72,
          ),
          itemCount: count,
          itemBuilder: (context, i) => _MediaTile(
            key: ValueKey(all[start + i].id),
            model: widget.model,
            tab: widget.tab,
            all: all,
            index: start + i,
            now: now,
          ),
        ),
      );
}

/// 存到哪：图片进相册，相册不收的视频（webm）安卓存下载、iOS 分享。
SaveTo _target(MediaItem item) => saveTarget(item.label, image: !item.video);

/// 一格：缩略图（竖的截图从顶上裁），视频盖播放标记，下面是名字和时间。点开看，长按存到手机。
class _MediaTile extends StatefulWidget {
  const _MediaTile({
    super.key,
    required this.model,
    required this.tab,
    required this.all,
    required this.index,
    required this.now,
  });

  final HiveAppModel model;
  final SessionTab tab;

  /// 这一页的全部项和这一格在其中的位置，点开后从这里左右翻。
  final List<MediaItem> all;
  final int index;
  final DateTime now;

  MediaItem get item => all[index];

  @override
  State<_MediaTile> createState() => _MediaTileState();
}

class _MediaTileState extends State<_MediaTile> {
  Uint8List? _thumb;
  var _failed = false;

  String get _key => '${widget.tab.sessionId}\n${widget.item.id}';

  @override
  void initState() {
    super.initState();
    _thumb = _thumbs.remove(_key);
    if (_thumb != null) {
      _thumbs[_key] = _thumb!;
    } else {
      _load();
    }
  }

  Future<void> _load() async {
    final rpc = widget.model.rpc;
    if (rpc == null) return;
    try {
      final res = await rpc.call('session.mediaThumb', {
        ..._sessionArgs(widget.tab),
        'id': widget.item.id,
      }, timeout: const Duration(seconds: 40));
      final bytes = rpcBytes(res['content']);
      _thumbs[_key] = bytes;
      while (_thumbs.length > _thumbMax) {
        _thumbs.remove(_thumbs.keys.first);
      }
      if (mounted) setState(() => _thumb = bytes);
    } catch (_) {
      if (mounted) setState(() => _failed = true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final item = widget.item;
    final thumb = _thumb;
    final time = historyTime(item.at, widget.now);
    return Semantics(
      button: true,
      label: '${item.video ? '视频' : '图片'} ${item.label}',
      child: GestureDetector(
        behavior: HitTestBehavior.opaque,
        onTap: () => Navigator.of(context).push(
          MaterialPageRoute<void>(
            builder: (_) => MediaViewer(
              model: widget.model,
              tab: widget.tab,
              items: widget.all,
              index: widget.index,
            ),
          ),
        ),
        onLongPress: () {
          final rpc = widget.model.rpc;
          if (rpc == null) return;
          showSaveSheet(
            context,
            to: _target(item),
            onSave: (to) => downloadAndSave(
              context,
              name: item.label,
              to: to,
              fetch: (cancelled, onProgress) => fetchMedia(
                rpc,
                widget.tab,
                item,
                cancelled: cancelled,
                onProgress: onProgress,
              ),
            ),
          );
        },
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Expanded(
              child: ClipRRect(
                borderRadius: BorderRadius.circular(Rd.input),
                child: Container(
                  color: c.fill,
                  width: double.infinity,
                  child: Stack(
                    fit: StackFit.expand,
                    children: [
                      if (thumb != null)
                        Image.memory(
                          thumb,
                          fit: BoxFit.cover,
                          alignment: Alignment.topCenter,
                          gaplessPlayback: true,
                        )
                      else if (_failed)
                        Icon(
                          item.video
                              ? Icons.videocam_off_outlined
                              : Icons.broken_image_outlined,
                          color: c.faint,
                        ),
                      if (item.video) const Center(child: _PlayMark()),
                    ],
                  ),
                ),
              ),
            ),
            const SizedBox(height: 6),
            Row(
              children: [
                Expanded(
                  child: Text(
                    item.label,
                    overflow: TextOverflow.ellipsis,
                    maxLines: 1,
                    style: TextStyle(fontSize: Fs.caption, color: c.muted),
                  ),
                ),
                if (time.isNotEmpty) ...[
                  const SizedBox(width: 6),
                  Text(
                    time,
                    style: TextStyle(
                      fontSize: Fs.caption,
                      color: c.faint,
                      fontFeatures: const [FontFeature.tabularFigures()],
                    ),
                  ),
                ],
              ],
            ),
          ],
        ),
      ),
    );
  }
}

class _PlayMark extends StatelessWidget {
  const _PlayMark();

  @override
  Widget build(BuildContext context) {
    return Container(
      width: 40,
      height: 40,
      decoration: const BoxDecoration(
        color: Color(0x8C000000),
        shape: BoxShape.circle,
      ),
      child: const Icon(
        Icons.play_arrow_rounded,
        size: 26,
        color: Colors.white,
      ),
    );
  }
}

/// 把会话里的一项分段取到手机上（`session.mediaRead`），同一项只取一次。
Future<File> fetchMedia(
  PhoneRpc rpc,
  SessionTab tab,
  MediaItem item, {
  required bool Function() cancelled,
  required void Function(int done, int total) onProgress,
}) async {
  final size = item.size;
  if (item.video && size != null && size > videoMax) {
    throw const VideoTooLarge();
  }
  final dot = item.label.lastIndexOf('.');
  final ext = item.video && dot >= 0 ? item.label.substring(dot) : '';
  return fetchChunked(
    key: 'media\n${tab.agent}\n${tab.sessionId}\n${item.id}',
    ext: ext,
    size: size,
    read: (offset, limit, timeout) => rpc.call('session.mediaRead', {
      ..._sessionArgs(tab),
      'id': item.id,
      'offset': offset,
      'limit': limit,
    }, timeout: timeout),
    cancelled: cancelled,
    onProgress: onProgress,
  );
}

/// 看一项：图片取原图（agent 当时看到的样子），双指放大；视频下好后播放。取到手机上后顶栏可以存到手机。
/// 从 [items] 的第 [index] 项打开，左右滑切到前一项、后一项（[MediaPager]），顶栏跟着换。
class MediaViewer extends StatefulWidget {
  const MediaViewer({
    super.key,
    required this.model,
    required this.tab,
    required this.items,
    required this.index,
  });

  final HiveAppModel model;
  final SessionTab tab;
  final List<MediaItem> items;
  final int index;

  @override
  State<MediaViewer> createState() => _MediaViewerState();
}

class _MediaViewerState extends State<MediaViewer> {
  late var _index = widget.index;

  /// 已经取到手机上的项，按位置记：翻回来时顶栏的存到手机还能用。
  final _files = <int, File>{};

  void _ready(int i, File file) {
    if (mounted) setState(() => _files[i] = file);
  }

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final items = widget.items;
    final item = items[_index];
    final tab = widget.tab;
    final rpc = widget.model.rpc;
    final file = _files[_index];
    final to = _target(item);
    final time = historyTime(item.at, DateTime.now());
    final sub = [
      if (item.source != item.label) item.source,
      if (time.isNotEmpty) time,
      if (items.length > 1) '${_index + 1}/${items.length}',
    ].join(' · ');
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 0,
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              item.label,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontSize: 16,
                fontWeight: FontWeight.w600,
                color: c.text,
              ),
            ),
            if (sub.isNotEmpty)
              Text(
                sub,
                overflow: TextOverflow.ellipsis,
                style: statusLineStyle(c.muted),
              ),
          ],
        ),
        actions: [
          Builder(
            builder: (button) => IconButton(
              tooltip: saveLabel(to),
              icon: Icon(saveIcon(to)),
              onPressed: file == null
                  ? null
                  : () => saveToPhone(button, file, item.label),
            ),
          ),
          const SizedBox(width: 4),
        ],
      ),
      body: rpc == null
          ? const EmptyState(
              icon: Icons.link_off_rounded,
              text: '还没连上电脑',
              error: true,
            )
          : MediaPager(
              count: items.length,
              initialIndex: widget.index,
              onChanged: (i) => setState(() => _index = i),
              builder: (context, i) {
                final item = items[i];
                if (item.video) {
                  return VideoView(
                    key: ValueKey(item.id),
                    onReady: (file) => _ready(i, file),
                    fetch: (cancelled, onProgress) => fetchMedia(
                      rpc,
                      tab,
                      item,
                      cancelled: cancelled,
                      onProgress: onProgress,
                    ),
                  );
                }
                return _ImageView(
                  key: ValueKey(item.id),
                  rpc: rpc,
                  tab: tab,
                  item: item,
                  onReady: (file) => _ready(i, file),
                );
              },
            ),
    );
  }
}

class _ImageView extends StatefulWidget {
  const _ImageView({
    super.key,
    required this.rpc,
    required this.tab,
    required this.item,
    required this.onReady,
  });

  final PhoneRpc rpc;
  final SessionTab tab;
  final MediaItem item;
  final ValueChanged<File> onReady;

  @override
  State<_ImageView> createState() => _ImageViewState();
}

class _ImageViewState extends State<_ImageView> {
  File? _file;
  String? _error;
  var _disposed = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final file = await fetchMedia(
        widget.rpc,
        widget.tab,
        widget.item,
        cancelled: () => _disposed,
        onProgress: (_, _) {},
      );
      if (!mounted) return;
      setState(() => _file = file);
      widget.onReady(file);
    } on VideoCancelled {
      return;
    } catch (e) {
      if (mounted) setState(() => _error = friendlyError(e));
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_error != null) {
      return EmptyState(
        icon: Icons.broken_image_outlined,
        text: _error!,
        error: true,
        action: TextButton(
          onPressed: () {
            setState(() => _error = null);
            _load();
          },
          child: const Text('重试'),
        ),
      );
    }
    final file = _file;
    if (file == null) return const Center(child: HiveLoader());
    return ZoomImage(child: Image.file(file));
  }
}
