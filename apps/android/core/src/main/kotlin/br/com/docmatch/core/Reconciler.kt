// The reconciliation flow, the same steps as the site: upload and scan each file, ask for the
// estimate (the user confirms the tokens), start the run, follow it until it settles (finished, or
// parked for an extra estimate the user approves). Pure Kotlin so the JVM tests cover it.
package br.com.docmatch.core

import kotlinx.coroutines.delay
import java.io.IOException
import java.security.MessageDigest
import java.util.UUID

/** What the screen shows while the flow runs. */
sealed interface Step {
    data class Uploading(val done: Int, val total: Int) : Step

    data class Scanning(val name: String) : Step

    data object Estimating : Step

    data object Starting : Step

    data class Running(val message: String?) : Step
}

/**
 * Uploads ready to run and the signed estimate the user has to accept. The idempotency key is
 * this estimate's: confirming again after a lost answer finds the run already started.
 */
data class Prepared(
    val uploadIds: List<String>,
    val quote: Quote,
    val options: RunOptions,
    val idempotencyKey: String = UUID.randomUUID().toString(),
)

class Reconciler(
    private val api: DocmatchApi,
    private val texts: Texts,
    private val pollMillis: Long = 1_000,
    private val maxScanPolls: Int = 180,
    private val sleep: suspend (Long) -> Unit = { delay(it) },
) {
    /** Uploads every file (waiting for the scan) and returns the estimate to confirm. */
    suspend fun prepare(files: List<LocalFile>, options: RunOptions, onStep: (Step) -> Unit = {}): Prepared {
        require(files.isNotEmpty()) { "nenhum arquivo" }
        val ids = mutableListOf<String>()
        files.forEachIndexed { i, file ->
            onStep(Step.Uploading(i + 1, files.size))
            ids += upload(file, onStep).id
        }
        onStep(Step.Estimating)
        return Prepared(ids, api.estimate(ids, options), options)
    }

    /** Starts the run the user accepted. */
    suspend fun start(prepared: Prepared, onStep: (Step) -> Unit = {}): Run {
        onStep(Step.Starting)
        return api.createRun(prepared.quote, prepared.uploadIds, prepared.options, prepared.idempotencyKey)
    }

    /** Polls the run until it settles: finished (as it is) or waiting for an approval. */
    suspend fun follow(runId: String, onStep: (Step) -> Unit = {}): Run {
        while (true) {
            val run = api.run(runId)
            if (RunStatus.settled(run.status)) return run
            onStep(Step.Running(run.progress?.message))
            sleep(pollMillis * 2)
        }
    }

    /** One file: ticket, bytes to the presigned URL, then the scanner's verdict. */
    suspend fun upload(file: LocalFile, onStep: (Step) -> Unit = {}): Upload {
        val sha = if (file.kind.media) null else sha256Hex(file)
        val ticket = api.createUpload(file, sha)
        api.putObject(ticket, file)
        var view = api.completeUpload(ticket.id, sha)
        var polls = 0
        while ((view.status == UploadStatus.PENDING || view.status == UploadStatus.SCANNING) && polls < maxScanPolls) {
            onStep(Step.Scanning(file.name))
            sleep(pollMillis)
            view = api.completeUpload(ticket.id, null)
            polls++
        }
        if (view.status != UploadStatus.READY) {
            throw ApiException(
                422,
                ApiException.UPLOAD_REJECTED,
                texts.t("app.rejected", mapOf("name" to file.name, "reason" to (view.rejectedReason ?: view.status))),
            )
        }
        return view
    }

    /** The SHA-256 of the file, streamed (the upload reads it again). */
    private fun sha256Hex(file: LocalFile): String =
        try {
            sha256Hex(file.open())
        } catch (e: IOException) {
            throw ApiException(0, ApiException.UPLOAD_FAILED, texts.t("errors.read_failed", mapOf("name" to file.name)))
        } catch (e: SecurityException) {
            throw ApiException(0, ApiException.UPLOAD_FAILED, texts.t("errors.read_failed", mapOf("name" to file.name)))
        }

    companion object {
        fun sha256Hex(bytes: ByteArray): String = sha256Hex(bytes.inputStream())

        fun sha256Hex(input: java.io.InputStream): String {
            val digest = MessageDigest.getInstance("SHA-256")
            input.use {
                val buffer = ByteArray(64 * 1024)
                while (true) {
                    val n = it.read(buffer)
                    if (n < 0) break
                    digest.update(buffer, 0, n)
                }
            }
            return digest.digest().joinToString("") { "%02x".format(it) }
        }
    }
}
