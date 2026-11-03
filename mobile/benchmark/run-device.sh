#!/bin/bash
# 在真机上跑 e2ee_bench（profile 模式，AOT），打印结果。
#   benchmark/run-device.sh ios <设备 UDID>       # xcrun devicectl list devices
#   benchmark/run-device.sh android <adb 序列号>   # adb devices
# 装的是临时目录里另建的小工程（包 ID com.neolabsapp.possebench），不覆盖手机上的 Posse，
# 也就不碰它的配对和设置；跑完（失败、超时也一样）把这个测试 App 删掉。
set -euo pipefail

platform=${1:?ios or android}
device=${2:?device id}
here=$(cd "$(dirname "$0")" && pwd)
mobile=$(dirname "$here")
work=${TMPDIR:-/tmp}/posse-e2ee-bench-app
id=com.neolabsapp.possebench

# 在 mobile/ 下建，模板和编译都用它 .fvmrc 里的 Flutter；stdin 不是终端时 flutter create
# 不问用哪张签名证书（本机有几张开发证书），团队由下面写进工程
if [ ! -d "$work" ]; then
	(cd "$mobile" && fvm flutter create --project-name possebench --org com.neolabsapp \
		--platforms ios,android "$work" </dev/null >/dev/null)
fi
(cd "$work" && fvm use "$(sed -n 's/.*"flutter": "\(.*\)".*/\1/p' "$mobile/.fvmrc")" --force >/dev/null)
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
pbx="$work/ios/Runner.xcodeproj/project.pbxproj"
# 先去掉已有的团队设置再写一份，工程复用时不会越写越多
ID=$id perl -pi -e 's/\s*DEVELOPMENT_TEAM = [^;]*;//g; s/(PRODUCT_BUNDLE_IDENTIFIER = \Q$ENV{ID}\E;)/$1 DEVELOPMENT_TEAM = QKSM9Z4U85;/' "$pbx"
cd "$work"
fvm flutter pub get >/dev/null

# 跑完、失败、超时都把测试 App 删掉
cleanup() {
	if [ "$platform" = ios ]; then
		xcrun devicectl device uninstall app --device "$device" "$id" >/dev/null 2>&1 || true
	else
		adb -s "$device" uninstall "$id" >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

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
	if [ ! -f "$out/e2ee-bench.txt" ]; then
		echo "no result after 10 minutes: did the app crash?" >&2
		exit 1
	fi
	cat "$out/e2ee-bench.txt"
	rm -rf "$out"
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
	if ! adb -s "$device" logcat -d -s flutter | grep -q 'E2EE-BENCH done'; then
		echo "no result after 10 minutes: did the app crash?" >&2
		exit 1
	fi
	adb -s "$device" logcat -d -s flutter | sed -n 's/.*E2EE-BENCH //p' | grep -v '^done'
	;;
*)
	echo "unknown platform $platform" >&2
	exit 2
	;;
esac
