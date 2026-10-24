import CoreMedia
import CryptoKit
import Foundation
import Network
import VideoToolbox

var listenPort: UInt16 = 47990
var macPort: UInt16 = 47991
var sayHello = true
let packetLimit = 1200
let dirToMac: UInt8 = 0
let dirToMobile: UInt8 = 1
let kindHello: UInt8 = 1
let kindVideo: UInt8 = 2
let kindReport: UInt8 = 3
let kindKeyframe: UInt8 = 5

let secret = Data("hive-desk-test-secret-32bytes!!".utf8)
let sessionNonce = Data("hive-desk-nonce!".utf8)

struct Keys {
    let toMobile: SymmetricKey
    let toMac: SymmetricKey
    static func test() -> Keys {
        let out = HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: secret), salt: sessionNonce, info: Data("hive-desk/v1".utf8), outputByteCount: 64)
        let raw = out.withUnsafeBytes { Data($0) }
        return Keys(toMobile: SymmetricKey(data: raw.prefix(32)), toMac: SymmetricKey(data: raw.suffix(32)))
    }
}

func nonce(_ dir: UInt8, _ counter: UInt64) -> AES.GCM.Nonce {
    var raw = Data(count: 12)
    raw[0] = dir
    var n = counter.bigEndian
    withUnsafeBytes(of: &n) { raw.replaceSubrange(4..<12, with: $0) }
    return try! AES.GCM.Nonce(data: raw)
}

func seal(_ key: SymmetricKey, _ dir: UInt8, _ kind: UInt8, _ counter: UInt64, _ plain: Data) throws -> Data {
    var packet = Data([kind])
    var n = counter.bigEndian
    withUnsafeBytes(of: &n) { packet.append(contentsOf: $0) }
    let box = try AES.GCM.seal(plain, using: key, nonce: nonce(dir, counter), authenticating: Data([kind]))
    packet.append(box.ciphertext)
    packet.append(box.tag)
    return packet
}

func openPacket(_ key: SymmetricKey, _ dir: UInt8, _ packet: Data) -> (UInt8, UInt64, Data)? {
    guard packet.count >= 1 + 8 + 16, packet.count <= packetLimit else { return nil }
    let kind = packet[0]
    let counter = packet.subdata(in: 1..<9).withUnsafeBytes { $0.load(as: UInt64.self).bigEndian }
    let body = packet.subdata(in: 9..<packet.count)
    guard body.count >= 16 else { return nil }
    let cipher = body.prefix(body.count - 16)
    let tag = body.suffix(16)
    guard let box = try? AES.GCM.SealedBox(nonce: nonce(dir, counter), ciphertext: cipher, tag: tag),
          let plain = try? AES.GCM.open(box, using: key, authenticating: Data([kind])) else { return nil }
    return (kind, counter, plain)
}

final class Replay {
    var last: UInt64 = 0
    var bits = [UInt64](repeating: 0, count: 16)
    func accept(_ n: UInt64) -> Bool {
        if n == 0 { return false }
        if last == 0 { last = n; bits[0] = 1; return true }
        if n > last {
            let shift = n - last
            if shift >= 1024 { bits = [UInt64](repeating: 0, count: 16) }
            else { self.shift(shift) }
            last = n
            bits[0] |= 1
            return true
        }
        let back = last - n
        if back >= 1024 { return false }
        let i = Int(back / 64), b = back % 64
        if bits[i] & (1 << b) != 0 { return false }
        bits[i] |= 1 << b
        return true
    }
    func shift(_ n: UInt64) {
        let words = Int(n / 64), bitsn = n % 64
        var next = [UInt64](repeating: 0, count: 16)
        for i in 0..<16 {
            let from = words + i
            if from >= 16 { break }
            next[i] = bits[from] << bitsn
            if bitsn != 0, from + 1 < 16 { next[i] |= bits[from + 1] >> (64 - bitsn) }
        }
        bits = next
    }
}

struct Assembled {
    var nals: [Data]
    var frame: UInt64
    var capture: Int64
    var key: Bool
    var pt: UInt8
}

final class Assembler {
    var frame: UInt64 = 0
    var capture: Int64 = 0
    var key = false
    var pt: UInt8 = 96
    var expect: UInt16 = 0
    var have = false
    var broken = false
    var abandonedIncomplete = false
    var nals: [Data] = []
    var fu = Data()
    func push(_ plain: Data) -> Assembled? {
        guard plain.count >= 17 + 12 else { return nil }
        let key = plain[0] & 1 != 0
        let number = plain.subdata(in: 1..<9).withUnsafeBytes { $0.load(as: UInt64.self).bigEndian }
        let capture = Int64(bitPattern: plain.subdata(in: 9..<17).withUnsafeBytes { $0.load(as: UInt64.self).bigEndian })
        let rtp = plain.subdata(in: 17..<plain.count)
        let seq = rtp.subdata(in: 2..<4).withUnsafeBytes { $0.load(as: UInt16.self).bigEndian }
        let marker = rtp[1] & 0x80 != 0
        let pt = rtp[1] & 0x7f
        let payload = rtp.subdata(in: 12..<rtp.count)
        abandonedIncomplete = false
        if !have || number != frame {
            // 上一帧还没收到 marker 就来了下一帧：发送端把没发完的丢掉了，序号是连续的。
            if have && number != frame {
                abandonedIncomplete = true
            }
            self.frame = number
            self.capture = capture
            self.key = key
            self.pt = pt
            expect = seq
            have = true
            broken = false
            nals = []
            fu = Data()
        } else if seq != expect {
            // 序号缺了这一帧就不要了，后面的片也不拼成残帧。
            broken = true
            fu = Data(); nals = []; expect = seq
            return nil
        }
        expect = expect &+ 1
        self.pt = pt
        if pt == 97 {
            takeH264(payload)
        } else if payload.count >= 3, ((payload[0] >> 1) & 0x3f) == 49 {
            let piece = payload.subdata(in: 3..<payload.count)
            if payload[2] & 0x80 != 0 { fu = piece } else { fu.append(piece) }
            if payload[2] & 0x40 != 0 {
                var nal = Data([payload[0], payload[1]])
                nal.append(fu)
                nal[0] = (payload[0] & 0x81) | ((payload[2] & 0x3f) << 1)
                nals.append(nal)
                fu = Data()
            }
        } else if !payload.isEmpty {
            nals.append(payload)
        }
        guard marker else { return nil }
        have = false
        if broken || !fu.isEmpty {
            broken = false
            fu = Data()
            nals = []
            return nil
        }
        return Assembled(nals: nals.filter { !$0.isEmpty }, frame: frame, capture: capture, key: self.key, pt: pt)
    }

    func takeH264(_ payload: Data) {
        if payload.count >= 2, payload[0] & 0x1f == 28 {
            let piece = payload.subdata(in: 2..<payload.count)
            if payload[1] & 0x80 != 0 {
                fu = Data([(payload[0] & 0xe0) | (payload[1] & 0x1f)])
                fu.append(piece)
            } else if !fu.isEmpty {
                fu.append(piece)
            }
            if payload[1] & 0x40 != 0, !fu.isEmpty {
                nals.append(fu)
                fu = Data()
            }
        } else if !payload.isEmpty {
            nals.append(payload)
        }
    }
}

final class Decoder {
    var session: VTDecompressionSession?
    var format: CMVideoFormatDescription?
    var times: [Double] = []
    var decoded = 0
    var shown = 0
    var decodeErr: OSStatus = 0
    func push(nals: [Data], pt: UInt8, started: CFAbsoluteTime) {
        let h264 = pt == 97
        let sets: [Data]
        let slices: [Data]
        if h264 {
            let sps = nals.filter { h264Type($0) == 7 }
            let pps = nals.filter { h264Type($0) == 8 }
            sets = sps + pps
            slices = nals.filter { let t = h264Type($0); return t != 7 && t != 8 && t != 6 && t != 9 }
        } else {
            sets = nals.filter { nalType($0) == 32 || nalType($0) == 33 || nalType($0) == 34 }
            slices = nals.filter { let t = nalType($0); return t != 32 && t != 33 && t != 34 && t != 35 }
        }
        if format == nil, h264 ? (sets.contains { h264Type($0) == 7 } && sets.contains { h264Type($0) == 8 }) : sets.count >= 3 {
            format = h264 ? makeFormatH264(sets) : makeFormat(sets)
            if let format {
                print(h264 ? "deskrecv: format h264" : "deskrecv: format hevc")
                var callback = VTDecompressionOutputCallbackRecord(decompressionOutputCallback: decodeCallback, decompressionOutputRefCon: Unmanaged.passUnretained(self).toOpaque())
                VTDecompressionSessionCreate(allocator: nil, formatDescription: format, decoderSpecification: nil, imageBufferAttributes: nil, outputCallback: &callback, decompressionSessionOut: &session)
            }
        }
        activeFormat = format
        guard let session, !slices.isEmpty else { return }
        guard let sample = sampleBuffer(slices) else { return }
        let t0 = CFAbsoluteTimeGetCurrent()
        let status = VTDecompressionSessionDecodeFrame(session, sampleBuffer: sample, flags: [._EnableAsynchronousDecompression], frameRefcon: nil, infoFlagsOut: nil)
        times.append((CFAbsoluteTimeGetCurrent() - t0) * 1000)
        if status == noErr { decoded += 1 } else if decodeErr == 0 { decodeErr = status }
        _ = started
    }
    func summary() -> String {
        let sorted = times.sorted()
        let p50 = sorted.isEmpty ? 0 : sorted[sorted.count / 2]
        let p95 = sorted.isEmpty ? 0 : sorted[min(sorted.count * 95 / 100, sorted.count - 1)]
        return String(format: "decoded=%d shown=%d decode-call p50=%.2fms p95=%.2fms err=%d", decoded, shown, p50, p95, decodeErr)
    }
}

func nalType(_ nal: Data) -> UInt8 { nal.isEmpty ? 0 : (nal[0] >> 1) & 0x3f }
func h264Type(_ nal: Data) -> UInt8 { nal.isEmpty ? 0 : nal[0] & 0x1f }

func makeFormat(_ sets: [Data]) -> CMVideoFormatDescription? {
    let copies: [(UnsafeMutablePointer<UInt8>, Int)] = sets.map { data in
        let p = UnsafeMutablePointer<UInt8>.allocate(capacity: data.count)
        data.copyBytes(to: p, count: data.count)
        return (p, data.count)
    }
    defer { copies.forEach { $0.0.deallocate() } }
    var pointers = copies.map { UnsafePointer($0.0) }
    var sizes = copies.map { $0.1 }
    var format: CMVideoFormatDescription?
    let status = CMVideoFormatDescriptionCreateFromHEVCParameterSets(allocator: nil, parameterSetCount: pointers.count, parameterSetPointers: &pointers, parameterSetSizes: &sizes, nalUnitHeaderLength: 4, extensions: nil, formatDescriptionOut: &format)
    return status == noErr ? format : nil
}

func makeFormatH264(_ sets: [Data]) -> CMVideoFormatDescription? {
    let copies: [(UnsafeMutablePointer<UInt8>, Int)] = sets.map { data in
        let p = UnsafeMutablePointer<UInt8>.allocate(capacity: data.count)
        data.copyBytes(to: p, count: data.count)
        return (p, data.count)
    }
    defer { copies.forEach { $0.0.deallocate() } }
    var pointers = copies.map { UnsafePointer($0.0) }
    var sizes = copies.map { $0.1 }
    var format: CMVideoFormatDescription?
    let status = CMVideoFormatDescriptionCreateFromH264ParameterSets(allocator: nil, parameterSetCount: pointers.count, parameterSetPointers: &pointers, parameterSetSizes: &sizes, nalUnitHeaderLength: 4, formatDescriptionOut: &format)
    if status != noErr { print("deskrecv: h264 format \(status)") }
    return status == noErr ? format : nil
}

func sampleBuffer(_ nals: [Data]) -> CMSampleBuffer? {
    var avcc = Data()
    for nal in nals {
        var len = UInt32(nal.count).bigEndian
        withUnsafeBytes(of: &len) { avcc.append(contentsOf: $0) }
        avcc.append(nal)
    }
    var block: CMBlockBuffer?
    guard CMBlockBufferCreateWithMemoryBlock(allocator: nil, memoryBlock: nil, blockLength: avcc.count, blockAllocator: nil, customBlockSource: nil, offsetToData: 0, dataLength: avcc.count, flags: 0, blockBufferOut: &block) == noErr, let block else { return nil }
    _ = avcc.withUnsafeBytes { CMBlockBufferReplaceDataBytes(with: $0.baseAddress!, blockBuffer: block, offsetIntoDestination: 0, dataLength: avcc.count) }
    var sample: CMSampleBuffer?
    var sizes = [avcc.count]
    var timing = CMSampleTimingInfo(duration: .invalid, presentationTimeStamp: .zero, decodeTimeStamp: .invalid)
    guard let format = activeFormat, CMSampleBufferCreateReady(allocator: nil, dataBuffer: block, formatDescription: format, sampleCount: 1, sampleTimingEntryCount: 1, sampleTimingArray: &timing, sampleSizeEntryCount: 1, sampleSizeArray: &sizes, sampleBufferOut: &sample) == noErr else { return nil }
    return sample
}

var activeFormat: CMVideoFormatDescription?
private func decodeCallback(_ refcon: UnsafeMutableRawPointer?, _ source: UnsafeMutableRawPointer?, _ status: OSStatus, _ flags: VTDecodeInfoFlags, _ image: CVImageBuffer?, _ pts: CMTime, _ duration: CMTime) {
    guard status == noErr, image != nil, let refcon else { return }
    Unmanaged<Decoder>.fromOpaque(refcon).takeUnretainedValue().shown += 1
}

final class Receiver {
    let keys = Keys.test()
    let replay = Replay()
    let asm = Assembler()
    let decoder = Decoder()
    let sendLock = NSLock()
    let statsLock = NSLock()
    var sock: Int32 = -1
    var mac: sockaddr_in = sockaddr_in()
    var sendN: UInt64 = 0
    var expect: UInt16 = 0
    var haveSeq = false
    var loss: UInt32 = 0
    var packets: UInt32 = 0
    var gapsUs: [UInt32] = []
    var lastArrival: CFAbsoluteTime = 0
    var lastFrameAt: CFAbsoluteTime = 0
    var waitingKey = false
    var lossAt: CFAbsoluteTime = 0
    var lastKeyReq: CFAbsoluteTime = 0
    var recoveries: [Double] = []
    var shownAfterLoss = 0
    var reportsOn = true
    var payloadType: UInt8 = 96

    func run() throws {
        sock = socket(AF_INET, SOCK_DGRAM, 0)
        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = listenPort.bigEndian
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        let bindRC = withUnsafePointer(to: &addr) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(sock, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        if bindRC != 0 { throw NSError(domain: "deskrecv", code: Int(errno)) }
        var rcv = Int32(4 << 20)
        _ = setsockopt(sock, SOL_SOCKET, SO_RCVBUF, &rcv, socklen_t(MemoryLayout<Int32>.size))
        mac.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        mac.sin_family = sa_family_t(AF_INET)
        mac.sin_port = macPort.bigEndian
        mac.sin_addr.s_addr = inet_addr("127.0.0.1")
        var gotVideo = false
        let hello = DispatchSource.makeTimerSource()
        hello.schedule(deadline: .now(), repeating: 0.1)
        hello.setEventHandler { [weak self] in
            if !sayHello || gotVideo { return }
            try? self?.send(kindHello, Data())
        }
        hello.resume()
        print("deskrecv listening \(listenPort), hello -> \(macPort)")
        let report = DispatchSource.makeTimerSource()
        report.schedule(deadline: .now() + 0.1, repeating: 0.1)
        report.setEventHandler { [weak self] in self?.sendReport() }
        report.resume()
        if stopAfter > 0 {
            DispatchQueue.global().asyncAfter(deadline: .now() + stopAfter) { [weak self] in
                guard let self else { return }
                self.statsLock.lock()
                self.reportsOn = false
                self.statsLock.unlock()
                print(String(format: "deskrecv: stopped reports after %.1fs", stopAfter))
            }
        }
        var buf = [UInt8](repeating: 0, count: packetLimit)
        while true {
            let n = recv(sock, &buf, buf.count, 0)
            if n <= 0 { continue }
            guard let opened = openPacket(keys.toMobile, dirToMobile, Data(buf.prefix(n))) else { continue }
            if !replay.accept(opened.1) { continue }
            if opened.0 != kindVideo { continue }
            gotVideo = true
            let now = CFAbsoluteTimeGetCurrent()
            let seq = opened.2.subdata(in: 19..<21).withUnsafeBytes { $0.load(as: UInt16.self).bigEndian }
            var latePacket = false
            statsLock.lock()
            packets += 1
            if !haveSeq {
                expect = seq &+ 1
                haveSeq = true
            } else if seq != expect {
                let ahead = seq &- expect
                // 延迟整形会让后发的包先到。回绕过来的旧包不算成几万个丢包。
                if ahead > 0 && ahead < 32768 {
                    loss += UInt32(ahead)
                    if !waitingKey {
                        waitingKey = true
                        lossAt = now
                    }
                    expect = seq &+ 1
                } else {
                    latePacket = true
                }
            } else {
                expect &+= 1
            }
            statsLock.unlock()
            if latePacket { continue }
            let frame = asm.push(opened.2)
            if asm.abandonedIncomplete && !waitingKey {
                waitingKey = true
                lossAt = now
            }
            if waitingKey && now - lastKeyReq > 0.2 {
                lastKeyReq = now
                try? send(kindKeyframe, Data())
            }
            if let frame {
                statsLock.lock()
                if lastFrameAt != 0 {
                    gapsUs.append(UInt32((now - lastFrameAt) * 1_000_000))
                }
                lastFrameAt = now
                statsLock.unlock()
                let nals = frame.nals
                let pt = frame.pt
                payloadType = pt
                let idr = pt == 97
                    ? nals.contains { h264Type($0) == 5 }
                    : nals.contains { let t = nalType($0); return (16...21).contains(t) }
                if waitingKey {
                    if frame.key || idr {
                        recoveries.append((now - lossAt) * 1000)
                        waitingKey = false
                    } else {
                        shownAfterLoss += 1
                        continue
                    }
                }
                decoder.push(nals: nals, pt: pt, started: now)
            }
        }
    }

    func sendReport() {
        statsLock.lock()
        if !reportsOn {
            statsLock.unlock()
            return
        }
        let loss = self.loss
        let packets = self.packets
        let highest = self.expect
        let gap = medianGap()
        self.loss = 0
        self.packets = 0
        gapsUs.removeAll(keepingCapacity: true)
        statsLock.unlock()
        var raw = Data()
        func put64(_ v: UInt64) { var x = v.bigEndian; withUnsafeBytes(of: &x) { raw.append(contentsOf: $0) } }
        func put32(_ v: UInt32) { var x = v.bigEndian; withUnsafeBytes(of: &x) { raw.append(contentsOf: $0) } }
        put64(UInt64(highest))
        put32(loss)
        put32(packets)
        put64(0)
        put64(0)
        put32(gap)
        try? send(kindReport, raw)
    }

    func medianGap() -> UInt32 {
        guard !gapsUs.isEmpty else { return 0 }
        let sorted = gapsUs.sorted()
        return sorted[sorted.count / 2]
    }

    func send(_ kind: UInt8, _ plain: Data) throws {
        sendLock.lock()
        defer { sendLock.unlock() }
        sendN += 1
        let packet = try seal(keys.toMac, dirToMac, kind, sendN, plain)
        _ = packet.withUnsafeBytes { raw in
            withUnsafePointer(to: &mac) { ptr in
                ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sendto(sock, raw.baseAddress, raw.count, 0, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
            }
        }
    }

    func finish() {
        if let session = decoder.session {
            VTDecompressionSessionWaitForAsynchronousFrames(session)
        }
        let rec = recoveries.sorted()
        let p50 = rec.isEmpty ? 0 : rec[rec.count / 2]
        print(decoder.summary())
        print(String(format: "recoveries=%d recovery-p50=%.1fms nonkey-after-loss=%d pt=%d", recoveries.count, p50, shownAfterLoss, payloadType))
    }
}

let receiver = Receiver()
var stopAfter: Double = 0
var nums: [UInt16] = []
var args = Array(CommandLine.arguments.dropFirst())
var ai = 0
while ai < args.count {
    let arg = args[ai]
    if arg == "--no-hello" {
        sayHello = false
    } else if arg == "--stop-after", ai + 1 < args.count {
        ai += 1
        stopAfter = Double(args[ai]) ?? 0
    } else if arg.hasPrefix("--stop-after=") {
        stopAfter = Double(arg.dropFirst("--stop-after=".count)) ?? 0
    } else if let p = UInt16(arg) {
        nums.append(p)
    }
    ai += 1
}
if nums.count >= 1 { listenPort = nums[0] }
if nums.count >= 2 { macPort = nums[1] }
signal(SIGINT) { _ in receiver.finish(); exit(0) }
signal(SIGTERM) { _ in receiver.finish(); exit(0) }
do { try receiver.run() } catch { print("deskrecv: \(error)"); exit(1) }
