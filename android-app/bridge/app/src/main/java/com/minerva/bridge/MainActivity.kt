package com.minerva.bridge

import android.Manifest
import android.os.Build
import android.app.role.RoleManager
import android.content.pm.PackageManager
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.widget.Button
import android.widget.ScrollView
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.app.ActivityCompat
import androidx.core.content.ContextCompat
import org.json.JSONObject
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Minimal status UI for the Minerva Phone Bridge. Displays connection status,
 * current call state, and a scrollable log of recent events.
 */
class MainActivity : AppCompatActivity(), MinervaClient.Listener {

    companion object {
        private const val PERMISSION_REQUEST_CODE = 100
        private const val ROLE_REQUEST_CODE = 101
        private const val MAX_LOG_LINES = 200

        private val REQUIRED_PERMISSIONS = arrayOf(
            Manifest.permission.RECORD_AUDIO,
            Manifest.permission.READ_PHONE_STATE,
            Manifest.permission.READ_CALL_LOG,
            Manifest.permission.CALL_PHONE,
            Manifest.permission.ANSWER_PHONE_CALLS
        )
    }

    private lateinit var tvConnectionStatus: TextView
    private lateinit var tvCallStatus: TextView
    private lateinit var tvLog: TextView
    private lateinit var scrollLog: ScrollView
    private lateinit var btnConnect: Button
    private val mainHandler = Handler(Looper.getMainLooper())
    private val logLines = mutableListOf<String>()
    private val dateFormat = SimpleDateFormat("HH:mm:ss", Locale.getDefault())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        tvConnectionStatus = findViewById(R.id.tvConnectionStatus)
        tvCallStatus = findViewById(R.id.tvCallStatus)
        tvLog = findViewById(R.id.tvLog)
        scrollLog = findViewById(R.id.scrollLog)
        btnConnect = findViewById(R.id.btnConnect)

        btnConnect.setOnClickListener {
            if (MinervaClient.connected) {
                MinervaClient.disconnect()
                BridgeForegroundService.stop(this)
            } else {
                BridgeForegroundService.start(this)
            }
        }

        updateConnectionUI(MinervaClient.connected)
        MinervaClient.addListener(this)

        checkAndRequestPermissions()
        requestCallScreeningRole()

        // Auto-connect on launch
        if (!MinervaClient.connected) {
            BridgeForegroundService.start(this)
        }

        appendLog("Minerva Phone Bridge initialized")
    }

    override fun onDestroy() {
        MinervaClient.removeListener(this)
        super.onDestroy()
    }

    private fun checkAndRequestPermissions() {
        val missing = REQUIRED_PERMISSIONS.filter {
            ContextCompat.checkSelfPermission(this, it) != PackageManager.PERMISSION_GRANTED
        }

        if (missing.isNotEmpty()) {
            ActivityCompat.requestPermissions(this, missing.toTypedArray(), PERMISSION_REQUEST_CODE)
        } else {
            appendLog("All permissions granted")
        }
    }

    private fun requestCallScreeningRole() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
            appendLog("Call screening role not available on this Android version")
            return
        }
        val roleManager = getSystemService(RoleManager::class.java)
        if (roleManager.isRoleAvailable(RoleManager.ROLE_CALL_SCREENING)) {
            if (!roleManager.isRoleHeld(RoleManager.ROLE_CALL_SCREENING)) {
                val intent = roleManager.createRequestRoleIntent(RoleManager.ROLE_CALL_SCREENING)
                startActivityForResult(intent, ROLE_REQUEST_CODE)
                appendLog("Requesting call screening role")
            } else {
                appendLog("Call screening role already held")
            }
        }
    }

    override fun onRequestPermissionsResult(
        requestCode: Int,
        permissions: Array<out String>,
        grantResults: IntArray
    ) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == PERMISSION_REQUEST_CODE) {
            val denied = permissions.zip(grantResults.toTypedArray())
                .filter { it.second != PackageManager.PERMISSION_GRANTED }
                .map { it.first.substringAfterLast('.') }

            if (denied.isEmpty()) {
                appendLog("All permissions granted")
            } else {
                appendLog("Permissions denied: ${denied.joinToString(", ")}")
            }
        }
    }

    private fun updateConnectionUI(connected: Boolean) {
        mainHandler.post {
            if (connected) {
                tvConnectionStatus.text = getString(R.string.status_connected)
                tvConnectionStatus.setTextColor(0xFF388E3C.toInt()) // Green
                btnConnect.text = getString(R.string.btn_disconnect)
            } else {
                tvConnectionStatus.text = getString(R.string.status_disconnected)
                tvConnectionStatus.setTextColor(0xFFD32F2F.toInt()) // Red
                btnConnect.text = getString(R.string.btn_connect)
            }
        }
    }

    private fun appendLog(message: String) {
        val timestamp = dateFormat.format(Date())
        val line = "[$timestamp] $message"

        mainHandler.post {
            logLines.add(line)
            // Trim old lines
            while (logLines.size > MAX_LOG_LINES) {
                logLines.removeAt(0)
            }
            tvLog.text = logLines.joinToString("\n")
            scrollLog.post {
                scrollLog.fullScroll(ScrollView.FOCUS_DOWN)
            }
        }
    }

    // -- MinervaClient.Listener --

    override fun onConnected() {
        updateConnectionUI(true)
        appendLog("Connected to Minerva server")
    }

    override fun onDisconnected() {
        updateConnectionUI(false)
        appendLog("Disconnected from Minerva server")
    }

    override fun onAudioReceived(pcmData: ByteArray) {
        // Audio is handled by AudioBridge via CallHandler, not UI
    }

    override fun onCommandReceived(command: String, extras: JSONObject) {
        appendLog("Command: $command")
    }

    override fun onLog(message: String) {
        appendLog(message)
    }
}
