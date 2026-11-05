package br.com.docmatch.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import br.com.docmatch.app.ui.DocmatchRoot

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val docmatch = (application as DocmatchApplication).docmatch
        setContent { DocmatchRoot(docmatch) }
    }
}
