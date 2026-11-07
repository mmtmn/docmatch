package br.com.docmatch.core

import java.net.URI
import java.net.URLDecoder

/** A link carries an opaque invitation, never an instruction to accept or switch accounts. */
object InvitationLink {
    fun token(url: String, site: String): String? = runCatching {
        val uri = URI(url)
        val base = URI(site)
        if (uri.userInfo != null) return null
        val web =
            uri.scheme == base.scheme &&
                uri.host == base.host &&
                uri.port == base.port &&
                uri.path == "/convite"
        val app =
            uri.scheme == "docmatch" &&
                uri.host == "convite" &&
                uri.path.isEmpty() &&
                uri.port == -1
        if (!web && !app) return null
        val parts = uri.rawFragment?.split('&') ?: return null
        if (parts.size != 1 || !parts[0].startsWith("token=")) return null
        URLDecoder.decode(parts[0].removePrefix("token="), "UTF-8").takeIf {
            it.matches(Regex("[A-Za-z0-9_-]{32,128}"))
        }
    }
        .getOrNull()
}
