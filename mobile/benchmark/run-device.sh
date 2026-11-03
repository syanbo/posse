#!/bin/bash
# 在真机上跑 e2ee_bench（profile 模式，AOT），打印结果。
#   benchmark/run-device.sh ios <设备 UDID>       # xcrun devicectl list devices
#   benchmark/run-device.sh android <adb 序列号>   # adb devices
# 装的是临时目录里另建的小工程（包 ID com.neolabsapp.possebench），不覆盖手机上的 Posse，
# 也就不碰它的配对和设置；跑完把这个测试 App 删掉。
set -euo pipefail

platform=${1:?ios or android}
device=${2:?device id}
here=$(cd "$(dirname "$0")" && pwd)
mobile=$(dirname "$here")
work=${TMPDIR:-/tmp}/posse-e2ee-bench-app
id=com.neolabsapp.possebench

if [ ! -d "$work" ]; then
	fvm flutter create --project-name possebench --org com.neolabsapp --platforms ios,android "$work" >/dev/null
fi
(cd "$work" && fvm use "$(sed -n 's/.*"flutter": "\(.*\)".*/\1/p' "$mobile/.fvmrc")" --force >/dev/null 2>&1)
cat >"$work/pubspec.yaml" <<EOF
name: possebench
publish_to: none
environment:
  sdk: ^3.13.5
dependencies:
  flutter:
    sdk: flutter
  crypto: any
  pinenacl: any
  posse_quic:
    path: $mobile/packages/posse_quic
EOF
# 版本跟 Posse 的锁文件一致
cp "$mobile/pubspec.lock" "$work/pubspec.lock"
cat >"$work/lib/main.dart" <<EOF
import 'file://$here/app.dart' as bench;

void main() => bench.main();
EOF
rm -rf "$work/test"
sed -i '' "s/PRODUCT_BUNDLE_IDENTIFIER = com.neolabsapp.possebench;/PRODUCT_BUNDLE_IDENTIFIER = $id; DEVELOPMENT_TEAM = QKSM9Z4U85;/" \
	"$work/ios/Runner.xcodeproj/project.pbxproj"
cd "$work"
fvm flutter pub get >/dev/null 2>&1

case $platform in
ios)
	fvm flutter build ios --profile
	xcrun devicectl device install app --device "$device" build/ios/iphoneos/Runner.app >/dev/null
	xcrun devicectl device process launch --device "$device" --terminate-existing "$id" >/dev/null
	out=$(mktemp -d)
	# 1 MiB 的 TweetNaCl 在手机上一次要几十毫秒，整轮要一两分钟
	for _ in $(seq 120); do
		sleep 5
		if xcrun devicectl device copy from --device "$device" --domain-type appDataContainer \
			--domain-identifier "$id" --source tmp/e2ee-bench.txt --destination "$out/e2ee-bench.txt" >/dev/null 2>&1; then
			break
		fi
	done
	cat "$out/e2ee-bench.txt"
	xcrun devicectl device uninstall app --device "$device" "$id" >/dev/null
	;;
android)
	fvm flutter build apk --profile
	adb -s "$device" install -r build/app/outputs/flutter-apk/app-profile.apk >/dev/null
	adb -s "$device" logcat -c
	adb -s "$device" shell am start -n "$id/.MainActivity" >/dev/null
	for _ in $(seq 120); do
		sleep 5
		if adb -s "$device" logcat -d -s flutter | grep -q 'E2EE-BENCH done'; then
			break
		fi
	done
	adb -s "$device" logcat -d -s flutter | sed -n 's/.*E2EE-BENCH //p' | grep -v '^done'
	adb -s "$device" uninstall "$id" >/dev/null
	;;
*)
	echo "unknown platform $platform" >&2
	exit 2
	;;
esac
