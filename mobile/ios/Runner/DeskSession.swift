import CoreMedia
import CryptoKit
import Darwin
import Foundation
import VideoToolbox

private let deskPacketLimit = 1200
private let deskDirToMac: UInt8 = 0
private let deskDirToMobile: UInt8 = 1
private let deskKindHello: UInt8 = 1
private let deskKindVideo: UInt8 = 2
private let deskKindReport: UInt8 = 3
private let deskKindInput: UInt8 = 4
private let deskKindKeyframe: UInt8 = 5
private let deskKindBeat: UInt8 = 6
private let deskKindNack: UInt8 = 7
/// 一个补发请求最多点这么多个序号（u16），远小于包长上限
private let deskNackMax = 64
/// 一次缺这么多以上不点名补发，当成断档直接要关键帧
private let deskNackWindow = 512

enum DeskCodec: String {
  case hevc
  case h264
}

enum DeskError: Error {
  case badAddress
  case socket
  case unsupportedCodec
}

struct DeskKeys {
  let toMobile: SymmetricKey
  let toMac: SymmetricKey

  /// 和 tools/deskrecv、internal/desk 的 HKDF 一致：info 是 hive-desk/v1，前 32 字节给手机收，后 32 字节给手机发。
  static func derive(secret: Data, nonce: Data) -> DeskKeys {
    let out = HKDF<SHA256>.deriveKey(
      inputKeyMaterial: SymmetricKey(data: secret),
      salt: nonce,
      info: Data("hive-desk/v1".utf8),
      outputByteCount: 64
    )
    let raw = out.withUnsafeBytes { Data($0) }
    return DeskKeys(
      toMobile: SymmetricKey(data: raw.prefix(32)),
      toMac: SymmetricKey(data: raw.suffix(32))
    )
  }
}

private func deskNonce(_ dir: UInt8, _ counter: UInt64) -> AES.GCM.Nonce {
  var raw = Data(count: 12)
  raw[0] = dir
  var n = counter.bigEndian
  withUnsafeBytes(of: &n) { raw.replaceSubrange(4 ..< 12, with: $0) }
  return try! AES.GCM.Nonce(data: raw)
}

private func deskSeal(
  _ key: SymmetricKey, _ dir: UInt8, _ kind: UInt8, _ counter: UInt64, _ plain: Data
) throws -> Data {
  var packet = Data([kind])
  var n = counter.bigEndian
  withUnsafeBytes(of: &n) { packet.append(contentsOf: $0) }
  let box = try AES.GCM.seal(
    plain, using: key, nonce: deskNonce(dir, counter), authenticating: Data([kind])
  )
  packet.append(box.ciphertext)
  packet.append(box.tag)
  return packet
}

/// 直接切接收缓冲。密文和 tag 不拷贝；明文是 open 新分配的，调用返回后缓冲可以复用。
private func deskOpen(
  _ key: SymmetricKey, _ dir: UInt8, _ packet: UnsafeRawBufferPointer
) -> (UInt8, UInt64, Data)? {
  let count = packet.count
  guard count >= 1 + 8 + 16, count <= deskPacketLimit, let base = packet.baseAddress else {
    return nil
  }
  let kind = base.load(as: UInt8.self)
  var counterBits: UInt64 = 0
  withUnsafeMutableBytes(of: &counterBits) { dst in
    dst.copyMemory(from: UnsafeRawBufferPointer(start: base.advanced(by: 1), count: 8))
  }
  let counter = UInt64(bigEndian: counterBits)
  let cipherCount = count - 9 - 16
  let cipher = cipherCount == 0
    ? Data()
    : Data(
      bytesNoCopy: UnsafeMutableRawPointer(mutating: base.advanced(by: 9)),
      count: cipherCount,
      deallocator: .none
    )
  let tag = Data(
    bytesNoCopy: UnsafeMutableRawPointer(mutating: base.advanced(by: count - 16)),
    count: 16,
    deallocator: .none
  )
  guard
    let box = try? AES.GCM.SealedBox(nonce: deskNonce(dir, counter), ciphertext: cipher, tag: tag),
    let plain = try? AES.GCM.open(box, using: key, authenticating: Data([kind]))
  else { return nil }
  return (kind, counter, plain)
}

private func beU16(_ data: Data, _ offset: Int) -> UInt16 {
  var value: UInt16 = 0
  _ = withUnsafeMutableBytes(of: &value) { data.copyBytes(to: $0, from: offset ..< offset + 2) }
  return UInt16(bigEndian: value)
}

private func beU64(_ data: Data, _ offset: Int) -> UInt64 {
  var value: UInt64 = 0
  _ = withUnsafeMutableBytes(of: &value) { data.copyBytes(to: $0, from: offset ..< offset + 8) }
  return UInt64(bigEndian: value)
}

/// 1024 位滑动窗口，和 WireGuard、deskrecv 一样：乱序照收，重复的和落在窗口前的丢掉。
private final class DeskReplay {
  private var last: UInt64 = 0
  private var bits = [UInt64](repeating: 0, count: 16)

  func reset() {
    last = 0
    bits = [UInt64](repeating: 0, count: 16)
  }

  func accept(_ n: UInt64) -> Bool {
    if n == 0 { return false }
    if last == 0 {
      last = n
      bits[0] = 1
      return true
    }
    if n > last {
      let shift = n - last
      if shift >= 1024 {
        bits = [UInt64](repeating: 0, count: 16)
      } else {
        self.shift(shift)
      }
      last = n
      bits[0] |= 1
      return true
    }
    let back = last - n
    if back >= 1024 { return false }
    let index = Int(back / 64)
    let bit = back % 64
    if bits[index] & (UInt64(1) << bit) != 0 { return false }
    bits[index] |= UInt64(1) << bit
    return true
  }

  private func shift(_ n: UInt64) {
    let words = Int(n / 64)
    let bitsn = n % 64
    var next = [UInt64](repeating: 0, count: 16)
    for i in 0 ..< 16 {
      let from = words + i
      if from >= 16 { break }
      next[i] = bits[from] << bitsn
      if bitsn != 0, from + 1 < 16 {
        next[i] |= bits[from + 1] >> (64 - bitsn)
      }
    }
    bits = next
  }
}

struct DeskAccessUnit {
  let nals: [Data]
  let frame: UInt64
  let capture: Int64
  let key: Bool
}

protocol DeskDepacketizer: AnyObject {
  func push(_ plain: Data) -> DeskAccessUnit?
  func reset()
}

/// H.264（RFC 6184）留到能开低延迟码率控制再做。现在工厂对它返回空。
func makeDepacketizer(_ codec: DeskCodec) -> DeskDepacketizer? {
  switch codec {
  case .hevc:
    return HevcDepacketizer()
  case .h264:
    return nil
  }
}

private final class HevcDepacketizer: DeskDepacketizer {
  private var frame: UInt64 = 0
  private var capture: Int64 = 0
  private var key = false
  private var expect: UInt16 = 0
  private var have = false
  private var broken = false
  private var nals: [Data] = []
  private var fu = Data()

  func reset() {
    have = false
    broken = false
    nals = []
    fu = Data()
  }

  func push(_ plain: Data) -> DeskAccessUnit? {
    guard plain.count >= 17 + 12 else { return nil }
    let key = plain[0] & 1 != 0
    let number = beU64(plain, 1)
    let capture = Int64(bitPattern: beU64(plain, 9))
    let seq = beU16(plain, 19)
    let marker = plain[18] & 0x80 != 0
    let payload = plain.subdata(in: 29 ..< plain.count)
    if !have || number != frame {
      frame = number
      self.capture = capture
      self.key = key
      expect = seq
      have = true
      broken = false
      nals = []
      fu = Data()
    } else if seq != expect {
      broken = true
      fu = Data()
      nals = []
      expect = seq
      return nil
    }
    expect = expect &+ 1
    take(payload)
    guard marker else { return nil }
    have = false
    if broken || !fu.isEmpty { return nil }
    return DeskAccessUnit(
      nals: nals.filter { !$0.isEmpty },
      frame: frame,
      capture: self.capture,
      key: self.key
    )
  }

  private func take(_ payload: Data) {
    if payload.count >= 3, ((payload[0] >> 1) & 0x3f) == 49 {
      let start = payload[2] & 0x80 != 0
      let end = payload[2] & 0x40 != 0
      let piece = payload.subdata(in: 3 ..< payload.count)
      if start {
        fu = piece
      } else if fu.isEmpty {
        // 这一帧的开头已经丢了，拼出来也是花的。
        broken = true
        return
      } else {
        fu.append(piece)
      }
      if end, !broken {
        var nal = Data([payload[0], payload[1]])
        nal.append(fu)
        nal[0] = (payload[0] & 0x81) | ((payload[2] & 0x3f) << 1)
        nals.append(nal)
        fu = Data()
      }
      return
    }
    if !payload.isEmpty {
      nals.append(payload)
    }
  }
}

private func nalType(_ nal: Data) -> UInt8 {
  nal.isEmpty ? 0 : (nal[0] >> 1) & 0x3f
}

private final class DecodeStamp {
  let started: CFAbsoluteTime
  init(_ started: CFAbsoluteTime) { self.started = started }
}

private final class DeskDecoder {
  var onImage: ((CVPixelBuffer, Double) -> Void)?
  private var session: VTDecompressionSession?
  private var format: CMVideoFormatDescription?
  private var formatKey = Data()

  func shutdown() {
    guard let session else { return }
    VTDecompressionSessionWaitForAsynchronousFrames(session)
    VTDecompressionSessionInvalidate(session)
    self.session = nil
    format = nil
    formatKey = Data()
  }

  func push(_ unit: DeskAccessUnit) {
    let sets = parameterSets(unit.nals)
    if let sets {
      let key = sets.reduce(into: Data()) { $0.append($1) }
      if key != formatKey {
        shutdown()
        formatKey = key
        format = makeFormat(sets)
        if let format {
          var callback = VTDecompressionOutputCallbackRecord(
            decompressionOutputCallback: deskOutputCallback,
            decompressionOutputRefCon: Unmanaged.passUnretained(self).toOpaque()
          )
          let attrs = deskImageAttributes()
          var created: VTDecompressionSession?
          var status = VTDecompressionSessionCreate(
            allocator: nil,
            formatDescription: format,
            decoderSpecification: nil,
            imageBufferAttributes: attrs,
            outputCallback: &callback,
            decompressionSessionOut: &created
          )
          if status != noErr {
            status = VTDecompressionSessionCreate(
              allocator: nil,
              formatDescription: format,
              decoderSpecification: nil,
              imageBufferAttributes: nil,
              outputCallback: &callback,
              decompressionSessionOut: &created
            )
          }
          if status == noErr {
            session = created
            if let created {
              VTSessionSetProperty(
                created,
                key: kVTDecompressionPropertyKey_RealTime,
                value: kCFBooleanTrue
              )
            }
          }
        }
      }
    }
    guard let session else { return }
    let slices = unit.nals.filter {
      let kind = nalType($0)
      return kind != 32 && kind != 33 && kind != 34 && kind != 35
    }
    guard !slices.isEmpty, let sample = sampleBuffer(slices, format: format) else { return }
    let stamp = DecodeStamp(CFAbsoluteTimeGetCurrent())
    let ref = Unmanaged.passRetained(stamp).toOpaque()
    var flags = VTDecodeInfoFlags()
    let status = VTDecompressionSessionDecodeFrame(
      session,
      sampleBuffer: sample,
      flags: ._EnableAsynchronousDecompression,
      frameRefcon: ref,
      infoFlagsOut: &flags
    )
    if status != noErr {
      Unmanaged<DecodeStamp>.fromOpaque(ref).release()
    }
  }
}

private func deskOutputCallback(
  _ refcon: UnsafeMutableRawPointer?,
  _ source: UnsafeMutableRawPointer?,
  _ status: OSStatus,
  _ flags: VTDecodeInfoFlags,
  _ image: CVImageBuffer?,
  _ pts: CMTime,
  _ duration: CMTime
) {
  let stamp = source.map { Unmanaged<DecodeStamp>.fromOpaque($0).takeRetainedValue() }
  guard let refcon else { return }
  let decoder = Unmanaged<DeskDecoder>.fromOpaque(refcon).takeUnretainedValue()
  let ms = stamp.map { (CFAbsoluteTimeGetCurrent() - $0.started) * 1000 } ?? 0
  guard status == noErr, let image else { return }
  decoder.onImage?(image, ms)
}

/// Flutter 的纹理直接吃 420v，和编码器交出来的一样，解码器不必再转成 BGRA。
private func deskImageAttributes() -> CFDictionary {
  [
    kCVPixelBufferPixelFormatTypeKey: NSNumber(value: kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange),
    kCVPixelBufferIOSurfacePropertiesKey: [:] as CFDictionary,
    kCVPixelBufferMetalCompatibilityKey: true,
  ] as CFDictionary
}

private func parameterSets(_ nals: [Data]) -> [Data]? {
  var vps: Data?
  var sps: Data?
  var pps: Data?
  for nal in nals {
    switch nalType(nal) {
    case 32 where vps == nil: vps = nal
    case 33 where sps == nil: sps = nal
    case 34 where pps == nil: pps = nal
    default: break
    }
  }
  guard let vps, let sps, let pps else { return nil }
  return [vps, sps, pps]
}

private func makeFormat(_ sets: [Data]) -> CMVideoFormatDescription? {
  let copies: [(UnsafeMutablePointer<UInt8>, Int)] = sets.map { data in
    let pointer = UnsafeMutablePointer<UInt8>.allocate(capacity: data.count)
    data.copyBytes(to: pointer, count: data.count)
    return (pointer, data.count)
  }
  defer { copies.forEach { $0.0.deallocate() } }
  var pointers = copies.map { UnsafePointer($0.0) }
  var sizes = copies.map { $0.1 }
  var format: CMVideoFormatDescription?
  let status = CMVideoFormatDescriptionCreateFromHEVCParameterSets(
    allocator: nil,
    parameterSetCount: pointers.count,
    parameterSetPointers: &pointers,
    parameterSetSizes: &sizes,
    nalUnitHeaderLength: 4,
    extensions: nil,
    formatDescriptionOut: &format
  )
  return status == noErr ? format : nil
}

private func sampleBuffer(_ nals: [Data], format: CMVideoFormatDescription?) -> CMSampleBuffer? {
  guard let format else { return nil }
  var avcc = Data()
  for nal in nals {
    var len = UInt32(nal.count).bigEndian
    withUnsafeBytes(of: &len) { avcc.append(contentsOf: $0) }
    avcc.append(nal)
  }
  var block: CMBlockBuffer?
  guard
    CMBlockBufferCreateWithMemoryBlock(
      allocator: nil,
      memoryBlock: nil,
      blockLength: avcc.count,
      blockAllocator: nil,
      customBlockSource: nil,
      offsetToData: 0,
      dataLength: avcc.count,
      flags: 0,
      blockBufferOut: &block
    ) == noErr,
    let block
  else { return nil }
  avcc.withUnsafeBytes { raw in
    _ = CMBlockBufferReplaceDataBytes(
      with: raw.baseAddress!,
      blockBuffer: block,
      offsetIntoDestination: 0,
      dataLength: avcc.count
    )
  }
  var sample: CMSampleBuffer?
  var sizes = [avcc.count]
  var timing = CMSampleTimingInfo(
    duration: .invalid,
    presentationTimeStamp: .zero,
    decodeTimeStamp: .invalid
  )
  guard
    CMSampleBufferCreateReady(
      allocator: nil,
      dataBuffer: block,
      formatDescription: format,
      sampleCount: 1,
      sampleTimingEntryCount: 1,
      sampleTimingArray: &timing,
      sampleSizeEntryCount: 1,
      sampleSizeArray: &sizes,
      sampleBufferOut: &sample
    ) == noErr
  else { return nil }
  return sample
}

/// 一个缺的视频包。found 是发现缺它的时刻，firstAsk、lastAsk 是第一次和最近一次点名补发的时刻。
struct DeskMiss {
  let found: CFAbsoluteTime
  var firstAsk: CFAbsoluteTime = 0
  var lastAsk: CFAbsoluteTime = 0
  var asked = 0

  init(found: CFAbsoluteTime) { self.found = found }
}

/// 专门的线程收 UDP。视频包在这里解密、拆 RTP、交给硬解，不经 Dart。
final class DeskSession {
  var onFrame: ((CVPixelBuffer) -> Void)?
  var onStats: (([String: Any]) -> Void)?

  private let keys: DeskKeys
  /// 直连候选（局域网等）和中转，hello 发给全部；收到 Mac 的包后只回给那条路。
  private let candidates: [sockaddr_in]
  private let relay: sockaddr_in?
  private let relayToken: Data
  /// Mac 最近一次发包来的地址，ioLock 保护。Mac 优先直连，手机跟着它走
  private var active: sockaddr_in?
  private var activeAt: CFAbsoluteTime = 0
  private let depacketizer: DeskDepacketizer
  private let decoder = DeskDecoder()
  private let replay = DeskReplay()
  private let flag = NSLock()
  private let statsLock = NSLock()
  private let joined = DispatchSemaphore(value: 0)

  private var thread: Thread?
  private var stopped = false
  private var paused = false
  private var needReset = false
  private var didJoin = false
  private var sendN: UInt64 = 0
  private var expect: UInt16 = 0
  private var haveSeq = false
  /// Mac 认补发请求（ready 里的 nack）。不认时缺包照旧直接要关键帧
  private let nack: Bool
  /// 比 expect 先到的包，等前面缺的补上再按序交给拆包；值是明文和到达时刻
  private var held: [UInt16: (Data, CFAbsoluteTime)] = [:]
  /// 收到过的最大序号（以 expect 为起点比远近），新来的包比它还远，中间的就是新缺的
  private var highest: UInt16 = 0
  /// 缺的包：第一次发现、上次点名的时刻和点了几次
  private var missing: [UInt16: DeskMiss] = [:]
  /// 往返时间的估计，按点名到补上来的时间算，决定多久再点一次、等多久放弃
  private var rtt: Double = 0.15
  /// 这一帧里有补上来的包：它到得晚是因为丢过，不是排队，不拿来算排队延迟和帧间隔
  private var fillFrame: UInt64?
  /// 刚发现丢包、不能补：下一圈马上要关键帧
  private var nextKeyNow = false
  private var lossInterval: UInt32 = 0
  private var lossTotal: UInt32 = 0
  private var packetsInterval: UInt32 = 0
  private var gapsUs: [UInt32] = []
  private var lastFrameAt: CFAbsoluteTime = 0
  /// 每帧「到达减采集」的秒数，含两台设备的时钟差。这一圈报告里的，和每秒的最小值。
  private var delays: [Double] = []
  private var delayFloor: [(Int, Double)] = []
  private var waitingKey = true
  private var primed = false
  private var recoveries = 0
  private var gotVideo = false
  private var videoBytes: [(CFAbsoluteTime, Int)] = []
  private var decodedAt: [CFAbsoluteTime] = []
  private var decodeMs: [(CFAbsoluteTime, Double)] = []
  private var pictureWidth = 0
  private var pictureHeight = 0
  private var inputAck = 0
  /// 经中转探测到的这个 socket 的公网地址，报给 Dart 去和 Mac 交换、打洞。只在收包线程上读写
  private var publicAddr: String?
  private var probes = 0
  private let ioLock = NSLock()
  private var socketFd: Int32 = -1
  /// 打洞的目标（Mac 出口的公网地址），ioLock 保护。punchUntil 之前 hello、report 也抄送一份过去
  private var punchPeer: sockaddr_in?
  private var punchUntil: CFAbsoluteTime = 0

  /// 两把密钥由 Dart 用会话密钥派生好。这里不再做 HKDF。
  init(
    toMobile: Data, toMac: Data, peers: [sockaddr_in], relay: sockaddr_in?, relayToken: Data?,
    codec: DeskCodec, nack: Bool
  ) throws {
    self.nack = nack
    guard toMobile.count == 32, toMac.count == 32 else { throw DeskError.badAddress }
    guard !peers.isEmpty || relay != nil else { throw DeskError.badAddress }
    guard let depacketizer = makeDepacketizer(codec) else { throw DeskError.unsupportedCodec }
    keys = DeskKeys(
      toMobile: SymmetricKey(data: toMobile),
      toMac: SymmetricKey(data: toMac)
    )
    if let relay, let relayToken, relayToken.count == deskRelayTokenLen {
      self.relay = relay
      self.relayToken = relayToken
      candidates = peers + [relay]
    } else {
      self.relay = nil
      self.relayToken = Data()
      candidates = peers
    }
    self.depacketizer = depacketizer
    decoder.onImage = { [weak self] image, ms in
      self?.noteDecoded(image, ms: ms)
    }
  }

  /// 明文已经是 EncodeInput 的布局。和 hello 共用发送计数器，可以从任意线程调用。
  func sendInput(_ plain: Data) {
    send(deskKindInput, plain)
  }

  /// 向 Mac 出口的公网地址打洞：几秒内连发打洞包，同时把 hello、report 也发过去。
  /// Mac 从这个地址解开手机的包就改发直连，手机收到后跟着走（noteActive）。可以从任意线程调用。
  func punch(_ peer: sockaddr_in) {
    ioLock.lock()
    punchPeer = peer
    punchUntil = CFAbsoluteTimeGetCurrent() + 4
    ioLock.unlock()
  }

  func start() {
    let thread = Thread { [weak self] in self?.loop() }
    thread.name = "hive.desk"
    self.thread = thread
    thread.start()
  }

  func requestStop() {
    flag.lock()
    stopped = true
    flag.unlock()
  }

  func setPaused(_ value: Bool) {
    flag.lock()
    if value {
      paused = true
    } else if paused {
      paused = false
      needReset = true
    }
    flag.unlock()
  }

  func waitUntilStopped() {
    flag.lock()
    if didJoin || thread == nil {
      flag.unlock()
      return
    }
    flag.unlock()
    joined.wait()
    flag.lock()
    didJoin = true
    flag.unlock()
  }

  private func takeFlags() -> (stopped: Bool, paused: Bool, reset: Bool) {
    flag.lock()
    defer { flag.unlock() }
    let reset = needReset
    needReset = false
    return (stopped, paused, reset)
  }

  /// 重新 hello。接收窗口清掉：Mac 可能已经把发送计数从头计。
  private func prepareForPeer() {
    ioLock.lock()
    active = nil
    punchPeer = nil
    punchUntil = 0
    ioLock.unlock()
    publicAddr = nil
    probes = 0
    gotVideo = false
    replay.reset()
    depacketizer.reset()
    waitingKey = true
    primed = false
    haveSeq = false
    expect = 0
    highest = 0
    held.removeAll()
    missing.removeAll()
    fillFrame = nil
    lastFrameAt = 0
    lossInterval = 0
    lossTotal = 0
    packetsInterval = 0
    gapsUs.removeAll(keepingCapacity: true)
    delays.removeAll(keepingCapacity: true)
    delayFloor.removeAll(keepingCapacity: true)
    statsLock.lock()
    videoBytes.removeAll(keepingCapacity: true)
    decodedAt.removeAll(keepingCapacity: true)
    decodeMs.removeAll(keepingCapacity: true)
    statsLock.unlock()
  }

  private func loop() {
    var fd: Int32 = -1
    defer {
      decoder.shutdown()
      let open = takeSocket()
      if open >= 0 { close(open) }
      joined.signal()
    }
    var nextHello: CFAbsoluteTime = 0
    var nextRegister: CFAbsoluteTime = 0
    var nextReport: CFAbsoluteTime = 0
    var nextKey: CFAbsoluteTime = 0
    var nextStats: CFAbsoluteTime = 0
    var nextProbe: CFAbsoluteTime = 0
    var nextPunch: CFAbsoluteTime = 0
    var buf = [UInt8](repeating: 0, count: 2048)
    while true {
      let state = takeFlags()
      if state.stopped { break }
      if state.paused {
        if fd >= 0 {
          closeSocket(fd)
          fd = -1
        }
        Thread.sleep(forTimeInterval: 0.05)
        continue
      }
      if fd < 0 || state.reset {
        if fd >= 0 { closeSocket(fd) }
        fd = openSocket()
        if fd < 0 {
          Thread.sleep(forTimeInterval: 0.2)
          continue
        }
        adoptSocket(fd)
        prepareForPeer()
        let now = CFAbsoluteTimeGetCurrent()
        nextHello = now
        nextRegister = now
        nextReport = now
        nextKey = now
        nextStats = now
      }
      let now = CFAbsoluteTimeGetCurrent()
      if relay != nil, now >= nextRegister {
        registerRelay()
        nextRegister = now + 5
      }
      // 画面经中转到了才探测：局域网通着不用打洞。探测几次没回就算了
      if relay != nil, publicAddr == nil, probes < 6, now >= nextProbe, gotVideo, path() == "relay" {
        sendControl(deskRelayProbe(), toRelay: true)
        probes += 1
        nextProbe = now + 0.5
      }
      if now >= nextPunch, let peer = punchTarget(now) {
        sendControl(Data([0x48, 0x52, 0x59, 0x31, 0x05]), to: peer)
        nextPunch = now + 0.05
      }
      if !gotVideo, now >= nextHello {
        send(deskKindHello, Data())
        nextHello = now + 0.1
      }
      if now >= nextReport {
        sendReport()
        nextReport = now + 0.1
      }
      // 进来时多半赶不上开头那个关键帧，参数集还没有，先要一张。丢包之后也走这里。
      if waitingKey, now >= nextKey || nextKeyNow {
        nextKeyNow = false
        send(deskKindKeyframe, Data())
        nextKey = now + 0.2
      }
      if now >= nextStats {
        emitStats(now)
        nextStats = now + 0.2
      }
      serviceMissing(now)
      var from = sockaddr_in()
      var fromLen = socklen_t(MemoryLayout<sockaddr_in>.size)
      let n = withUnsafeMutablePointer(to: &from) { pointer in
        pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) {
          recvfrom(fd, &buf, buf.count, 0, $0, &fromLen)
        }
      }
      if n <= 0 { continue }
      // 中转的回执、探测回包和 Mac 的打洞包都不是 desk 包
      if n >= 4, buf[0] == 0x48, buf[1] == 0x52, buf[2] == 0x59, buf[3] == 0x31 {
        if n == 11, buf[4] == 0x83, let relay, deskSameAddr(from, relay) {
          let port = UInt16(buf[9]) << 8 | UInt16(buf[10])
          publicAddr = "\(buf[5]).\(buf[6]).\(buf[7]).\(buf[8]):\(port)"
          emitStats(CFAbsoluteTimeGetCurrent())
        }
        continue
      }
      let opened = buf.withUnsafeBytes { raw -> (UInt8, UInt64, Data)? in
        let slice = UnsafeRawBufferPointer(start: raw.baseAddress, count: n)
        return deskOpen(keys.toMobile, deskDirToMobile, slice)
      }
      guard let opened else { continue }
      if !replay.accept(opened.1) { continue }
      noteActive(from)
      if opened.0 == deskKindBeat {
        noteBeat(opened.2)
        continue
      }
      if opened.0 != deskKindVideo { continue }
      gotVideo = true
      noteVideo(bytes: n, now: CFAbsoluteTimeGetCurrent())
      guard opened.2.count >= 29 else { continue }
      receive(opened.2, at: CFAbsoluteTimeGetCurrent())
    }
  }

  /// 一个视频包按 RTP 序号排进来。正好是下一个就交给拆包，再把排在后面已经到了的接着交；
  /// 跳过了几个就记成丢包：Mac 认补发时先存着、点名要缺的，不认时照旧跳过去、要关键帧。
  private func receive(_ plain: Data, at arrival: CFAbsoluteTime) {
    let seq = beU16(plain, 19)
    packetsInterval += 1
    if !haveSeq {
      expect = seq
      highest = seq
      haveSeq = true
    }
    let ahead = seq &- expect
    // 回绕过来的旧包、重复的补发
    if ahead >= 32768 { return }
    var filled = false
    if let miss = missing.removeValue(forKey: seq) {
      filled = true
      if miss.asked > 0 {
        // 点过不止一次的分不清是哪次补上的，按最早那次算，估计只会偏大
        rtt = rtt * 0.8 + max(arrival - miss.firstAsk, 0.005) * 0.2
      }
    } else if ahead > 0, held[seq] != nil {
      return
    }
    // highest 比 expect 还小一个（前面的都交了）时 far 是 -1
    let far = Int(Int16(bitPattern: highest &- expect))
    if Int(ahead) > far {
      let gap = Int(ahead) - far - 1
      if gap > 0 {
        lossInterval += UInt32(gap)
        lossTotal += UInt32(gap)
      }
      if nack, gap > 0, gap <= deskNackWindow {
        var s = highest &+ 1
        while s != seq {
          missing[s] = DeskMiss(found: arrival)
          s = s &+ 1
        }
      } else if gap > 0 {
        // 不能补，或者一下断了太多（补也来不及）：跳过缺的，拆包看到序号断了会丢掉这一帧，接着等关键帧
        held.removeAll()
        missing.removeAll()
        expect = seq
        highest = seq
        markLost()
        deliver(plain, at: arrival, filled: false)
        return
      }
      highest = seq
    }
    if seq == expect {
      deliver(plain, at: arrival, filled: filled)
      drain()
    } else {
      held[seq] = (plain, arrival)
    }
  }

  /// 把从 expect 起已经到了的包按序交出去，碰到还缺的停下。
  private func drain() {
    while let (plain, at) = held.removeValue(forKey: expect) {
      deliver(plain, at: at, filled: false)
    }
  }

  /// 点名要缺的包：新缺的马上要，没补上的隔一会儿再要，最多三次。最前面缺的那个等太久（或者攒的包太多）
  /// 就不等了：跳到下一个到了的包，拆包丢掉缺了的帧，要关键帧。
  private func serviceMissing(_ now: CFAbsoluteTime) {
    guard nack, !missing.isEmpty else { return }
    let retry = max(rtt * 1.5, 0.04)
    var ask: [UInt16] = []
    for (seq, miss) in missing where miss.asked < 3 && now - miss.lastAsk >= retry {
      ask.append(seq)
      missing[seq]!.lastAsk = now
      if miss.asked == 0 { missing[seq]!.firstAsk = now }
      missing[seq]!.asked += 1
    }
    var start = 0
    while start < ask.count {
      var raw = Data()
      for seq in ask[start ..< min(start + deskNackMax, ask.count)] {
        raw.append(UInt8(seq >> 8))
        raw.append(UInt8(seq & 0xff))
      }
      send(deskKindNack, raw)
      start += deskNackMax
    }
    let giveUp = min(max(rtt * 3 + 0.1, 0.25), 1.0)
    guard let first = missing[expect], now - first.found > giveUp || held.count > 2048 else { return }
    // 下一个已经到了的包
    guard let resume = held.keys.min(by: { ($0 &- expect) < ($1 &- expect) }) else {
      missing.removeAll()
      return
    }
    var s = expect
    while s != resume {
      missing.removeValue(forKey: s)
      s = s &+ 1
    }
    expect = resume
    markLost()
    drain()
  }

  private func markLost() {
    if !waitingKey {
      waitingKey = true
      // 下一圈马上要关键帧，不等上一个 200 ms 间隔走完。
      nextKeyNow = true
    }
  }

  private func deliver(_ plain: Data, at arrival: CFAbsoluteTime, filled: Bool) {
    expect = plain.count >= 21 ? beU16(plain, 19) &+ 1 : expect &+ 1
    let number = beU64(plain, 1)
    if filled { fillFrame = number }
    guard let unit = depacketizer.push(plain) else { return }
    let idr = unit.nals.contains { (16 ... 21).contains(nalType($0)) }
    if waitingKey {
      if unit.key || idr {
        if primed { recoveries += 1 }
        waitingKey = false
        primed = true
      } else {
        return
      }
    }
    if fillFrame == unit.frame {
      lastFrameAt = 0
    } else {
      if lastFrameAt != 0 {
        // 排在缺口后面的包按当初的到达时刻交出来，可能比上一帧还早
        gapsUs.append(UInt32(min(max(arrival - lastFrameAt, 0) * 1_000_000, Double(UInt32.max))))
      }
      lastFrameAt = max(arrival, lastFrameAt)
      noteDelay(capture: beU64(plain, 9))
    }
    decoder.push(unit)
  }

  private func sendReport() {
    let loss = lossInterval
    let packets = packetsInterval
    let highest = expect
    let gap = medianGap()
    let queue = queueDelayUs()
    lossInterval = 0
    packetsInterval = 0
    gapsUs.removeAll(keepingCapacity: true)
    delays.removeAll(keepingCapacity: true)
    var raw = Data()
    func put64(_ value: UInt64) {
      var x = value.bigEndian
      withUnsafeBytes(of: &x) { raw.append(contentsOf: $0) }
    }
    func put32(_ value: UInt32) {
      var x = value.bigEndian
      withUnsafeBytes(of: &x) { raw.append(contentsOf: $0) }
    }
    put64(UInt64(highest))
    put32(loss)
    put32(packets)
    // 排队延迟（微秒），后面的 1 表示这个字段有效：旧版手机这两格是 0
    put64(queue ?? 0)
    put64(queue == nil ? 0 : 1)
    put32(gap)
    send(deskKindReport, raw)
  }

  /// 时钟差是常数，减掉最近 10 秒里最小的那次，剩下的就是这一帧比最顺的时候多排的队。
  /// 不用帧间隔：Mac 只在画面变了才出帧，帧率忽高忽低，间隔变大不等于排队。
  private func noteDelay(capture: UInt64) {
    guard capture > 0 else { return }
    let now = Date().timeIntervalSince1970
    let delay = now - Double(capture) / 1e9
    delays.append(delay)
    let second = Int(now)
    if let last = delayFloor.last, last.0 == second {
      delayFloor[delayFloor.count - 1].1 = min(last.1, delay)
    } else {
      delayFloor.append((second, delay))
    }
    delayFloor.removeAll { $0.0 < second - 10 }
  }

  private func queueDelayUs() -> UInt64? {
    guard !delays.isEmpty, let floor = delayFloor.map({ $0.1 }).min() else { return nil }
    let sorted = delays.sorted()
    let queue = max(0, sorted[sorted.count / 2] - floor)
    return UInt64(min(queue * 1_000_000, Double(UInt32.max)))
  }

  /// beat 明文 16 字节、大端：前 8 字节是时刻，后 8 字节是已执行的必达序号。
  private func noteBeat(_ plain: Data) {
    guard plain.count >= 16 else { return }
    inputAck = Int(beU64(plain, 8))
    emitStats(CFAbsoluteTimeGetCurrent())
  }

  private func medianGap() -> UInt32 {
    guard !gapsUs.isEmpty else { return 0 }
    let sorted = gapsUs.sorted()
    return sorted[sorted.count / 2]
  }

  private func send(_ kind: UInt8, _ plain: Data) {
    ioLock.lock()
    defer { ioLock.unlock() }
    guard socketFd >= 0 else { return }
    sendN += 1
    guard let packet = try? deskSeal(keys.toMac, deskDirToMac, kind, sendN, plain) else { return }
    // 2 秒没收到 Mac 的包就当路断了，重新发给全部候选，Mac 从哪条收到就从哪条回
    let now = CFAbsoluteTimeGetCurrent()
    if let active, now - activeAt < 2 {
      sendRaw(packet, to: active)
    } else {
      for dest in candidates { sendRaw(packet, to: dest) }
    }
    if let peer = punchPeer, now < punchUntil, active.map({ !deskSameAddr($0, peer) }) ?? true {
      sendRaw(packet, to: peer)
    }
  }

  /// 还在打洞期间返回目标。
  private func punchTarget(_ now: CFAbsoluteTime) -> sockaddr_in? {
    ioLock.lock()
    defer { ioLock.unlock() }
    guard let peer = punchPeer, now < punchUntil else { return nil }
    return peer
  }

  /// 画面现在走哪条路：lan（局域网、Tailscale 等直连地址）、relay、punched（打洞直连），还没收到为空。
  private func path() -> String? {
    ioLock.lock()
    defer { ioLock.unlock() }
    guard let active, CFAbsoluteTimeGetCurrent() - activeAt < 2 else { return nil }
    if let relay, deskSameAddr(active, relay) { return "relay" }
    if let peer = punchPeer, deskSameAddr(active, peer) { return "punched" }
    return "lan"
  }

  /// 不加密的控制包（探测、打洞）。
  private func sendControl(_ packet: Data, toRelay: Bool = false, to dest: sockaddr_in? = nil) {
    ioLock.lock()
    defer { ioLock.unlock() }
    guard socketFd >= 0, let dest = toRelay ? relay : dest else { return }
    sendRaw(packet, to: dest)
  }

  /// 调用方持有 ioLock。
  private func sendRaw(_ packet: Data, to dest: sockaddr_in) {
    var dest = dest
    let fd = socketFd
    packet.withUnsafeBytes { raw in
      _ = withUnsafePointer(to: &dest) { pointer in
        pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) {
          sendto(fd, raw.baseAddress, raw.count, 0, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
      }
    }
  }

  /// 向中转登记：明文，'HRY1'、身份 2、16 字节令牌。兼当保活，换网络后中转靠它更新地址。
  private func registerRelay() {
    guard let relay else { return }
    var packet = Data([0x48, 0x52, 0x59, 0x31, 2])
    packet.append(relayToken)
    ioLock.lock()
    defer { ioLock.unlock() }
    guard socketFd >= 0 else { return }
    sendRaw(packet, to: relay)
  }

  private func noteActive(_ from: sockaddr_in) {
    ioLock.lock()
    defer { ioLock.unlock() }
    let punched = punchPeer.map { deskSameAddr($0, from) } ?? false
    guard punched || candidates.contains(where: { deskSameAddr($0, from) }) else { return }
    active = from
    activeAt = CFAbsoluteTimeGetCurrent()
  }

  private func adoptSocket(_ fd: Int32) {
    ioLock.lock()
    let old = socketFd
    socketFd = fd
    ioLock.unlock()
    if old >= 0 && old != fd { close(old) }
  }

  /// 把描述符从发送路径摘掉。调用方负责 close。
  private func takeSocket() -> Int32 {
    ioLock.lock()
    let current = socketFd
    socketFd = -1
    ioLock.unlock()
    return current
  }

  private func closeSocket(_ fd: Int32) {
    let current = takeSocket()
    if current >= 0 { close(current) }
    if fd >= 0 && fd != current { close(fd) }
  }

  private func noteVideo(bytes: Int, now: CFAbsoluteTime) {
    statsLock.lock()
    videoBytes.append((now, bytes))
    videoBytes.removeAll { now - $0.0 > 2 }
    statsLock.unlock()
  }

  private func noteDecoded(_ image: CVPixelBuffer, ms: Double) {
    let now = CFAbsoluteTimeGetCurrent()
    statsLock.lock()
    decodedAt.append(now)
    decodeMs.append((now, ms))
    decodedAt.removeAll { now - $0 > 2 }
    decodeMs.removeAll { now - $0.0 > 2 }
    pictureWidth = CVPixelBufferGetWidth(image)
    pictureHeight = CVPixelBufferGetHeight(image)
    statsLock.unlock()
    onFrame?(image)
  }

  private func emitStats(_ now: CFAbsoluteTime) {
    statsLock.lock()
    let bytes = videoBytes.filter { now - $0.0 <= 1 }.reduce(0) { $0 + $1.1 }
    let frames = decodedAt.filter { now - $0 <= 1 }.count
    let samples = decodeMs.filter { now - $0.0 <= 1 }.map(\.1).sorted()
    let p50 = samples.isEmpty ? 0 : samples[samples.count / 2]
    let width = pictureWidth
    let height = pictureHeight
    statsLock.unlock()
    var stats: [String: Any] = [
      "fps": Double(frames),
      "bitrate": Double(bytes) * 8,
      "loss": Int(lossTotal),
      "decodeMs": p50,
      "recoveries": recoveries,
      "width": width,
      "height": height,
      "video": gotVideo,
      "ack": inputAck,
    ]
    if let publicAddr { stats["public"] = publicAddr }
    if let path = path() { stats["path"] = path }
    onStats?(stats)
  }

  private func openSocket() -> Int32 {
    let fd = socket(AF_INET, SOCK_DGRAM, 0)
    if fd < 0 { return -1 }
    var addr = sockaddr_in()
    addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = 0
    addr.sin_addr.s_addr = INADDR_ANY
    let bound = withUnsafePointer(to: &addr) { pointer in
      pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) {
        bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
      }
    }
    if bound != 0 {
      close(fd)
      return -1
    }
    // 没包来时也要醒得勤一点：缺的包隔几十毫秒要再点名一次
    var timeout = timeval(tv_sec: 0, tv_usec: 20_000)
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
    // 默认接收缓冲很小，关键帧连续十几个包会在套接字里被丢掉。
    var buffer: Int32 = 1 << 20
    setsockopt(fd, SOL_SOCKET, SO_RCVBUF, &buffer, socklen_t(MemoryLayout<Int32>.size))
    var one: Int32 = 1
    setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
    return fd
  }
}

let deskRelayTokenLen = 16

/// 中转的探测包：'HRY1'、0x03，补零到 24 字节，和 internal/deskrelay 一致。
func deskRelayProbe() -> Data {
  var packet = Data([0x48, 0x52, 0x59, 0x31, 0x03])
  packet.append(Data(count: 19))
  return packet
}

func deskSameAddr(_ a: sockaddr_in, _ b: sockaddr_in) -> Bool {
  a.sin_addr.s_addr == b.sin_addr.s_addr && a.sin_port == b.sin_port
}

/// "host:port" 形式的地址（中转）。
func deskResolveHostPort(_ text: String) -> sockaddr_in? {
  guard let colon = text.lastIndex(of: ":"), let port = UInt16(text[text.index(after: colon)...])
  else { return nil }
  return deskResolve(host: String(text[..<colon]), port: port)
}

func deskResolve(host: String, port: UInt16) -> sockaddr_in? {
  var addr = sockaddr_in()
  addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
  addr.sin_family = sa_family_t(AF_INET)
  addr.sin_port = port.bigEndian
  if host.withCString({ inet_pton(AF_INET, $0, &addr.sin_addr) }) == 1 {
    return addr
  }
  var hints = addrinfo()
  hints.ai_family = AF_INET
  hints.ai_socktype = SOCK_DGRAM
  var info: UnsafeMutablePointer<addrinfo>?
  let rc = getaddrinfo(host, String(port), &hints, &info)
  guard rc == 0, let info else { return nil }
  defer { freeaddrinfo(info) }
  guard let sa = info.pointee.ai_addr else { return nil }
  return sa.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { $0.pointee }
}
