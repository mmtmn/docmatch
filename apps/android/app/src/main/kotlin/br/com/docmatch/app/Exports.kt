package br.com.docmatch.app

import android.content.Context
import java.io.File

/**
 * Reports exported for the share sheet live in the cache only while the session does: they go
 * when this device signs out, when another device ends its session, and at every start.
 */
object Exports {
    fun dir(context: Context): File = File(context.cacheDir, "exports")

    fun clear(context: Context) {
        dir(context).deleteRecursively()
    }
}
