// The reconciliation flow, the same steps as the site and the Android app: upload and scan each
// file, ask for the estimate (the user confirms the tokens), start the run, follow it to the end.
import Foundation
#if canImport(CryptoKit)
import CryptoKit
#endif

/// What the screen shows while the flow runs.
public enum Step: Sendable, Equatable {
    case uploading(done: Int, total: Int)
    case scanning(name: String)
    case estimating
    case starting
    case running(message: String?)
}

/// Uploads ready to run and the signed estimate the user has to accept. The idempotency key is
/// this estimate's: confirming again after a lost answer finds the run already started.
public struct Prepared: Sendable, Equatable {
    public let uploadIds: [String]
    public let quote: Quote
    public let options: RunOptions
    public let idempotencyKey: String

    public init(uploadIds: [String], quote: Quote, options: RunOptions, idempotencyKey: String = UUID().uuidString.lowercased()) {
        self.uploadIds = uploadIds
        self.quote = quote
        self.options = options
        self.idempotencyKey = idempotencyKey
    }
}

public final class Reconciler: Sendable {
    private let api: DocmatchAPI
    private let texts: Texts
    private let pollNanos: UInt64
    private let maxScanPolls: Int
    private let sleep: @Sendable (UInt64) async throws -> Void

    public init(
        api: DocmatchAPI, texts: Texts, pollNanos: UInt64 = 1_000_000_000, maxScanPolls: Int = 180,
        sleep: @escaping @Sendable (UInt64) async throws -> Void = { try await Task.sleep(nanoseconds: $0) }
    ) {
        self.api = api
        self.texts = texts
        self.pollNanos = pollNanos
        self.maxScanPolls = maxScanPolls
        self.sleep = sleep
    }

    /// Uploads every file (waiting for the scan) and returns the estimate to confirm.
    public func prepare(_ files: [LocalFile], options: RunOptions, onStep: @Sendable (Step) -> Void = { _ in }) async throws -> Prepared {
        precondition(!files.isEmpty, "nenhum arquivo")
        var ids: [String] = []
        for (i, file) in files.enumerated() {
            onStep(.uploading(done: i + 1, total: files.count))
            ids.append(try await upload(file, onStep: onStep).id)
        }
        onStep(.estimating)
        return Prepared(uploadIds: ids, quote: try await api.estimate(uploadIds: ids, options: options), options: options)
    }

    /// Starts the run the user accepted.
    public func start(_ prepared: Prepared, onStep: @Sendable (Step) -> Void = { _ in }) async throws -> Run {
        onStep(.starting)
        return try await api.createRun(prepared.quote, uploadIds: prepared.uploadIds, options: prepared.options, idempotencyKey: prepared.idempotencyKey)
    }

    /// Polls the run until it settles: finished (as it is) or waiting for an approval.
    public func follow(_ runId: String, onStep: @Sendable (Step) -> Void = { _ in }) async throws -> Run {
        while true {
            let run = try await api.run(runId)
            if RunStatus.settled(run.status) { return run }
            onStep(.running(message: run.progress?.message))
            try await sleep(pollNanos * 2)
        }
    }

    /// One file: ticket, bytes to the presigned URL, then the scanner's verdict.
    public func upload(_ file: LocalFile, onStep: @Sendable (Step) -> Void = { _ in }) async throws -> Upload {
        let sha = file.kind.media ? nil : Reconciler.sha256Hex(try api.read(file))
        let ticket = try await api.createUpload(file, sha256: sha)
        try await api.putObject(ticket, file)
        var view = try await api.completeUpload(ticket.id, sha256: sha)
        var polls = 0
        while (view.status == UploadStatus.pending || view.status == UploadStatus.scanning) && polls < maxScanPolls {
            onStep(.scanning(name: file.name))
            try await sleep(pollNanos)
            view = try await api.completeUpload(ticket.id, sha256: nil)
            polls += 1
        }
        guard view.status == UploadStatus.ready else {
            throw ApiError(
                status: 422, code: ApiError.uploadRejected,
                message: texts.t("app.rejected", ["name": file.name, "reason": view.rejectedReason ?? view.status]))
        }
        return view
    }

    public static func sha256Hex(_ data: Data) -> String { sha256(data).map { String(format: "%02x", $0) }.joined() }

    /// SHA-256: CryptoKit in the app, the portable version for the Linux test runs.
    public static func sha256(_ data: Data) -> [UInt8] {
        #if canImport(CryptoKit)
        return Array(SHA256.hash(data: data))
        #else
        return SHA256Portable.digest(data)
        #endif
    }
}

#if !canImport(CryptoKit)
/// FIPS 180-4 SHA-256 for the Linux test runs (the app uses CryptoKit).
enum SHA256Portable {
    private static let k: [UInt32] = [
        0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
        0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
        0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
        0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
        0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
        0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
        0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
        0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
    ]

    static func digest(_ data: Data) -> [UInt8] {
        var h: [UInt32] = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]
        var message = [UInt8](data)
        let bitLength = UInt64(message.count) * 8
        message.append(0x80)
        while message.count % 64 != 56 { message.append(0) }
        for i in (0..<8).reversed() { message.append(UInt8(truncatingIfNeeded: bitLength >> (UInt64(i) * 8))) }
        var w = [UInt32](repeating: 0, count: 64)
        for chunk in stride(from: 0, to: message.count, by: 64) {
            for i in 0..<16 {
                let j = chunk + i * 4
                w[i] = UInt32(message[j]) << 24 | UInt32(message[j + 1]) << 16 | UInt32(message[j + 2]) << 8 | UInt32(message[j + 3])
            }
            for i in 16..<64 {
                let s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >> 3)
                let s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >> 10)
                w[i] = w[i - 16] &+ s0 &+ w[i - 7] &+ s1
            }
            var (a, b, c, d, e, f, g, hh) = (h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7])
            for i in 0..<64 {
                let s1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)
                let ch = (e & f) ^ (~e & g)
                let t1 = hh &+ s1 &+ ch &+ k[i] &+ w[i]
                let s0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)
                let maj = (a & b) ^ (a & c) ^ (b & c)
                let t2 = s0 &+ maj
                (hh, g, f, e, d, c, b, a) = (g, f, e, d &+ t1, c, b, a, t1 &+ t2)
            }
            h[0] = h[0] &+ a; h[1] = h[1] &+ b; h[2] = h[2] &+ c; h[3] = h[3] &+ d
            h[4] = h[4] &+ e; h[5] = h[5] &+ f; h[6] = h[6] &+ g; h[7] = h[7] &+ hh
        }
        return h.flatMap { word in (0..<4).map { UInt8(truncatingIfNeeded: word >> (24 - $0 * 8)) } }
    }

    private static func rotr(_ x: UInt32, _ n: UInt32) -> UInt32 { (x >> n) | (x << (32 - n)) }
}
#endif
