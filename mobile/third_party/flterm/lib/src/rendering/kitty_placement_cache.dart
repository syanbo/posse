import 'dart:ui';

import 'package:libghostty/libghostty.dart';
import 'package:meta/meta.dart';

import 'kitty_image_cache.dart';
import 'paint_state.dart';

/// Caches paint-safe snapshots of Kitty graphics placements.
///
/// Libghostty generations invalidate protocol content, while terminal geometry
/// invalidates resolved placement rectangles. Unchanged inputs avoid placement
/// iteration, image lookup, sorting, and image eviction.
final class KittyPlacementCache {
  final TerminalPaintState _state;
  final KittyImageCache _images;
  final List<KittyPlacementSnapshot> _snapshots = [];
  final Set<int> _liveImageIds = {};
  _SnapshotKey? _key;

  KittyPlacementCache({required this._state, required this._images});

  /// Placement snapshots ordered by their signed z-index.
  Iterable<KittyPlacementSnapshot> get snapshots => _snapshots;

  /// Refreshes snapshots from [terminal] when protocol or geometry inputs have
  /// changed.
  ///
  /// [geometryDirty] must be true after terminal mutations that can change
  /// placement render information without changing Kitty storage generation.
  ///
  /// Returns whether callers must rebuild their paint-order buckets.
  bool sync(Terminal terminal, {required bool geometryDirty}) {
    final graphics = KittyGraphics.of(terminal);
    if (graphics == null) {
      final changed = _key != null || _snapshots.isNotEmpty;
      _clear();
      _images.evict(_liveImageIds);
      _key = null;
      return changed;
    }

    final key = _SnapshotKey(
      generation: graphics.generation,
      cellWidth: _state.metrics.cellWidth,
      cellHeight: _state.metrics.cellHeight,
      devicePixelRatio: _state.devicePixelRatio,
      viewportOffset: _state.viewportOffset,
      rows: _state.rows,
      cols: _state.cols,
    );
    if (!geometryDirty && _key == key) return false;

    _clear();
    for (final placement in graphics.placements()) {
      _liveImageIds.add(placement.imageId);

      final info = placement.renderInfo;
      if (!info.viewportVisible) continue;
      if (info.pixelWidth == 0 || info.pixelHeight == 0) continue;

      final image = graphics.image(placement.imageId);
      if (image == null) continue;

      _images.lookup(image);
      _snapshots.add(
        KittyPlacementSnapshot(
          imageId: placement.imageId,
          dst: Rect.fromLTWH(
            info.viewportCol * key.cellWidth +
                placement.xOffset / key.devicePixelRatio,
            info.viewportRow * key.cellHeight +
                placement.yOffset / key.devicePixelRatio,
            info.pixelWidth / key.devicePixelRatio,
            info.pixelHeight / key.devicePixelRatio,
          ),
          src: Rect.fromLTWH(
            info.sourceX.toDouble(),
            info.sourceY.toDouble(),
            info.sourceWidth.toDouble(),
            info.sourceHeight.toDouble(),
          ),
          z: placement.z,
        ),
      );
    }

    if (_snapshots.length > 1) _snapshots.sort(_compareZ);
    _images.evict(_liveImageIds);
    _key = key;
    return true;
  }

  void _clear() {
    _snapshots.clear();
    _liveImageIds.clear();
  }

  static int _compareZ(KittyPlacementSnapshot a, KittyPlacementSnapshot b) {
    return a.z.compareTo(b.z);
  }
}

/// Placement data copied from libghostty for use during paint.
///
/// The snapshot remains valid when subsequent terminal mutations invalidate
/// libghostty's borrowed placement handles.
final class KittyPlacementSnapshot {
  final int imageId;

  /// Destination rectangle in the same logical-pixel space as cells.
  final Rect dst;

  /// Source rectangle in the image's own pixel space.
  final Rect src;

  /// Signed z-index from the Kitty graphics protocol.
  final int z;

  const KittyPlacementSnapshot({
    required this.imageId,
    required this.dst,
    required this.src,
    required this.z,
  });
}

@immutable
final class _SnapshotKey {
  final int generation;
  final double cellWidth;
  final double cellHeight;
  final double devicePixelRatio;
  final int viewportOffset;
  final int rows;
  final int cols;

  const _SnapshotKey({
    required this.generation,
    required this.cellWidth,
    required this.cellHeight,
    required this.devicePixelRatio,
    required this.viewportOffset,
    required this.rows,
    required this.cols,
  });

  @override
  int get hashCode => Object.hash(
    generation,
    cellWidth,
    cellHeight,
    devicePixelRatio,
    viewportOffset,
    rows,
    cols,
  );

  @override
  bool operator ==(Object other) {
    return other is _SnapshotKey &&
        generation == other.generation &&
        cellWidth == other.cellWidth &&
        cellHeight == other.cellHeight &&
        devicePixelRatio == other.devicePixelRatio &&
        viewportOffset == other.viewportOffset &&
        rows == other.rows &&
        cols == other.cols;
  }
}
