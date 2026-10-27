// desksession 直接跑手机插件里的 DeskSession，测的就是手机上那份收包、重排、补发、解码，每秒打一行统计。
// 用 desk cast 的固定测试密钥，配 deskshape 造差网络：
//
//	xcrun swiftc -O -o /tmp/desksession tools/desksession/main.swift mobile/ios/Runner/DeskSession.swift
//	posse desk cast --addr 127.0.0.1:47991 &
//	go run ./tools/deskshape --listen 127.0.0.1:45991 --forward 127.0.0.1:47991 --rate 500kbps --loss 0.05 --delay 150ms &
//	/tmp/desksession --port 45991 --seconds 60 [--no-nack] [--dump DIR]
//
// --dump 每秒把最近解码出的一帧存成 DIR/t<第几秒>.png，拿去和截图比清晰度（ffmpeg 的 psnr、ssim）。
import CoreImage
import CryptoKit
import Foundation

setvbuf(stdout, nil, _IOLBF, 0)
var port: UInt16 = 47991
var seconds: Double = 0
var nack = true
var dump: String?
var args = Array(CommandLine.arguments.dropFirst())
while !args.isEmpty {
  let arg = args.removeFirst()
  switch arg {
  case "--port": port = UInt16(args.isEmpty ? "" : args.removeFirst()) ?? port
  case "--seconds": seconds = Double(args.isEmpty ? "" : args.removeFirst()) ?? 0
  case "--no-nack": nack = false
  case "--dump": dump = args.isEmpty ? nil : args.removeFirst()
  default:
    print("usage: desksession [--port N] [--seconds N] [--no-nack] [--dump DIR]")
    exit(2)
  }
}

let material = HKDF<SHA256>.deriveKey(
  inputKeyMaterial: SymmetricKey(data: Data("posse-desk-test-secret-32bytes!".utf8)),
  salt: Data("posse-desk-nonce".utf8), info: Data("posse-desk/v1".utf8), outputByteCount: 64
).withUnsafeBytes { Data($0) }
guard let peer = deskResolve(host: "127.0.0.1", port: port),
  let session = try? DeskSession(
    toMobile: material.prefix(32), toMac: material.suffix(32), peers: [peer], relay: nil,
    relayToken: nil, codec: .hevc, nack: nack)
else {
  print("desksession: cannot start")
  exit(1)
}
let lock = NSLock()
var frames = 0
var total = 0
var last: [String: Any] = [:]
var latest: CVPixelBuffer?
let images = CIContext()
var shot = 0
session.onFrame = { image in
  lock.lock()
  latest = image
  frames += 1
  total += 1
  lock.unlock()
}
session.onStats = { stats in
  lock.lock()
  last = stats
  lock.unlock()
}
session.start()
print("desksession -> 127.0.0.1:\(port) nack=\(nack)")
let started = CFAbsoluteTimeGetCurrent()
while seconds <= 0 || CFAbsoluteTimeGetCurrent() - started < seconds {
  Thread.sleep(forTimeInterval: 1)
  lock.lock()
  print(
    String(
      format: "t=%.0fs decoded=%d total=%d loss=%d recoveries=%d bitrate=%.0fkbps",
      CFAbsoluteTimeGetCurrent() - started, frames, total, last["loss"] as? Int ?? 0,
      last["recoveries"] as? Int ?? 0, (last["bitrate"] as? Double ?? 0) / 1000))
  frames = 0
  let image = latest
  lock.unlock()
  if let dump, let image {
    shot += 1
    let url = URL(fileURLWithPath: dump).appendingPathComponent(String(format: "t%02d.png", shot))
    try? images.writePNGRepresentation(
      of: CIImage(cvPixelBuffer: image), to: url, format: .RGBA8,
      colorSpace: CGColorSpace(name: CGColorSpace.sRGB)!)
  }
}
session.requestStop()
session.waitUntilStopped()
