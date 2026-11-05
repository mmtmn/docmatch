// The precheck of picked files, the same as the site's (apps/web/src/lib/precheck.ts) and
// Android's (Precheck.kt): the kind comes from the first bytes and must agree with the extension,
// the caps come from limits.yaml, and every refusal uses the site's message.
// testdata/precheck/cases.json keeps the three clients on the same verdicts.
import Foundation

public enum FileKind: String, Sendable, Equatable, CaseIterable {
    case csv, txt, xlsx, pdf, zip, eml, ofx, image, audio, video

    /// Audio and video go up as media (a PUT of the file, no hash); everything else as a document.
    public var media: Bool { self == .audio || self == .video }
}

/// A refusal: the site's message key and its variables.
public struct Refusal: Sendable, Equatable {
    public let key: String
    public let vars: [String: String]
}

/// A file the picker returned, before the precheck: name, size (-1 when unknown) and its head.
public struct Candidate: Sendable {
    public let name: String
    public let size: Int64
    public let head: @Sendable () throws -> Data

    public init(name: String, size: Int64, head: @escaping @Sendable () throws -> Data) {
        self.name = name
        self.size = size
        self.head = head
    }
}

public enum Precheck {
    /// Bytes read from the head of each file for sniffing.
    public static let headBytes = 4096

    private static let kb: Int64 = 1024
    private static let mb: Int64 = 1024 * 1024
    private static let gb: Int64 = 1024 * 1024 * 1024
    public static let documentFileBytes = Int64(Limits.Files.documentFileMaxMB) * mb
    public static let documentRunBytes = Int64(Limits.Files.documentRunMaxMB) * mb
    public static let mediaFileBytes = Int64(Limits.Files.mediaFileMaxGB) * gb
    public static let mediaRunBytes = Int64(Limits.Files.mediaRunMaxGB) * gb

    /// Extension -> kind. CNAB return/remittance files (.ret/.rem) are fixed-width text.
    public static let extensions: [String: FileKind] = {
        var out: [String: FileKind] = [:]
        for e in ["csv", "tsv"] { out[e] = .csv }
        for e in ["txt", "text", "md", "log", "ret", "rem", "cnab"] { out[e] = .txt }
        for e in ["xlsx", "xlsm", "xls"] { out[e] = .xlsx }
        out["pdf"] = .pdf
        out["zip"] = .zip
        out["eml"] = .eml
        out["ofx"] = .ofx
        for e in ["png", "jpg", "jpeg", "gif", "webp", "heic", "heif", "tif", "tiff", "bmp"] { out[e] = .image }
        for e in ["mp3", "wav", "m4a", "aac", "flac", "ogg", "oga", "opus", "weba"] { out[e] = .audio }
        for e in ["mp4", "m4v", "mov", "webm", "mkv", "avi"] { out[e] = .video }
        return out
    }()

    private static let textKinds: Set<FileKind> = [.csv, .txt, .eml, .ofx]
    private static let audioMime = [
        "mp3": "audio/mpeg", "wav": "audio/wav", "m4a": "audio/mp4", "aac": "audio/aac", "flac": "audio/flac", "ogg": "audio/ogg",
        "oga": "audio/ogg", "opus": "audio/opus", "weba": "audio/webm",
    ]
    private static let videoMime = [
        "mp4": "video/mp4", "m4v": "video/mp4", "mov": "video/quicktime", "webm": "video/webm", "mkv": "video/x-matroska",
        "avi": "video/x-msvideo",
    ]

    /// The lower-cased extension (1 to 8 letters or digits after the last dot), if any.
    public static func fileExtension(_ name: String) -> String? {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let dot = trimmed.lastIndex(of: ".") else { return nil }
        let ext = trimmed[trimmed.index(after: dot)...]
        guard (1...8).contains(ext.count), ext.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber) }) else { return nil }
        return ext.lowercased()
    }

    /// The kind of a file from its extension and content, or why it is refused.
    public static func classify(_ name: String, _ head: Data) -> (FileKind?, Refusal?) {
        let ext = fileExtension(name)
        let extKind = ext.flatMap { extensions[$0] }
        let bytes = [UInt8](head)
        let detected = sniff(bytes, ext)
        let unknown = Refusal(key: "errors.file_type_unknown", vars: ["name": name])
        let mismatch = Refusal(key: "errors.file_extension_mismatch", vars: ["name": name, "ext": ext ?? ""])
        guard let detected else {
            if let extKind, looksLikeText(bytes), !textKinds.contains(extKind) { return (nil, mismatch) }
            if let extKind, !looksLikeText(bytes), textKinds.contains(extKind) { return (nil, mismatch) }
            return (nil, unknown)
        }
        if let extKind, extKind != detected { return (nil, mismatch) }
        // Unknown extension: trust unambiguous content, never containers or plain text.
        if extKind == nil, ext != nil, detected == .zip || detected == .xlsx || textKinds.contains(detected) { return (nil, unknown) }
        return (detected, nil)
    }

    /// The MIME sent with the upload: one the API's scanner accepts for the kind (nil: not sent).
    public static func mime(_ kind: FileKind, _ name: String) -> String? {
        let ext = fileExtension(name)
        switch kind {
        case .csv: return ext == "tsv" ? "text/tab-separated-values" : "text/csv"
        case .txt: return "text/plain"
        case .xlsx: return ext == "xls" ? "application/vnd.ms-excel" : "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
        case .pdf: return "application/pdf"
        case .zip: return "application/zip"
        case .eml: return "message/rfc822"
        case .ofx: return "application/x-ofx"
        case .audio: return ext.flatMap { audioMime[$0] } ?? "audio/mpeg"
        case .video: return ext.flatMap { videoMime[$0] } ?? "video/mp4"
        case .image: return nil  // the API does not take images yet
        }
    }

    /// A candidate that passed: what the upload needs.
    public struct Accepted: Sendable, Equatable {
        public let name: String
        public let size: Int64
        public let kind: FileKind
        public let mime: String
    }

    /// Checks what the picker returned against what is already in the list, in the site's order:
    /// name, empty, duplicate (name and size), readable head, kind, then the per-family caps.
    public static func check(_ texts: Texts, _ candidates: [Candidate], existing: [LocalFile]) -> ([Accepted], [String]) {
        var accepted: [Accepted] = []
        var problems: [String] = []
        var docs = existing.filter { !$0.kind.media }.count
        var docBytes = existing.filter { !$0.kind.media }.reduce(Int64(0)) { $0 + $1.size }
        var media = existing.filter { $0.kind.media }.count
        var mediaBytes = existing.filter { $0.kind.media }.reduce(Int64(0)) { $0 + $1.size }
        func refuse(_ key: String, _ vars: [String: any Sendable]) { problems.append(texts.t(key, vars)) }
        for c in candidates {
            let name = c.name
            if name.utf8.count > Limits.Files.uploadedFileNameBytes {
                refuse("errors.file_name_too_long", ["name": name, "max": Limits.Files.uploadedFileNameBytes])
                continue
            }
            if c.size == 0 {
                refuse("errors.file_empty", ["name": name])
                continue
            }
            if existing.contains(where: { $0.name == name && $0.size == c.size }) || accepted.contains(where: { $0.name == name && $0.size == c.size }) {
                refuse("errors.file_duplicate", ["name": name])
                continue
            }
            guard let head = try? c.head() else {
                refuse("errors.read_failed", ["name": name])
                continue
            }
            let (kind, refusal) = classify(name, head)
            guard let kind else {
                if let refusal { refuse(refusal.key, refusal.vars) }
                continue
            }
            guard let mime = mime(kind, name) else {
                refuse("mobile.image_unsupported", ["name": name])
                continue
            }
            if kind.media {
                if c.size > mediaFileBytes {
                    refuse("errors.file_too_large", ["name": name, "max": Format.size(texts, mediaFileBytes)])
                } else if media + 1 > Limits.IDs.mediaFilesPerRunHardMax {
                    refuse("errors.too_many_files", ["max": Limits.IDs.mediaFilesPerRunHardMax])
                } else if mediaBytes + c.size > mediaRunBytes {
                    refuse("errors.media_run_too_large", ["max": Format.size(texts, mediaRunBytes)])
                } else {
                    media += 1
                    mediaBytes += c.size
                    accepted.append(Accepted(name: name, size: c.size, kind: kind, mime: mime))
                }
            } else {
                if c.size > documentFileBytes {
                    refuse("errors.file_too_large", ["name": name, "max": Format.size(texts, documentFileBytes)])
                } else if docs + 1 > Limits.IDs.filesPerRunHardMax {
                    refuse("errors.too_many_files", ["max": Limits.IDs.filesPerRunHardMax])
                } else if docBytes + c.size > documentRunBytes {
                    refuse("errors.run_too_large", ["max": Format.size(texts, documentRunBytes)])
                } else {
                    docs += 1
                    docBytes += c.size
                    accepted.append(Accepted(name: name, size: c.size, kind: kind, mime: mime))
                }
            }
        }
        return (accepted, problems)
    }

    static func looksLikeText(_ bytes: [UInt8]) -> Bool {
        if bytes.isEmpty { return false }
        var control = 0
        for b in bytes {
            if b == 0 { return false }
            if b < 32 && b != 9 && b != 10 && b != 13 && b != 12 && b != 27 { control += 1 }
        }
        return Double(control) / Double(bytes.count) < 0.02
    }

    private static func isHeaderLine(_ line: Substring) -> Bool {
        guard let first = line.first, first.isASCII, first.isLetter, let colon = line.firstIndex(of: ":") else { return false }
        let name = line[line.startIndex..<colon]
        guard name.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "-") }) else { return false }
        let after = line.index(after: colon)
        return after < line.endIndex && (line[after] == " " || line[after] == "\t")
    }

    static func looksLikeEmail(_ text: String) -> Bool {
        let body = text.hasPrefix("\u{FEFF}") ? String(text.dropFirst()) : text
        let lines = body.replacingOccurrences(of: "\r\n", with: "\n").split(separator: "\n", omittingEmptySubsequences: false).prefix(40)
        let first = lines.first ?? ""
        guard first.hasPrefix("From ") || isHeaderLine(first) else { return false }
        var headers = 0
        for line in lines {
            if line.isEmpty { break }
            if isHeaderLine(line) { headers += 1 }
        }
        return headers >= 2
    }

    static func looksLikeOfx(_ text: String) -> Bool {
        let head = String(text.prefix(2048)).uppercased()
        return head.contains("OFXHEADER") || head.contains("<OFX>") || head.contains("<?OFX")
    }

    /// The kind from the first bytes; the extension disambiguates containers. Nil: not accepted.
    static func sniff(_ bytes: [UInt8], _ ext: String?) -> FileKind? {
        let extKind = ext.flatMap { extensions[$0] }
        func starts(_ sig: [UInt8], at offset: Int = 0) -> Bool {
            bytes.count >= offset + sig.count && zip(sig.indices, sig).allSatisfy { bytes[offset + $0.0] == $0.1 }
        }
        func ascii(_ offset: Int, _ length: Int) -> String {
            guard offset < bytes.count else { return "" }
            return String(bytes[offset..<min(bytes.count, offset + length)].map { Character(Unicode.Scalar($0)) })
        }
        if starts([0x25, 0x50, 0x44, 0x46]) { return .pdf }
        if starts([0x50, 0x4b, 0x03, 0x04]) || starts([0x50, 0x4b, 0x05, 0x06]) || starts([0x50, 0x4b, 0x07, 0x08]) {
            if extKind == .xlsx { return .xlsx }
            if extKind == .zip { return .zip }
            return nil  // other OOXML/zip-based formats are not accepted
        }
        if starts([0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1]) { return extKind == .xlsx ? .xlsx : nil }
        if starts([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]) { return .image }
        if starts([0xff, 0xd8, 0xff]) { return .image }
        if starts([0x47, 0x49, 0x46, 0x38]) { return .image }
        if starts([0x49, 0x49, 0x2a, 0x00]) || starts([0x4d, 0x4d, 0x00, 0x2a]) { return .image }
        if starts([0x42, 0x4d]), extKind == .image { return .image }
        if starts([0x52, 0x49, 0x46, 0x46]) {
            switch ascii(8, 4) {
            case "WEBP": return .image
            case "WAVE": return .audio
            case "AVI ": return .video
            default: return nil
            }
        }
        if starts([0x49, 0x44, 0x33]) { return .audio }
        if starts([0x66, 0x4c, 0x61, 0x43]) { return .audio }
        if starts([0x4f, 0x67, 0x67, 0x53]) { return extKind == .video ? .video : .audio }
        if bytes.count >= 2, bytes[0] == 0xff, bytes[1] & 0xe6 == 0xe2 { return .audio }
        if starts([0x1a, 0x45, 0xdf, 0xa3]) { return extKind == .audio ? .audio : .video }
        if ascii(4, 4) == "ftyp" {
            let brand = ascii(8, 4)
            if ["heic", "heix", "hevc", "hevx", "mif1", "msf1"].contains(brand) { return .image }
            if brand == "M4A " { return .audio }
            return extKind == .audio ? .audio : .video
        }
        if looksLikeText(bytes) {
            let text = String(decoding: bytes, as: UTF8.self)
            if looksLikeEmail(text) { return .eml }
            if looksLikeOfx(text) { return .ofx }
            if extKind == .csv || extKind == .txt { return extKind }
            if extKind == nil, ext == nil { return .txt }
            if extKind == .eml || extKind == .ofx { return .txt }  // reported as a mismatch
            return nil
        }
        return nil
    }
}
