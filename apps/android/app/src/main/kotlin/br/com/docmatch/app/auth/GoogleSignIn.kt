package br.com.docmatch.app.auth

import android.app.Activity
import androidx.credentials.CredentialManager
import androidx.credentials.CustomCredential
import androidx.credentials.GetCredentialRequest
import androidx.credentials.exceptions.NoCredentialException
import com.google.android.libraries.identity.googleid.GetGoogleIdOption
import com.google.android.libraries.identity.googleid.GoogleIdTokenCredential

/**
 * Google's own sign-in on the phone (Credential Manager). The ID token it returns is issued to the
 * server client id and goes to POST /v1/auth/google/native, which resolves the account exactly
 * like the site's Google sign-in.
 */
object GoogleSignIn {
    suspend fun idToken(activity: Activity, serverClientId: String): String {
        val option =
            GetGoogleIdOption.Builder()
                .setServerClientId(serverClientId)
                .setFilterByAuthorizedAccounts(false)
                .setAutoSelectEnabled(false)
                .build()
        val request = GetCredentialRequest.Builder().addCredentialOption(option).build()
        val credential =
            try {
                CredentialManager.create(activity).getCredential(activity, request).credential
            } catch (e: NoCredentialException) {
                throw IllegalStateException("nenhuma conta Google neste aparelho", e)
            }
        check(credential is CustomCredential && credential.type == GoogleIdTokenCredential.TYPE_GOOGLE_ID_TOKEN_CREDENTIAL) {
            "credencial inesperada: ${credential.type}"
        }
        return GoogleIdTokenCredential.createFrom(credential.data).idToken
    }
}
