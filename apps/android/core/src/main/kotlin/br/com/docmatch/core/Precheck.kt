// The precheck of picked files, the same as the site's (apps/web/src/lib/precheck.ts): the kind
// comes from the first bytes and must agree with the extension, the caps come from limits.yaml,
// and every refusal uses the site's message. testdata/precheck/cases.json keeps the three clients
// (web, Android, iOS) on the same verdicts. The API repeats every check.
package br.com.docmatch.core

import br.com.docmatch.contracts.Limits

enum class FileKind(val id: String) {
    CSV("csv"),
    TXT("txt"),
    XLSX("xlsx"),
    PDF("pdf"),
    ZIP("zip"),
    EML("eml"),
    OFX("ofx"),
    XML("xml"),
    IMAGE("image"),
    AUDIO("audio"),
    VIDEO("video"),
    ;

    /** Audio and video go up as media (a PUT, no hash); everything else as a document. */
    val media: Boolean get() = this == AUDIO || this == VIDEO

    companion object {
        fun of(id: String): FileKind? = entries.firstOrNull { it.id == id }
    }
}

/** A refusal: the message key of the site and its variables. */
data class Refusal(val key: String, val vars: Map<String, Any>)

/** A file the picker returned, before the precheck: its name, its size (-1 unknown), its head. */
class Candidate(val name: String, val size: Long, val head: () -> ByteArray)

object Precheck {
    /** Bytes read from the head of each file for sniffing. */
    const val HEAD_BYTES = 4096

    private const val OOXML = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
    private const val KB = 1024L
    private const val MB = 1024 * KB
    private const val GB = 1024 * MB

    val documentFileBytes = Limits.Files.DOCUMENT_FILE_MAX_MB * MB
    val documentRunBytes = Limits.Files.DOCUMENT_RUN_MAX_MB * MB
    val mediaFileBytes = Limits.Files.MEDIA_FILE_MAX_GB * GB
    val mediaRunBytes = Limits.Files.MEDIA_RUN_MAX_GB * GB

    /** Extension -> kind. CNAB return/remittance files (.ret/.rem) are fixed-width text. */
    val extensions: Map<String, FileKind> =
        buildMap {
            listOf("csv", "tsv").forEach { put(it, FileKind.CSV) }
            listOf("txt", "text", "md", "log", "ret", "rem", "cnab").forEach { put(it, FileKind.TXT) }
            listOf("xlsx", "xlsm", "xls", "ods").forEach { put(it, FileKind.XLSX) }
            put("pdf", FileKind.PDF)
            put("zip", FileKind.ZIP)
            put("eml", FileKind.EML)
            put("mbox", FileKind.EML)
            put("ofx", FileKind.OFX)
            put("xml", FileKind.XML)
            listOf("png", "jpg", "jpeg", "gif", "webp", "heic", "heif", "tif", "tiff", "bmp").forEach { put(it, FileKind.IMAGE) }
            listOf("mp3", "wav", "m4a", "aac", "flac", "ogg", "oga", "opus", "weba").forEach { put(it, FileKind.AUDIO) }
            listOf("mp4", "m4v", "mov", "webm", "mkv", "avi").forEach { put(it, FileKind.VIDEO) }
        }

    private val textKinds = setOf(FileKind.CSV, FileKind.TXT, FileKind.EML, FileKind.OFX, FileKind.XML)
    private val audioMime =
        mapOf(
            "mp3" to "audio/mpeg", "wav" to "audio/wav", "m4a" to "audio/mp4", "aac" to "audio/aac", "flac" to "audio/flac",
            "ogg" to "audio/ogg", "oga" to "audio/ogg", "opus" to "audio/opus", "weba" to "audio/webm",
        )
    private val videoMime =
        mapOf(
            "mp4" to "video/mp4", "m4v" to "video/mp4", "mov" to "video/quicktime", "webm" to "video/webm",
            "mkv" to "video/x-matroska", "avi" to "video/x-msvideo",
        )
    private val headerLine = Regex("^[A-Za-z][A-Za-z0-9-]*:[ \t]")

    fun extension(name: String): String? = Regex("\\.([A-Za-z0-9]{1,8})$").find(name.trim())?.groupValues?.get(1)?.lowercase()

    /** The kind of a file from its extension and content, or the reason it is refused. */
    fun classify(name: String, head: ByteArray): Pair<FileKind?, Refusal?> {
        val ext = extension(name)
        val extKind = ext?.let { extensions[it] }
        val detected = sniff(head, ext)
        val unknown = Refusal("errors.file_type_unknown", mapOf("name" to name))
        val mismatch = Refusal("errors.file_extension_mismatch", mapOf("name" to name, "ext" to (ext ?: "")))
        if (detected == null) {
            if (extKind != null && looksLikeText(head) && extKind !in textKinds) return null to mismatch
            if (extKind != null && !looksLikeText(head) && extKind in textKinds) return null to mismatch
            return null to unknown
        }
        if (extKind != null && extKind != detected) return null to mismatch
        // Unknown extension: trust unambiguous content, never containers or plain text.
        if (extKind == null && ext != null && (detected == FileKind.ZIP || detected == FileKind.XLSX || detected in textKinds)) {
            return null to unknown
        }
        return detected to null
    }

    /** The MIME sent with the upload: one the API's scanner accepts for the kind (null: not sent). */
    fun mime(kind: FileKind, name: String): String? {
        val ext = extension(name)
        return when (kind) {
            FileKind.CSV -> if (ext == "tsv") "text/tab-separated-values" else "text/csv"
            FileKind.TXT -> "text/plain"
            FileKind.XLSX ->
                when (ext) {
                    "xls" -> "application/vnd.ms-excel"
                    "ods" -> "application/vnd.oasis.opendocument.spreadsheet"
                    else -> OOXML
                }
            FileKind.PDF -> "application/pdf"
            FileKind.ZIP -> "application/zip"
            FileKind.EML -> if (ext == "mbox") "application/mbox" else "message/rfc822"
            FileKind.XML -> "application/xml"
            FileKind.OFX -> "application/x-ofx"
            FileKind.AUDIO -> audioMime[ext] ?: "audio/mpeg"
            FileKind.VIDEO -> videoMime[ext] ?: "video/mp4"
            FileKind.IMAGE -> null // the API does not take images yet
        }
    }

    /**
     * Checks what the picker returned against what is already in the list, in the site's order:
     * name, empty, duplicate (name and size), readable head, kind, then the per-family caps.
     * Returns the accepted files and the refusals, in pt-BR.
     */
    fun check(texts: Texts, candidates: List<Candidate>, existing: List<LocalFile>): Pair<List<Accepted>, List<String>> {
        val accepted = mutableListOf<Accepted>()
        val problems = mutableListOf<String>()
        var docs = existing.count { !it.kind.media }
        var docBytes = existing.filter { !it.kind.media }.sumOf { it.size }
        var media = existing.count { it.kind.media }
        var mediaBytes = existing.filter { it.kind.media }.sumOf { it.size }
        fun refuse(key: String, vars: Map<String, Any>) {
            problems += texts.t(key, vars)
        }
        for (c in candidates) {
            val name = c.name
            if (name.encodeToByteArray().size > Limits.Files.UPLOADED_FILE_NAME_BYTES) {
                refuse("errors.file_name_too_long", mapOf("name" to name, "max" to Limits.Files.UPLOADED_FILE_NAME_BYTES))
                continue
            }
            if (c.size == 0L) {
                refuse("errors.file_empty", mapOf("name" to name))
                continue
            }
            if (existing.any { it.name == name && it.size == c.size } || accepted.any { it.name == name && it.size == c.size }) {
                refuse("errors.file_duplicate", mapOf("name" to name))
                continue
            }
            val head =
                try {
                    c.head()
                } catch (e: Exception) {
                    refuse("errors.read_failed", mapOf("name" to name))
                    continue
                }
            val (kind, refusal) = classify(name, head)
            if (refusal != null || kind == null) {
                refuse(refusal!!.key, refusal.vars)
                continue
            }
            val mime = mime(kind, name)
            if (mime == null) {
                refuse("mobile.image_unsupported", mapOf("name" to name))
                continue
            }
            if (kind.media) {
                when {
                    c.size > mediaFileBytes -> refuse("errors.file_too_large", mapOf("name" to name, "max" to Format.size(texts, mediaFileBytes)))
                    media + 1 > Limits.Ids.MEDIA_FILES_PER_RUN_HARD_MAX -> refuse("errors.too_many_files", mapOf("max" to Limits.Ids.MEDIA_FILES_PER_RUN_HARD_MAX))
                    mediaBytes + c.size > mediaRunBytes -> refuse("errors.media_run_too_large", mapOf("max" to Format.size(texts, mediaRunBytes)))
                    else -> {
                        media++
                        mediaBytes += c.size
                        accepted += Accepted(name, c.size, kind, mime)
                    }
                }
            } else {
                when {
                    c.size > documentFileBytes -> refuse("errors.file_too_large", mapOf("name" to name, "max" to Format.size(texts, documentFileBytes)))
                    docs + 1 > Limits.Ids.FILES_PER_RUN_HARD_MAX -> refuse("errors.too_many_files", mapOf("max" to Limits.Ids.FILES_PER_RUN_HARD_MAX))
                    docBytes + c.size > documentRunBytes -> refuse("errors.run_too_large", mapOf("max" to Format.size(texts, documentRunBytes)))
                    else -> {
                        docs++
                        docBytes += c.size
                        accepted += Accepted(name, c.size, kind, mime)
                    }
                }
            }
        }
        return accepted to problems
    }

    /** A candidate that passed: what the upload needs. */
    data class Accepted(val name: String, val size: Long, val kind: FileKind, val mime: String)

    fun looksLikeText(bytes: ByteArray): Boolean {
        if (bytes.isEmpty()) return false
        var control = 0
        for (b in bytes) {
            val v = b.toInt() and 0xff
            if (v == 0) return false
            if (v < 32 && v != 9 && v != 10 && v != 13 && v != 12 && v != 27) control++
        }
        return control.toDouble() / bytes.size < 0.02
    }

    fun looksLikeEmail(text: String): Boolean {
        val lines = text.removePrefix("﻿").split(Regex("\r?\n")).take(40)
        val first = lines.firstOrNull().orEmpty()
        if (!(first.startsWith("From ") || headerLine.containsMatchIn(first))) return false
        var headers = 0
        for (line in lines) {
            if (line.isEmpty()) break
            if (headerLine.containsMatchIn(line)) headers++
        }
        return headers >= 2
    }

    fun looksLikeOfx(text: String): Boolean {
        val head = text.take(2048).uppercase()
        return "OFXHEADER" in head || "<OFX>" in head || "<?OFX" in head
    }

    /** The kind from the first bytes; the extension disambiguates containers. Null: not accepted. */
    fun sniff(bytes: ByteArray, ext: String?): FileKind? {
        val extKind = ext?.let { extensions[it] }
        fun starts(vararg sig: Int, offset: Int = 0): Boolean =
            bytes.size >= offset + sig.size && sig.indices.all { (bytes[offset + it].toInt() and 0xff) == sig[it] }
        fun ascii(offset: Int, length: Int): String =
            buildString { for (i in offset until minOf(bytes.size, offset + length)) append((bytes[i].toInt() and 0xff).toChar()) }

        if (starts(0x25, 0x50, 0x44, 0x46)) return FileKind.PDF
        if (starts(0x50, 0x4b, 0x03, 0x04) || starts(0x50, 0x4b, 0x05, 0x06) || starts(0x50, 0x4b, 0x07, 0x08)) {
            return when (extKind) {
                FileKind.XLSX -> FileKind.XLSX
                FileKind.ZIP -> FileKind.ZIP
                else -> null // other OOXML/zip-based formats are not accepted
            }
        }
        if (starts(0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1)) return if (extKind == FileKind.XLSX) FileKind.XLSX else null
        if (starts(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a)) return FileKind.IMAGE
        if (starts(0xff, 0xd8, 0xff)) return FileKind.IMAGE
        if (starts(0x47, 0x49, 0x46, 0x38)) return FileKind.IMAGE
        if (starts(0x49, 0x49, 0x2a, 0x00) || starts(0x4d, 0x4d, 0x00, 0x2a)) return FileKind.IMAGE
        if (starts(0x42, 0x4d) && extKind == FileKind.IMAGE) return FileKind.IMAGE
        if (starts(0x52, 0x49, 0x46, 0x46)) {
            return when (ascii(8, 4)) {
                "WEBP" -> FileKind.IMAGE
                "WAVE" -> FileKind.AUDIO
                "AVI " -> FileKind.VIDEO
                else -> null
            }
        }
        if (starts(0x49, 0x44, 0x33)) return FileKind.AUDIO
        if (starts(0x66, 0x4c, 0x61, 0x43)) return FileKind.AUDIO
        if (starts(0x4f, 0x67, 0x67, 0x53)) return if (extKind == FileKind.VIDEO) FileKind.VIDEO else FileKind.AUDIO
        if (bytes.size >= 2 && (bytes[0].toInt() and 0xff) == 0xff && (bytes[1].toInt() and 0xe6) == 0xe2) return FileKind.AUDIO
        if (starts(0x1a, 0x45, 0xdf, 0xa3)) return if (extKind == FileKind.AUDIO) FileKind.AUDIO else FileKind.VIDEO
        if (ascii(4, 4) == "ftyp") {
            val brand = ascii(8, 4)
            if (brand in setOf("heic", "heix", "hevc", "hevx", "mif1", "msf1")) return FileKind.IMAGE
            if (brand == "M4A ") return FileKind.AUDIO
            if (extKind == FileKind.AUDIO) return FileKind.AUDIO
            return FileKind.VIDEO
        }
        if (looksLikeText(bytes)) {
            val text = bytes.decodeToString()
            if (looksLikeEmail(text)) return FileKind.EML
            if (looksLikeOfx(text)) return FileKind.OFX
            // NF-e, CT-e and other XML: markup from the first character (after a BOM).
            if (extKind == FileKind.XML && text.removePrefix("\uFEFF").trimStart().startsWith("<")) return FileKind.XML
            if (extKind == FileKind.CSV || extKind == FileKind.TXT) return extKind
            if (extKind == null && ext == null) return FileKind.TXT
            if (extKind == FileKind.EML || extKind == FileKind.OFX) return FileKind.TXT // reported as a mismatch
            return null
        }
        return null
    }
}
