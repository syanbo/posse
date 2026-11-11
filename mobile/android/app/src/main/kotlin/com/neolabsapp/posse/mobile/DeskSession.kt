package com.neolabsapp.posse.mobile

import android.media.MediaCodec
import android.media.MediaCodecList
import android.media.MediaFormat
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.view.Surface
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.Inet4Address
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.SocketTimeoutException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.atomic.AtomicBoolean
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

// 包格式、序号处理和统计口径都照 ios/Runner/DeskSession.swift，两边改动要同步。

private const val PACKET_LIMIT = 1200
private const val DIR_TO_MAC: Byte = 0
private const val DIR_TO_MOBILE: Byte = 1
private const val KIND_HELLO = 1
private const val KIND_VIDEO = 2
private const val KIND_REPORT = 3
private const val KIND_INPUT = 4
private const val KIND_KEYFRAME = 5
private const val KIND_BEAT = 6
private const val KIND_NACK = 7
/// 一个补发请求最多点这么多个序号（u16），远小于包长上限
private const val NACK_MAX = 64
/// 一次缺这么多以上不点名补发，当成断档直接要关键帧
private const val NACK_WINDOW = 512
const val DESK_RELAY_TOKEN_LEN = 16

class DeskException(val code: String, message: String) : Exception(message)

private fun now(): Double = System.nanoTime() / 1e9

private fun beU16(b: ByteArray, off: Int): Int =
    ((b[off].toInt() and 0xff) shl 8) or (b[off + 1].toInt() and 0xff)

private fun beU64(b: ByteArray, off: Int): Long {
    var v = 0L
    for (i in 0 until 8) v = (v shl 8) or (b[off + i].toLong() and 0xff)
    return v
}

private fun nonce(dir: Byte, counter: Long): ByteArray {
    val n = ByteArray(12)
    n[0] = dir
    for (i in 0 until 8) n[4 + i] = (counter ushr (56 - 8 * i)).toByte()
    return n
}

private fun sameAddr(a: InetSocketAddress, b: InetSocketAddress): Boolean =
    a.port == b.port && a.address == b.address

/// "host:port" 形式的地址（中转、打洞）。会查 DNS，不能在主线程调。
fun deskResolveHostPort(text: String): InetSocketAddress? {
    val colon = text.lastIndexOf(':')
    if (colon <= 0) return null
    val port = text.substring(colon + 1).toIntOrNull() ?: return null
    return deskResolve(text.substring(0, colon), port)
}

/// 只要 IPv4：Mac 那头的 UDP 只开在 IPv4 上。
fun deskResolve(host: String, port: Int): InetSocketAddress? {
    if (port <= 0 || port >= 65536) return null
    return try {
        val addr = InetAddress.getAllByName(host).firstOrNull { it is Inet4Address } ?: return null
        InetSocketAddress(addr, port)
    } catch (e: Exception) {
        null
    }
}

/// 1024 位滑动窗口，和 WireGuard、deskrecv 一样：乱序照收，重复的和落在窗口前的丢掉。
private class DeskReplay {
    private var last = 0L
    private var bits = LongArray(16)

    fun reset() {
        last = 0
        bits = LongArray(16)
    }

    fun accept(n: Long): Boolean {
        if (n == 0L) return false
        if (last == 0L) {
            last = n
            bits[0] = 1
            return true
        }
        if (java.lang.Long.compareUnsigned(n, last) > 0) {
            val shift = n - last
            if (java.lang.Long.compareUnsigned(shift, 1024) >= 0) bits = LongArray(16) else shift(shift.toInt())
            last = n
            bits[0] = bits[0] or 1
            return true
        }
        val back = last - n
        if (java.lang.Long.compareUnsigned(back, 1024) >= 0) return false
        val index = (back / 64).toInt()
        val bit = (back % 64).toInt()
        if (bits[index] and (1L shl bit) != 0L) return false
        bits[index] = bits[index] or (1L shl bit)
        return true
    }

    private fun shift(n: Int) {
        val words = n / 64
        val b = n % 64
        val next = LongArray(16)
        // 位号是离 last 的距离：last 前进 n，每一位往高处挪 n
        for (i in words until 16) {
            val from = i - words
            next[i] = bits[from] shl b
            if (b != 0 && from > 0) next[i] = next[i] or (bits[from - 1] ushr (64 - b))
        }
        bits = next
    }
}

/// 码率照 Moonlight：按 250 ms 分桶记字节，平均只取最近 2 秒里已经满了的桶，峰值看 10 秒。
private class DeskRate {
    private val bucket = 0.25
    private val count = 40
    private val index = LongArray(count) { -1 }
    private val bytes = LongArray(count)

    fun add(n: Int, at: Double) {
        val i = (at / bucket).toLong()
        val slot = (i % count).toInt()
        if (index[slot] != i) {
            index[slot] = i
            bytes[slot] = 0
        }
        bytes[slot] += n.toLong()
    }

    fun average(at: Double): Double {
        val current = (at / bucket).toLong()
        val span = (2 / bucket).toLong()
        var sum = 0L
        for (i in (current - span) until current) {
            val slot = (i % count).toInt()
            if (index[slot] == i) sum += bytes[slot]
        }
        return sum * 8.0 / 2
    }

    fun peak(at: Double): Double {
        val current = (at / bucket).toLong()
        var most = 0L
        for (s in 0 until count) {
            if (index[s] in 0 until current && current - index[s] <= count) most = maxOf(most, bytes[s])
        }
        return most * 8.0 / bucket
    }

    fun reset() {
        index.fill(-1)
        bytes.fill(0)
    }
}

private class DeskShown(
    val fps: Double = -1.0,
    val received: Double = 0.0,
    val bitrate: Double = 0.0,
    val peak: Double = 0.0,
    val still: Boolean = false,
    val decodeMs: Double = 0.0,
)

private class AccessUnit(val nals: List<ByteArray>, val frame: Long, val key: Boolean)

private fun nalType(nal: ByteArray): Int = if (nal.isEmpty()) 0 else (nal[0].toInt() shr 1) and 0x3f

/// HEVC 的 RTP 拆包（RFC 7798，只有单 NAL 和 FU）。一帧里序号断了就整帧丢掉。
private class HevcDepacketizer {
    private var frame = 0L
    private var key = false
    private var expect = 0
    private var have = false
    private var broken = false
    private var nals = ArrayList<ByteArray>()
    private var fu: java.io.ByteArrayOutputStream? = null

    fun reset() {
        have = false
        broken = false
        nals = ArrayList()
        fu = null
    }

    fun push(plain: ByteArray): AccessUnit? {
        if (plain.size < 29) return null
        val key = plain[0].toInt() and 1 != 0
        val number = beU64(plain, 1)
        val seq = beU16(plain, 19)
        val marker = plain[18].toInt() and 0x80 != 0
        if (!have || number != frame) {
            frame = number
            this.key = key
            expect = seq
            have = true
            broken = false
            nals = ArrayList()
            fu = null
        } else if (seq != expect) {
            broken = true
            fu = null
            nals = ArrayList()
            expect = seq
            return null
        }
        expect = (expect + 1) and 0xffff
        take(plain, 29)
        if (!marker) return null
        have = false
        if (broken || fu != null) return null
        return AccessUnit(nals.filter { it.isNotEmpty() }, frame, this.key)
    }

    private fun take(p: ByteArray, off: Int) {
        val len = p.size - off
        if (len >= 3 && ((p[off].toInt() shr 1) and 0x3f) == 49) {
            val h = p[off + 2].toInt()
            val start = h and 0x80 != 0
            val end = h and 0x40 != 0
            if (start) {
                fu = java.io.ByteArrayOutputStream(len * 4)
            } else if (fu == null) {
                // 这一帧的开头已经丢了，拼出来也是花的。
                broken = true
                return
            }
            fu!!.write(p, off + 3, len - 3)
            if (end && !broken) {
                val body = fu!!.toByteArray()
                val nal = ByteArray(body.size + 2)
                nal[0] = ((p[off].toInt() and 0x81) or ((h and 0x3f) shl 1)).toByte()
                nal[1] = p[off + 1]
                System.arraycopy(body, 0, nal, 2, body.size)
                nals.add(nal)
                fu = null
            }
            return
        }
        if (len > 0) nals.add(p.copyOfRange(off, p.size))
    }
}

/// SPS 里的画面宽高，配置解码器用。读不出来时给个常见值，硬解会按码流自己改。
private fun spsSize(sps: ByteArray): Pair<Int, Int> {
    try {
        // 去掉防竞争字节 00 00 03
        val raw = java.io.ByteArrayOutputStream(sps.size)
        var zeros = 0
        for (i in 2 until sps.size) {
            val b = sps[i].toInt() and 0xff
            if (zeros >= 2 && b == 3) {
                zeros = 0
                continue
            }
            zeros = if (b == 0) zeros + 1 else 0
            raw.write(b)
        }
        val r = BitReader(raw.toByteArray())
        r.skip(4)
        val maxSub = r.bits(3)
        r.skip(1)
        r.skip(88 + 8)
        val profilePresent = BooleanArray(maxSub)
        val levelPresent = BooleanArray(maxSub)
        for (i in 0 until maxSub) {
            profilePresent[i] = r.bits(1) == 1
            levelPresent[i] = r.bits(1) == 1
        }
        if (maxSub > 0) for (i in maxSub until 8) r.skip(2)
        for (i in 0 until maxSub) {
            if (profilePresent[i]) r.skip(88)
            if (levelPresent[i]) r.skip(8)
        }
        r.ue()
        if (r.ue() == 3) r.skip(1)
        val w = r.ue()
        val h = r.ue()
        if (w in 16..8192 && h in 16..8192) return w to h
    } catch (e: Exception) {
        // 读不出来走默认值
    }
    return 1920 to 1080
}

private class BitReader(private val b: ByteArray) {
    private var pos = 0

    fun bits(n: Int): Int {
        var v = 0
        repeat(n) {
            val byte = b[pos shr 3].toInt() and 0xff
            v = (v shl 1) or ((byte shr (7 - (pos and 7))) and 1)
            pos++
        }
        return v
    }

    fun skip(n: Int) {
        pos += n
        if (pos > b.size * 8) throw IndexOutOfBoundsException()
    }

    fun ue(): Int {
        var zeros = 0
        while (bits(1) == 0) {
            zeros++
            if (zeros > 31) throw IllegalStateException()
        }
        return (1 shl zeros) - 1 + bits(zeros)
    }
}

/// MediaCodec 硬解，异步模式跑在自己的线程上；解出来的帧直接渲染到给它的 Surface（Flutter 纹理或平台视图）。
/// onBroken 的参数表示这个解码器一帧都没出就坏了：多半是它不认这种 Surface。
private class DeskDecoder(
    private val onImage: (width: Int, height: Int, ms: Double) -> Unit,
    private val onBroken: (unrendered: Boolean) -> Unit,
) {
    private val thread = HandlerThread("posse.desk.decode").apply { start() }
    private val handler = Handler(thread.looper)
    private var codec: MediaCodec? = null
    private var formatKey: List<ByteArray>? = null
    private var surface: Surface? = null
    private val pending = ArrayDeque<AccessUnit>()
    private val free = ArrayDeque<Int>()
    private val started = HashMap<Long, Double>()
    private var pts = 0L
    private var width = 0
    private var height = 0
    private var rendered = false

    /// 换了 Surface（App 回到前台后纹理重建）要重建解码器，等下一张关键帧带参数集来。
    fun setSurface(value: Surface?) {
        handler.post {
            surface = value
            release()
        }
    }

    fun push(unit: AccessUnit) {
        handler.post {
            val sets = parameterSets(unit.nals)
            if (sets != null && (codec == null || !sameSets(sets, formatKey))) {
                release()
                formatKey = sets
                configure(sets)
            }
            if (codec == null) return@post
            // 解码跟不上时堆着的帧不能丢（后面的 P 帧要参考它们），只防止无限涨
            if (pending.size > 120) {
                release()
                onBroken(false)
                return@post
            }
            pending.addLast(unit)
            feed()
        }
    }

    fun shutdown() {
        val done = CountDownLatch(1)
        handler.post {
            release()
            done.countDown()
        }
        done.await()
        thread.quitSafely()
    }

    private fun configure(sets: List<ByteArray>) {
        val target = surface ?: return
        val (w, h) = spsSize(sets[1])
        val name = MediaCodecList(MediaCodecList.REGULAR_CODECS).findDecoderForFormat(
            MediaFormat.createVideoFormat(MediaFormat.MIMETYPE_VIDEO_HEVC, w, h),
        )
        // 先带低延迟的键试，个别解码器不认其中的键、配置失败时退回最朴素的设置。
        // 海思不认 KEY_LOW_LATENCY（configure 报 0x80001001），直接用朴素的
        val hisi = name != null && (name.startsWith("OMX.hisi") || name.startsWith("c2.hisi"))
        for (tuned in if (hisi) listOf(false) else listOf(true, false)) {
            val created = try {
                if (name != null) MediaCodec.createByCodecName(name) else MediaCodec.createDecoderByType(MediaFormat.MIMETYPE_VIDEO_HEVC)
            } catch (e: Exception) {
                return
            }
            created.setCallback(callback(), handler)
            try {
                created.configure(format(sets, w, h, if (tuned) name ?: "" else null), target, null, 0)
                created.start()
            } catch (e: Exception) {
                try {
                    created.release()
                } catch (_: Exception) {
                }
                continue
            }
            width = w
            height = h
            rendered = false
            codec = created
            return
        }
        onBroken(true)
    }

    /// tuned 是解码器名，为空时不加低延迟的键。
    private fun format(sets: List<ByteArray>, w: Int, h: Int, tuned: String?): MediaFormat {
        val format = MediaFormat.createVideoFormat(MediaFormat.MIMETYPE_VIDEO_HEVC, w, h)
        val csd = java.io.ByteArrayOutputStream()
        for (s in sets) {
            csd.write(byteArrayOf(0, 0, 0, 1))
            csd.write(s)
        }
        format.setByteBuffer("csd-0", java.nio.ByteBuffer.wrap(csd.toByteArray()))
        // 关键帧可能比按分辨率估的默认输入缓冲大
        format.setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, maxOf(w * h, 1 shl 20))
        if (tuned == null) return format
        // 要的是延迟最低：低延迟模式、实时优先级，高通另有自己的低延迟开关（Moonlight 也这样设）。
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) format.setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
        format.setInteger(MediaFormat.KEY_PRIORITY, 0)
        if (tuned.startsWith("c2.qti") || tuned.startsWith("OMX.qcom")) {
            format.setInteger("vendor.qti-ext-dec-low-latency.enable", 1)
            format.setInteger("vendor.qti-ext-dec-picture-order.enable", 1)
        }
        return format
    }

    private fun callback() = object : MediaCodec.Callback() {
        override fun onInputBufferAvailable(c: MediaCodec, index: Int) {
            if (c !== codec) return
            free.addLast(index)
            feed()
        }

        override fun onOutputBufferAvailable(c: MediaCodec, index: Int, info: MediaCodec.BufferInfo) {
            if (c !== codec) return
            val start = started.remove(info.presentationTimeUs)
            try {
                c.releaseOutputBuffer(index, info.size > 0)
            } catch (e: IllegalStateException) {
                return
            }
            if (info.size > 0) {
                rendered = true
                val ms = if (start != null) (now() - start) * 1000 else 0.0
                onImage(width, height, ms)
            }
        }

        override fun onError(c: MediaCodec, e: MediaCodec.CodecException) {
            if (c !== codec) return
            val unrendered = !rendered
            release()
            onBroken(unrendered)
        }

        override fun onOutputFormatChanged(c: MediaCodec, f: MediaFormat) {
            if (c !== codec) return
            var fw = f.getInteger(MediaFormat.KEY_WIDTH)
            var fh = f.getInteger(MediaFormat.KEY_HEIGHT)
            if (f.containsKey("crop-right") && f.containsKey("crop-left")) {
                fw = f.getInteger("crop-right") - f.getInteger("crop-left") + 1
            }
            if (f.containsKey("crop-bottom") && f.containsKey("crop-top")) {
                fh = f.getInteger("crop-bottom") - f.getInteger("crop-top") + 1
            }
            width = fw
            height = fh
        }
    }

    private fun feed() {
        val c = codec ?: return
        while (pending.isNotEmpty() && free.isNotEmpty()) {
            val unit = pending.removeFirst()
            val index = free.removeFirst()
            val slices = unit.nals.filter { nalType(it) !in 32..35 }
            if (slices.isEmpty()) {
                free.addFirst(index)
                continue
            }
            try {
                val buf = c.getInputBuffer(index) ?: continue
                buf.clear()
                var size = 0
                for (nal in slices) {
                    buf.put(byteArrayOf(0, 0, 0, 1))
                    buf.put(nal)
                    size += 4 + nal.size
                }
                val t = pts++
                started[t] = now()
                if (started.size > 64) started.keys.minOrNull()?.let { started.remove(it) }
                c.queueInputBuffer(index, 0, size, t, 0)
            } catch (e: Exception) {
                // 缓冲不够大或解码器已经坏了：重建，等关键帧
                release()
                onBroken(false)
                return
            }
        }
    }

    private fun release() {
        val c = codec ?: run {
            formatKey = null
            return
        }
        codec = null
        formatKey = null
        pending.clear()
        free.clear()
        started.clear()
        try {
            c.stop()
        } catch (_: Exception) {
        }
        try {
            c.release()
        } catch (_: Exception) {
        }
    }

    private fun parameterSets(nals: List<ByteArray>): List<ByteArray>? {
        var vps: ByteArray? = null
        var sps: ByteArray? = null
        var pps: ByteArray? = null
        for (nal in nals) {
            when (nalType(nal)) {
                32 -> if (vps == null) vps = nal
                33 -> if (sps == null) sps = nal
                34 -> if (pps == null) pps = nal
            }
        }
        if (vps == null || sps == null || pps == null) return null
        return listOf(vps, sps, pps)
    }

    private fun sameSets(a: List<ByteArray>, b: List<ByteArray>?): Boolean =
        b != null && a.size == b.size && a.indices.all { a[it].contentEquals(b[it]) }
}

/// 解 HEVC 用哪个解码器，没有时为空。
fun deskHevcDecoder(): String? =
    MediaCodecList(MediaCodecList.REGULAR_CODECS)
        .findDecoderForFormat(MediaFormat.createVideoFormat(MediaFormat.MIMETYPE_VIDEO_HEVC, 1920, 1080))

/// 机器能不能硬解 HEVC。不能时不去订阅，免得 Mac 白推流。
fun deskCanDecodeHevc(): Boolean =
    MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos.any { info ->
        !info.isEncoder && info.supportedTypes.any { it.equals(MediaFormat.MIMETYPE_VIDEO_HEVC, true) }
    }

private class Miss(val found: Double) {
    var firstAsk = 0.0
    var lastAsk = 0.0
    var asked = 0
}

/// 专门的线程收 UDP。视频包在这里解密、拆 RTP、交给硬解，不经 Dart。
class DeskSession(
    toMobile: ByteArray,
    toMac: ByteArray,
    peers: List<InetSocketAddress>,
    relay: InetSocketAddress?,
    relayToken: ByteArray?,
    private val nack: Boolean,
    startInView: Boolean,
) {
    /// 画面画在平台视图（SurfaceView）上，不画在 Flutter 纹理上：海思解码器在纹理的 Surface 上配不出输出缓冲区
    /// （视频播放也是这样，见 lib/files/video.dart）。开始时按解码器名定，纹理上一帧没出解码器就坏了也换过来，随下一次统计报给 Dart
    @Volatile var view = startInView
        private set

    @Volatile var onStats: ((Map<String, Any>) -> Unit)? = null

    private val keyToMobile = SecretKeySpec(toMobile, "AES")
    private val keyToMac = SecretKeySpec(toMac, "AES")
    private val openCipher = Cipher.getInstance("AES/GCM/NoPadding")
    private val sealCipher = Cipher.getInstance("AES/GCM/NoPadding")
    /// 直连候选（局域网等）和中转，hello 发给全部；收到 Mac 的包后只回给那条路。
    private val candidates: List<InetSocketAddress>
    private val relay: InetSocketAddress?
    private val relayToken: ByteArray
    private val ioLock = Object()
    private val statsLock = Object()
    private val flagLock = Object()
    /// Mac 最近一次发包来的地址，ioLock 保护
    private var active: InetSocketAddress? = null
    private var activeAt = 0.0
    private var socket: DatagramSocket? = null
    private var punchPeer: InetSocketAddress? = null
    private var punchUntil = 0.0
    private var sendN = 0L

    private val depacketizer = HevcDepacketizer()
    private val replay = DeskReplay()
    private val decoderBroken = AtomicBoolean(false)
    private val decoder: DeskDecoder = DeskDecoder(::noteDecoded) { unrendered ->
        if (unrendered && !view) {
            view = true
            // 不再往纹理上配；等平台视图建好交来它的 Surface
            this.decoder.setSurface(null)
        }
        decoderBroken.set(true)
    }
    /// 输入从主线程来，安卓不让主线程收发网络（会抛 NetworkOnMainThreadException），按序交给这条线程发
    private val sender = java.util.concurrent.Executors.newSingleThreadExecutor()
    private var thread: Thread? = null
    private val joined = CountDownLatch(1)

    private var stopped = false
    private var paused = false
    private var needReset = false

    private var expect = 0
    private var haveSeq = false
    private val held = HashMap<Int, Pair<ByteArray, Double>>()
    private var highest = 0
    private val missing = HashMap<Int, Miss>()
    private var rtt = 0.15
    private var fillFrame: Long? = null
    private var nextKeyNow = false
    private var lossInterval = 0L
    private var lossTotal = 0L
    private var packetsInterval = 0L
    private val gapsUs = ArrayList<Long>()
    private var lastFrameAt = 0.0
    private val delays = ArrayList<Double>()
    private val delayFloor = ArrayList<Pair<Long, Double>>()
    private var waitingKey = true
    private var heardAt = 0.0
    private var primed = false
    private var recoveries = 0
    private var gotVideo = false
    private val videoRate = DeskRate()
    private val receivedAt = ArrayList<Double>()
    private val decodedAt = ArrayList<Double>()
    private val decodeMs = ArrayList<Pair<Double, Double>>()
    private var shownAt = 0.0
    private var shown = DeskShown()
    private var pictureWidth = 0
    private var pictureHeight = 0
    private var inputAck = 0L
    private var publicAddr: String? = null
    private var probes = 0

    init {
        require(toMobile.size == 32 && toMac.size == 32)
        require(peers.isNotEmpty() || relay != null)
        if (relay != null && relayToken != null && relayToken.size == DESK_RELAY_TOKEN_LEN) {
            this.relay = relay
            this.relayToken = relayToken
            candidates = peers + relay
        } else {
            this.relay = null
            this.relayToken = ByteArray(0)
            candidates = peers
        }
    }

    /// 纹理的 Surface；画在平台视图上时不用它。
    fun setTextureSurface(surface: Surface?) {
        if (!view) changeSurface(surface)
    }

    /// 平台视图的 Surface。
    fun setViewSurface(surface: Surface?) {
        if (view) changeSurface(surface)
    }

    // 换了 Surface 解码器要重建，之前的关键帧可能已经被丢掉；画面静止时 Mac 不出帧，要主动要一张
    private fun changeSurface(surface: Surface?) {
        decoder.setSurface(surface)
        if (surface != null) decoderBroken.set(true)
    }

    /// 明文已经是 EncodeInput 的布局。和 hello 共用发送计数器，可以从任意线程调用。
    fun sendInput(plain: ByteArray) {
        try {
            sender.execute { send(KIND_INPUT, plain) }
        } catch (e: java.util.concurrent.RejectedExecutionException) {
            // 已经停了
        }
    }

    /// 向 Mac 出口的公网地址打洞：几秒内连发打洞包，同时把 hello、report 也发过去。
    fun punch(peer: InetSocketAddress) {
        synchronized(ioLock) {
            punchPeer = peer
            punchUntil = now() + 4
        }
    }

    fun start() {
        val t = Thread({ loop() }, "posse.desk")
        thread = t
        t.start()
    }

    fun requestStop() {
        synchronized(flagLock) { stopped = true }
    }

    /// 建好了却没开始（停止抢在前面，代数变了）：收包线程没起来，它收尾时关的解码线程和发送线程在这里关。
    fun discard() {
        if (thread != null) return
        sender.shutdown()
        decoder.shutdown()
    }

    fun setPaused(value: Boolean) {
        synchronized(flagLock) {
            if (value) {
                paused = true
            } else if (paused) {
                paused = false
                needReset = true
            }
        }
    }

    fun waitUntilStopped() {
        if (thread == null) return
        joined.await()
    }

    private fun takeFlags(): Triple<Boolean, Boolean, Boolean> = synchronized(flagLock) {
        val reset = needReset
        needReset = false
        Triple(stopped, paused, reset)
    }

    /// 重新 hello。接收窗口清掉：Mac 可能已经把发送计数从头计。
    private fun prepareForPeer() {
        synchronized(ioLock) {
            active = null
            punchPeer = null
            punchUntil = 0.0
        }
        publicAddr = null
        probes = 0
        gotVideo = false
        replay.reset()
        depacketizer.reset()
        waitingKey = true
        primed = false
        haveSeq = false
        expect = 0
        highest = 0
        held.clear()
        missing.clear()
        fillFrame = null
        lastFrameAt = 0.0
        lossInterval = 0
        lossTotal = 0
        packetsInterval = 0
        gapsUs.clear()
        delays.clear()
        delayFloor.clear()
        synchronized(statsLock) {
            videoRate.reset()
            receivedAt.clear()
            decodedAt.clear()
            decodeMs.clear()
            shownAt = 0.0
            shown = DeskShown()
        }
    }

    private fun loop() {
        var sock: DatagramSocket? = null
        try {
            var nextHello = 0.0
            var nextRegister = 0.0
            var nextReport = 0.0
            var nextKey = 0.0
            var nextStats = 0.0
            var nextProbe = 0.0
            var nextPunch = 0.0
            val buf = ByteArray(2048)
            val packet = DatagramPacket(buf, buf.size)
            while (true) {
                val (stop, pause, reset) = takeFlags()
                if (stop) break
                if (pause) {
                    if (sock != null) {
                        closeSocket()
                        sock = null
                    }
                    Thread.sleep(50)
                    continue
                }
                if (sock == null || reset) {
                    if (sock != null) closeSocket()
                    sock = openSocket()
                    if (sock == null) {
                        Thread.sleep(200)
                        continue
                    }
                    synchronized(ioLock) { socket = sock }
                    prepareForPeer()
                    val t = now()
                    nextHello = t
                    nextRegister = t
                    nextReport = t
                    nextKey = t
                    nextStats = t
                }
                if (decoderBroken.getAndSet(false)) markLost()
                val t = now()
                if (relay != null && t >= nextRegister) {
                    registerRelay()
                    nextRegister = t + 5
                }
                // 画面经中转到了才探测：局域网通着不用打洞。探测几次没回就算了
                if (relay != null && publicAddr == null && probes < 6 && t >= nextProbe && gotVideo && path() == "relay") {
                    val probe = ByteArray(24)
                    byteArrayOf(0x48, 0x52, 0x59, 0x31, 0x03).copyInto(probe)
                    sendControl(probe, relay)
                    probes++
                    nextProbe = t + 0.5
                }
                if (t >= nextPunch) {
                    punchTarget(t)?.let {
                        sendControl(byteArrayOf(0x48, 0x52, 0x59, 0x31, 0x05), it)
                        nextPunch = t + 0.05
                    }
                }
                if (!gotVideo && t >= nextHello) {
                    send(KIND_HELLO, ByteArray(0))
                    nextHello = t + 0.1
                }
                if (t >= nextReport) {
                    sendReport()
                    nextReport = t + 0.1
                }
                // 进来时多半赶不上开头那个关键帧，参数集还没有，先要一张。丢包之后也走这里。
                if (waitingKey && (t >= nextKey || nextKeyNow)) {
                    nextKeyNow = false
                    send(KIND_KEYFRAME, ByteArray(0))
                    nextKey = t + 0.2
                }
                if (t >= nextStats) {
                    emitStats(t)
                    nextStats = t + 0.2
                }
                serviceMissing(t)
                packet.setData(buf, 0, buf.size)
                try {
                    sock.receive(packet)
                } catch (e: SocketTimeoutException) {
                    continue
                } catch (e: Exception) {
                    // 换网络时 socket 可能报错，重开一个
                    closeSocket()
                    sock = null
                    continue
                }
                val n = packet.length
                val from = packet.socketAddress as? InetSocketAddress ?: continue
                // 中转的回执、探测回包和 Mac 的打洞包都不是 desk 包
                if (n >= 4 && buf[0] == 0x48.toByte() && buf[1] == 0x52.toByte() && buf[2] == 0x59.toByte() && buf[3] == 0x31.toByte()) {
                    if (n == 11 && buf[4] == 0x83.toByte() && relay != null && sameAddr(from, relay)) {
                        val port = beU16(buf, 9)
                        publicAddr = "${buf[5].toInt() and 0xff}.${buf[6].toInt() and 0xff}.${buf[7].toInt() and 0xff}.${buf[8].toInt() and 0xff}:$port"
                        emitStats(now())
                    }
                    continue
                }
                val opened = open(buf, n) ?: continue
                if (!replay.accept(opened.second)) continue
                heardAt = now()
                noteActive(from)
                if (opened.first == KIND_BEAT) {
                    noteBeat(opened.third)
                    continue
                }
                if (opened.first != KIND_VIDEO) continue
                gotVideo = true
                if (opened.third.size < 29) continue
                receive(opened.third, now())
            }
        } catch (e: InterruptedException) {
            // 停下
        } finally {
            sender.shutdown()
            decoder.shutdown()
            closeSocket()
            joined.countDown()
        }
    }

    private fun open(buf: ByteArray, count: Int): Triple<Int, Long, ByteArray>? {
        if (count < 1 + 8 + 16 || count > PACKET_LIMIT) return null
        val kind = buf[0].toInt() and 0xff
        val counter = beU64(buf, 1)
        return try {
            openCipher.init(Cipher.DECRYPT_MODE, keyToMobile, GCMParameterSpec(128, nonce(DIR_TO_MOBILE, counter)))
            openCipher.updateAAD(byteArrayOf(kind.toByte()))
            Triple(kind, counter, openCipher.doFinal(buf, 9, count - 9))
        } catch (e: Exception) {
            null
        }
    }

    /// 一个视频包按 RTP 序号排进来。正好是下一个就交给拆包，再把排在后面已经到了的接着交；
    /// 跳过了几个就记成丢包：Mac 认补发时先存着、点名要缺的，不认时照旧跳过去、要关键帧。
    private fun receive(plain: ByteArray, arrival: Double) {
        val seq = beU16(plain, 19)
        packetsInterval++
        if (!haveSeq) {
            expect = seq
            highest = seq
            haveSeq = true
        }
        val ahead = (seq - expect) and 0xffff
        // 回绕过来的旧包、重复的补发
        if (ahead >= 32768) return
        var filled = false
        val miss = missing.remove(seq)
        if (miss != null) {
            filled = true
            if (miss.asked > 0) {
                // 点过不止一次的分不清是哪次补上的，按最早那次算，估计只会偏大
                rtt = rtt * 0.8 + maxOf(arrival - miss.firstAsk, 0.005) * 0.2
            }
        } else if (ahead > 0 && held.containsKey(seq)) {
            return
        }
        // highest 比 expect 还小一个（前面的都交了）时 far 是 -1
        val far = ((highest - expect) and 0xffff).toShort().toInt()
        if (ahead > far) {
            val gap = ahead - far - 1
            if (gap > 0) {
                lossInterval += gap
                lossTotal += gap
            }
            if (nack && gap > 0 && gap <= NACK_WINDOW) {
                var s = (highest + 1) and 0xffff
                while (s != seq) {
                    missing[s] = Miss(arrival)
                    s = (s + 1) and 0xffff
                }
            } else if (gap > 0) {
                // 不能补，或者一下断了太多（补也来不及）：跳过缺的，拆包看到序号断了会丢掉这一帧，接着等关键帧
                held.clear()
                missing.clear()
                expect = seq
                highest = seq
                markLost()
                deliver(plain, arrival, false)
                return
            }
            highest = seq
        }
        if (seq == expect) {
            deliver(plain, arrival, filled)
            drain()
        } else {
            held[seq] = plain to arrival
        }
    }

    /// 把从 expect 起已经到了的包按序交出去，碰到还缺的停下。
    private fun drain() {
        while (true) {
            val (plain, at) = held.remove(expect) ?: return
            deliver(plain, at, false)
        }
    }

    /// 点名要缺的包：新缺的马上要，没补上的隔一会儿再要，最多三次。最前面缺的那个等太久（或者攒的包太多）
    /// 就不等了：跳到下一个到了的包，拆包丢掉缺了的帧，要关键帧。
    private fun serviceMissing(t: Double) {
        if (!nack || missing.isEmpty()) return
        val retry = maxOf(rtt * 1.5, 0.04)
        val ask = ArrayList<Int>()
        for ((seq, miss) in missing) {
            if (miss.asked < 3 && t - miss.lastAsk >= retry) {
                ask.add(seq)
                miss.lastAsk = t
                if (miss.asked == 0) miss.firstAsk = t
                miss.asked++
            }
        }
        var start = 0
        while (start < ask.size) {
            val end = minOf(start + NACK_MAX, ask.size)
            val raw = ByteArray((end - start) * 2)
            for (i in start until end) {
                raw[(i - start) * 2] = (ask[i] shr 8).toByte()
                raw[(i - start) * 2 + 1] = ask[i].toByte()
            }
            send(KIND_NACK, raw)
            start = end
        }
        val giveUp = minOf(maxOf(rtt * 3 + 0.1, 0.25), 1.0)
        val first = missing[expect] ?: return
        if (!(t - first.found > giveUp || held.size > 2048)) return
        // 下一个已经到了的包
        val resume = held.keys.minByOrNull { (it - expect) and 0xffff }
        if (resume == null) {
            missing.clear()
            return
        }
        var s = expect
        while (s != resume) {
            missing.remove(s)
            s = (s + 1) and 0xffff
        }
        expect = resume
        markLost()
        drain()
    }

    private fun markLost() {
        if (!waitingKey) {
            waitingKey = true
            // 下一圈马上要关键帧，不等上一个 200 ms 间隔走完。
            nextKeyNow = true
        }
    }

    private fun deliver(plain: ByteArray, arrival: Double, filled: Boolean) {
        expect = if (plain.size >= 21) (beU16(plain, 19) + 1) and 0xffff else (expect + 1) and 0xffff
        val number = beU64(plain, 1)
        if (filled) fillFrame = number
        val unit = depacketizer.push(plain) ?: return
        val idr = unit.nals.any { nalType(it) in 16..21 }
        if (waitingKey) {
            if (unit.key || idr) {
                if (primed) recoveries++
                waitingKey = false
                primed = true
            } else {
                return
            }
        }
        if (fillFrame == unit.frame) {
            lastFrameAt = 0.0
        } else {
            if (lastFrameAt != 0.0) {
                // 排在缺口后面的包按当初的到达时刻交出来，可能比上一帧还早
                gapsUs.add(minOf(maxOf(arrival - lastFrameAt, 0.0) * 1_000_000, 4294967295.0).toLong())
            }
            lastFrameAt = maxOf(arrival, lastFrameAt)
            noteDelay(beU64(plain, 9))
        }
        noteVideo(unit, now())
        decoder.push(unit)
    }

    private fun sendReport() {
        val loss = lossInterval
        val packets = packetsInterval
        val high = expect
        val gap = medianGap()
        val queue = queueDelayUs()
        lossInterval = 0
        packetsInterval = 0
        gapsUs.clear()
        delays.clear()
        val raw = java.nio.ByteBuffer.allocate(8 + 4 + 4 + 8 + 8 + 4)
        raw.putLong(high.toLong())
        raw.putInt(loss.toInt())
        raw.putInt(packets.toInt())
        // 排队延迟（微秒），后面的 1 表示这个字段有效：旧版手机这两格是 0
        raw.putLong(queue ?: 0)
        raw.putLong(if (queue == null) 0 else 1)
        raw.putInt(gap.toInt())
        send(KIND_REPORT, raw.array())
    }

    /// 时钟差是常数，减掉最近 10 秒里最小的那次，剩下的就是这一帧比最顺的时候多排的队。
    private fun noteDelay(capture: Long) {
        if (capture <= 0) return
        val wall = System.currentTimeMillis() / 1000.0
        val delay = wall - capture / 1e9
        delays.add(delay)
        val second = wall.toLong()
        val last = delayFloor.lastOrNull()
        if (last != null && last.first == second) {
            delayFloor[delayFloor.size - 1] = second to minOf(last.second, delay)
        } else {
            delayFloor.add(second to delay)
        }
        delayFloor.removeAll { it.first < second - 10 }
    }

    private fun queueDelayUs(): Long? {
        if (delays.isEmpty()) return null
        val floor = delayFloor.minOfOrNull { it.second } ?: return null
        val sorted = delays.sorted()
        val queue = maxOf(0.0, sorted[sorted.size / 2] - floor)
        return minOf(queue * 1_000_000, 4294967295.0).toLong()
    }

    /// beat 明文 16 字节、大端：前 8 字节是时刻，后 8 字节是已执行的必达序号。
    private fun noteBeat(plain: ByteArray) {
        if (plain.size < 16) return
        inputAck = beU64(plain, 8)
        emitStats(now())
    }

    private fun medianGap(): Long {
        if (gapsUs.isEmpty()) return 0
        val sorted = gapsUs.sorted()
        return sorted[sorted.size / 2]
    }

    private fun send(kind: Int, plain: ByteArray) {
        synchronized(ioLock) {
            val sock = socket ?: return
            sendN++
            val packet = try {
                val out = ByteArray(9 + plain.size + 16)
                out[0] = kind.toByte()
                for (i in 0 until 8) out[1 + i] = (sendN ushr (56 - 8 * i)).toByte()
                sealCipher.init(Cipher.ENCRYPT_MODE, keyToMac, GCMParameterSpec(128, nonce(DIR_TO_MAC, sendN)))
                sealCipher.updateAAD(byteArrayOf(kind.toByte()))
                sealCipher.doFinal(plain, 0, plain.size, out, 9)
                out
            } catch (e: Exception) {
                return
            }
            // 2 秒没收到 Mac 的包就当路断了，重新发给全部候选，Mac 从哪条收到就从哪条回
            val t = now()
            val current = active
            if (current != null && t - activeAt < 2) {
                sendRaw(sock, packet, current)
            } else {
                for (dest in candidates) sendRaw(sock, packet, dest)
            }
            val peer = punchPeer
            if (peer != null && t < punchUntil && (current == null || !sameAddr(current, peer))) {
                sendRaw(sock, packet, peer)
            }
        }
    }

    private fun punchTarget(t: Double): InetSocketAddress? = synchronized(ioLock) {
        val peer = punchPeer
        if (peer != null && t < punchUntil) peer else null
    }

    /// 画面现在走哪条路：lan（局域网、Tailscale 等直连地址）、relay、punched（打洞直连），还没收到为空。
    private fun path(): String? = synchronized(ioLock) {
        val current = active ?: return null
        if (now() - activeAt >= 2) return null
        if (relay != null && sameAddr(current, relay)) return "relay"
        val peer = punchPeer
        if (peer != null && sameAddr(current, peer)) return "punched"
        "lan"
    }

    /// 不加密的控制包（探测、打洞）。
    private fun sendControl(packet: ByteArray, dest: InetSocketAddress) {
        synchronized(ioLock) {
            val sock = socket ?: return
            sendRaw(sock, packet, dest)
        }
    }

    /// 调用方持有 ioLock。
    private fun sendRaw(sock: DatagramSocket, packet: ByteArray, dest: InetSocketAddress) {
        try {
            sock.send(DatagramPacket(packet, packet.size, dest))
        } catch (e: Exception) {
            // 没网、地址不可达：下一轮再发
        }
    }

    /// 向中转登记：明文，'HRY1'、身份 2、16 字节令牌。兼当保活，换网络后中转靠它更新地址。
    private fun registerRelay() {
        val dest = relay ?: return
        val packet = byteArrayOf(0x48, 0x52, 0x59, 0x31, 2) + relayToken
        sendControl(packet, dest)
    }

    private fun noteActive(from: InetSocketAddress) {
        synchronized(ioLock) {
            val punched = punchPeer?.let { sameAddr(it, from) } ?: false
            if (!punched && candidates.none { sameAddr(it, from) }) return
            active = from
            activeAt = now()
        }
    }

    private fun closeSocket() {
        val sock = synchronized(ioLock) {
            val s = socket
            socket = null
            s
        }
        sock?.close()
    }

    /// 码率只算视频本身：RTP 头、加密开销、重复的补发都不算，和 Moonlight 一样按帧的长度记。
    private fun noteVideo(unit: AccessUnit, t: Double) {
        val bytes = unit.nals.sumOf { it.size }
        synchronized(statsLock) {
            videoRate.add(bytes, t)
            receivedAt.add(t)
            receivedAt.removeAll { t - it > 2 }
        }
    }

    private fun noteDecoded(width: Int, height: Int, ms: Double) {
        val t = now()
        synchronized(statsLock) {
            if (shownAt == 0.0) shownAt = t
            decodedAt.add(t)
            decodeMs.add(t to ms)
            decodedAt.removeAll { t - it > 2 }
            decodeMs.removeAll { t - it.first > 2 }
            pictureWidth = width
            pictureHeight = height
        }
    }

    private fun emitStats(t: Double) {
        val show: DeskShown
        val width: Int
        val height: Int
        synchronized(statsLock) {
            if (shownAt > 0 && t - shownAt >= 1) {
                shownAt = t
                val samples = decodeMs.filter { t - it.first <= 1 }.map { it.second }.sorted()
                val last = decodedAt.lastOrNull()
                shown = DeskShown(
                    fps = decodedAt.count { t - it <= 1 }.toDouble(),
                    received = receivedAt.count { t - it <= 1 }.toDouble(),
                    bitrate = videoRate.average(t),
                    peak = videoRate.peak(t),
                    // Mac 只在画面变了时出帧，1 秒没新帧是画面没动，不是卡了。前提是 Mac 的包（至少每秒一次的 beat）
                    // 还在来、也不在等关键帧：断网或丢包等关键帧时画面也不动，那是卡住
                    still = (last != null && t - last > 1) && !waitingKey && t - heardAt < 2.5,
                    decodeMs = if (samples.isEmpty()) 0.0 else samples[samples.size / 2],
                )
            }
            show = shown
            width = pictureWidth
            height = pictureHeight
        }
        val stats = HashMap<String, Any>()
        stats["fps"] = show.fps
        stats["received"] = show.received
        stats["bitrate"] = show.bitrate
        stats["peak"] = show.peak
        stats["still"] = show.still
        stats["loss"] = lossTotal
        stats["decodeMs"] = show.decodeMs
        stats["recoveries"] = recoveries
        stats["width"] = width
        stats["height"] = height
        stats["video"] = gotVideo
        stats["ack"] = inputAck
        stats["view"] = view
        publicAddr?.let { stats["public"] = it }
        path()?.let { stats["path"] = it }
        onStats?.invoke(stats)
    }

    private fun openSocket(): DatagramSocket? = try {
        DatagramSocket(null).apply {
            bind(InetSocketAddress(0))
            // 没包来时也要醒得勤一点：缺的包隔几十毫秒要再点名一次
            soTimeout = 20
            // 默认接收缓冲很小，关键帧连续十几个包会在套接字里被丢掉。
            receiveBufferSize = 1 shl 20
        }
    } catch (e: Exception) {
        null
    }
}
