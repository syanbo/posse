enum DeskMod { command, shift, option, control }

enum DeskModMode { off, once, locked }

const deskModMark = {
  DeskMod.command: '⌘',
  DeskMod.shift: '⇧',
  DeskMod.option: '⌥',
  DeskMod.control: '⌃',
};

/// 修饰键：点一下只管下一个键，长按锁住，锁住后再点松开。
class DeskMods {
  final _mode = <DeskMod, DeskModMode>{};

  DeskModMode mode(DeskMod mod) => _mode[mod] ?? DeskModMode.off;

  bool get any => _mode.values.any((mode) => mode != DeskModMode.off);

  void tap(DeskMod mod) {
    _mode[mod] = switch (mode(mod)) {
      DeskModMode.off => DeskModMode.once,
      DeskModMode.once || DeskModMode.locked => DeskModMode.off,
    };
  }

  void lock(DeskMod mod) {
    _mode[mod] = DeskModMode.locked;
  }

  /// 当前亮着的修饰键。一次性的用完就灭，锁住的留着。
  Set<DeskMod> consume() {
    final on = {
      for (final mod in DeskMod.values)
        if (mode(mod) != DeskModMode.off) mod,
    };
    for (final mod in DeskMod.values) {
      if (mode(mod) == DeskModMode.once) _mode[mod] = DeskModMode.off;
    }
    return on;
  }
}

/// ⌘⇧⌥⌃ 的固定顺序，后面接键帽上的字。
String deskChord(Set<DeskMod> mods, String key) {
  final marks = [
    for (final mod in DeskMod.values)
      if (mods.contains(mod)) deskModMark[mod]!,
  ];
  return '${marks.join()}$key';
}
