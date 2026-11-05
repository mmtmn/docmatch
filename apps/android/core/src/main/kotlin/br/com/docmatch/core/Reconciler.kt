// The reconciliation flow, the same steps as the site: upload and scan each file, ask for the
// estimate (the user confirms the tokens), start the run, follow it to the end and read the
// findings. Pure Kotlin so the JVM tests cover it without a phone.
package br.com.docmatch.core

import kotlinx.coroutines.delay
import java.security.MessageDigest

/** What the screen shows while the flow runs. */
sealed interface Step {
    data class Uploading(val done: Int, val total: Int) : Step

    data class Scanning(val name: String) : Step

    data object Estimating : Step

    data object Starting : Step

    data class Running(val message: String?) : Step
}

/** Uploads ready to run, and the signed estimate the user has to accept. */
data class Prepared(val uploadIds: List<String>, val quote: Quote, val rulesText: String)

class Reconciler(
    private val api: DocmatchApi,
    private val texts: Texts,
    private val pollMillis: Long = 1_000,
    private val maxScanPolls: Int = 180,
    private val sleep: suspend (Long) -> Unit = { delay(it) },
) {
    /** Uploads every file (waiting for the scan) and returns the estimate to confirm. */
    suspend fun prepare(files: List<LocalFile>, rulesText: String, onStep: (Step) -> Unit = {}): Prepared {
        require(files.isNotEmpty()) { "nenhum arquivo" }
        val ids = mutableListOf<String>()
        files.forEachIndexed { i, file ->
            onStep(Step.Uploading(i + 1, files.size))
            ids += upload(file, onStep).id
        }
        onStep(Step.Estimating)
        return Prepared(ids, api.estimate(ids, rulesText), rulesText)
    }

    /** Starts the run the user accepted. */
    suspend fun start(prepared: Prepared, onStep: (Step) -> Unit = {}): Run {
        onStep(Step.Starting)
        return api.createRun(prepared.quote, prepared.uploadIds, prepared.rulesText)
    }

    /** Polls the run until it finishes; failed and cancelled runs come back as they are. */
    suspend fun await(runId: String, onStep: (Step) -> Unit = {}): Run {
        while (true) {
            val run = api.run(runId)
            if (RunStatus.terminal(run.status)) return run
            onStep(Step.Running(run.progress?.message))
            sleep(pollMillis * 2)
        }
    }

    /** One file: ticket, bytes to the presigned URL, then the scanner's verdict. */
    suspend fun upload(file: LocalFile, onStep: (Step) -> Unit = {}): Upload {
        val sha = sha256Hex(file.bytes)
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

    companion object {
        fun sha256Hex(bytes: ByteArray): String =
            MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }
    }
}
