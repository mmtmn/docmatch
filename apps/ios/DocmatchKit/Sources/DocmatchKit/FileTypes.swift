// The documents the apps send: the same extensions the site accepts (apps/web/src/lib/precheck.ts)
// and Android sends, with the MIME type the API expects and the size cap of limits.yaml.
import Foundation

public enum FileTypes {
    /// extension -> MIME sent to POST /v1/uploads.
    public static let documents: [String: String] = [
        "csv": "text/csv",
        "tsv": "text/tab-separated-values",
        "txt": "text/plain",
        "text": "text/plain",
        "md": "text/plain",
        "log": "text/plain",
        "ret": "text/plain",
        "rem": "text/plain",
        "cnab": "text/plain",
        "xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        "xlsm": "application/vnd.ms-excel.sheet.macroEnabled.12",
        "xls": "application/vnd.ms-excel",
        "pdf": "application/pdf",
        "zip": "application/zip",
        "eml": "message/rfc822",
        "ofx": "application/x-ofx",
    ]

    /// The absolute per-file cap (the plan may be lower; the API has the last word).
    public static let maxDocumentBytes = Int64(Limits.Files.documentFileMaxMB) * 1024 * 1024

    /// The lower-cased extension (1 to 8 letters or digits after the last dot), if any.
    public static func fileExtension(_ name: String) -> String? {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let dot = trimmed.lastIndex(of: ".") else { return nil }
        let ext = trimmed[trimmed.index(after: dot)...]
        guard (1...8).contains(ext.count), ext.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber) }) else { return nil }
        return ext.lowercased()
    }

    /// The MIME for a document name, or nil when the site would refuse it too.
    public static func mime(for name: String) -> String? { fileExtension(name).flatMap { documents[$0] } }

    /// Why a picked file cannot go, in pt-BR, or nil when it can.
    public static func problem(_ texts: Texts, name: String, size: Int64) -> String? {
        if mime(for: name) == nil { return texts.t("errors.file_type_unknown", ["name": name]) }
        if size <= 0 { return texts.t("errors.file_empty", ["name": name]) }
        if size > maxDocumentBytes { return texts.t("errors.file_too_large", ["name": name, "max": "\(Limits.Files.documentFileMaxMB) MB"]) }
        if name.utf8.count > Limits.Files.uploadedFileNameBytes {
            return texts.t("errors.file_name_too_long", ["name": name, "max": Limits.Files.uploadedFileNameBytes])
        }
        return nil
    }
}
