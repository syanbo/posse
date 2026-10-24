import 'package:flutter_test/flutter_test.dart';
import 'package:hive_mobile/desk/mods.dart';

void main() {
  test('a tap arms the next key, another tap lets go', () {
    final mods = DeskMods();
    expect(mods.mode(DeskMod.command), DeskModMode.off);
    mods.tap(DeskMod.command);
    expect(mods.mode(DeskMod.command), DeskModMode.once);
    expect(mods.any, isTrue);
    mods.tap(DeskMod.command);
    expect(mods.mode(DeskMod.command), DeskModMode.off);
    expect(mods.any, isFalse);
  });

  test('a lock stays through consume, a one-shot does not', () {
    final mods = DeskMods();
    mods.tap(DeskMod.shift);
    mods.lock(DeskMod.command);
    mods.tap(DeskMod.command);
    expect(mods.mode(DeskMod.command), DeskModMode.off);

    mods.lock(DeskMod.command);
    mods.lock(DeskMod.option);
    expect(mods.consume(), {DeskMod.command, DeskMod.shift, DeskMod.option});
    expect(mods.mode(DeskMod.shift), DeskModMode.off);
    expect(mods.mode(DeskMod.command), DeskModMode.locked);
    expect(mods.mode(DeskMod.option), DeskModMode.locked);
    expect(mods.consume(), {DeskMod.command, DeskMod.option});
  });

  test('chords use the command shift option control order', () {
    expect(
      deskChord({DeskMod.control, DeskMod.command, DeskMod.option}, 'C'),
      '⌘⌥⌃C',
    );
    expect(deskChord({DeskMod.shift}, 'Tab'), '⇧Tab');
    expect(deskChord({}, 'Esc'), 'Esc');
  });
}
