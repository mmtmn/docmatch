package br.com.docmatch.app.auth

import android.app.Activity
import androidx.credentials.CredentialManager
import androidx.credentials.CustomCredential
import androidx.credentials.GetCredentialRequest
import androidx.credentials.exceptions.GetCredentialCancellationException
import androidx.credentials.exceptions.GetCredentialException
import androidx.credentials.exceptions.NoCredentialException
import com.google.android.libraries.identity.googleid.GetGoogleIdOption
import com.google.android.libraries.identity.googleid.GoogleIdTokenCredential

/**
 * Google's own sign-in on the phone (Credential Manager). The ID token it returns is issued to the
 * server client id and goes to POST /v1/auth/google/native, which resolves the account exactly
 * like the site's Google sign-in. Failures come back as [Outcome]s the screen words itself.
 */
object GoogleSignIn {
    sealed interface Outcome {
        data class Token(val idToken: String) : Outcome

        /** The user closed the picker: nothing to say. */
        data object Cancelled : Outcome

        data object NoAccount : Outcome

        data object Failed : Outcome
    }

    suspend fun idToken(activity: Activity, serverClientId: String): Outcome {
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
            } catch (e: GetCredentialCancellationException) {
                return Outcome.Cancelled
            } catch (e: NoCredentialException) {
                return Outcome.NoAccount
            } catch (e: GetCredentialException) {
                return Outcome.Failed
            }
        if (credential !is CustomCredential || credential.type != GoogleIdTokenCredential.TYPE_GOOGLE_ID_TOKEN_CREDENTIAL) return Outcome.Failed
        return runCatching { Outcome.Token(GoogleIdTokenCredential.createFrom(credential.data).idToken) }.getOrDefault(Outcome.Failed)
    }
}
