package br.com.docmatch.app

import android.os.Bundle
import android.content.Intent
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import br.com.docmatch.app.ui.DocmatchRoot

class MainActivity : ComponentActivity() {
    private var invitationUrl by mutableStateOf<String?>(null)
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        invitationUrl = intent?.dataString
        enableEdgeToEdge()
        val docmatch = (application as DocmatchApplication).docmatch
        setContent { DocmatchRoot(docmatch,invitationUrl) { invitationUrl = null } }
    }
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        invitationUrl = intent.dataString
    }
}
