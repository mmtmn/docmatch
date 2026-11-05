// The documents the apps send, the same extensions the site accepts (apps/web/src/lib/precheck.ts),
// with the MIME type the API expects and the size cap of limits.yaml.
package br.com.docmatch.core

import br.com.docmatch.contracts.Limits

object FileTypes {
    /** extension -> MIME sent to POST /v1/uploads. */
    val documents: Map<String, String> =
        mapOf(
            "csv" to "text/csv",
            "tsv" to "text/tab-separated-values",
            "txt" to "text/plain",
            "text" to "text/plain",
            "md" to "text/plain",
            "log" to "text/plain",
            "ret" to "text/plain",
            "rem" to "text/plain",
            "cnab" to "text/plain",
            "xlsx" to "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
            "xlsm" to "application/vnd.ms-excel.sheet.macroEnabled.12",
            "xls" to "application/vnd.ms-excel",
            "pdf" to "application/pdf",
            "zip" to "application/zip",
            "eml" to "message/rfc822",
            "ofx" to "application/x-ofx",
        )

    /** The absolute per-file cap (the plan may be lower; the API has the last word). */
    val maxDocumentBytes: Long = Limits.Files.DOCUMENT_FILE_MAX_MB.toLong() * 1024 * 1024

    fun extension(name: String): String? =
        Regex("\\.([A-Za-z0-9]{1,8})$").find(name.trim())?.groupValues?.get(1)?.lowercase()

    /** The MIME for a document name, or null when the site would refuse it too. */
    fun mimeFor(name: String): String? = extension(name)?.let { documents[it] }

    /** Why a picked file cannot go, in pt-BR, or null when it can. */
    fun problem(texts: Texts, name: String, size: Long): String? =
        when {
            mimeFor(name) == null -> texts.t("errors.file_type_unknown", mapOf("name" to name))
            size <= 0 -> texts.t("errors.file_empty", mapOf("name" to name))
            size > maxDocumentBytes ->
                texts.t("errors.file_too_large", mapOf("name" to name, "max" to "${Limits.Files.DOCUMENT_FILE_MAX_MB} MB"))
            name.encodeToByteArray().size > Limits.Files.UPLOADED_FILE_NAME_BYTES ->
                texts.t("errors.file_name_too_long", mapOf("name" to name, "max" to Limits.Files.UPLOADED_FILE_NAME_BYTES))
            else -> null
        }
}
