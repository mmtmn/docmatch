package br.com.docmatch.app

import android.app.Application
import android.os.Build
import br.com.docmatch.core.Docmatch

/** One Docmatch per process: the API client, the session cookies and the pt-BR texts. */
class DocmatchApplication : Application() {
    lateinit var docmatch: Docmatch
        private set

    override fun onCreate() {
        super.onCreate()
        Exports.clear(this)
        val texts = assets.open("pt-BR.json").use { it.readBytes().decodeToString() }
        docmatch =
            Docmatch(
                baseUrl = BuildConfig.API_BASE_URL,
                store = KeystoreSecretStore(this),
                userAgent = Docmatch.androidUserAgent(BuildConfig.VERSION_NAME, Build.MODEL, Build.VERSION.RELEASE),
                textsJson = texts,
                // Debug only: the local API signs upload URLs for its loopback (see HttpTransport).
                aliasLoopback = BuildConfig.DEBUG,
            )
    }
}
