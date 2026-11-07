package br.com.docmatch.app.ui

import android.content.Context
import android.net.Uri
import android.provider.OpenableColumns
import br.com.docmatch.core.Candidate
import br.com.docmatch.core.LocalFile
import br.com.docmatch.core.Precheck
import br.com.docmatch.core.Texts
import java.io.IOException
import java.io.InputStream
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

internal object PickedFiles {
    suspend fun check(
        context: Context,
        uris: List<Uri>,
        texts: Texts,
        existing: List<LocalFile> = emptyList(),
    ): Pair<List<LocalFile>, List<String>> {
        val resolver = context.contentResolver
        return withContext(Dispatchers.IO) {
            val sources = mutableMapOf<String, Uri>()
            val candidates = uris.map { uri ->
                val (name, reported) = metadata(context, uri)
                val size =
                    if (reported >= 0) reported
                    else runCatching { measure(resolver.openInputStream(uri)) }.getOrDefault(-1L)
                sources["$name/$size"] = uri
                Candidate(name, size) { head(resolver.openInputStream(uri)) }
            }
            val (ok, refused) = Precheck.check(texts, candidates, existing)
            ok.map { a ->
                val uri = sources.getValue("${a.name}/${a.size}")
                LocalFile(a.name, a.mime, a.size, a.kind) {
                    resolver.openInputStream(uri) ?: throw IOException("sem conteúdo")
                }
            } to refused
        }
    }

    private fun metadata(context: Context, uri: Uri): Pair<String, Long> {
        runCatching {
            context.contentResolver
                .query(
                    uri,
                    arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE),
                    null,
                    null,
                    null,
                )
                ?.use { c ->
                    if (c.moveToFirst()) {
                        val name = c.getString(0) ?: uri.lastPathSegment.orEmpty()
                        val size = if (c.isNull(1)) -1L else c.getLong(1)
                        return name to size
                    }
                }
        }
        return (uri.lastPathSegment ?: "arquivo") to -1L
    }

    /** The first bytes, for the content check (readNBytes is API 33+). */
    fun head(input: InputStream?): ByteArray {
        val stream = input ?: throw IOException("sem conteúdo")
        stream.use {
            val out = ByteArray(Precheck.HEAD_BYTES)
            var n = 0
            while (n < out.size) {
                val r = it.read(out, n, out.size - n)
                if (r < 0) break
                n += r
            }
            return out.copyOf(n)
        }
    }

    /** The real size when the provider does not say, counted up to just past the largest cap. */
    fun measure(input: InputStream?): Long {
        val stream = input ?: throw IOException("sem conteúdo")
        val cap = Precheck.mediaFileBytes + 1
        stream.use {
            val buffer = ByteArray(64 * 1024)
            var total = 0L
            while (total < cap) {
                val r = it.read(buffer)
                if (r < 0) break
                total += r
            }
            return total
        }
    }
}
