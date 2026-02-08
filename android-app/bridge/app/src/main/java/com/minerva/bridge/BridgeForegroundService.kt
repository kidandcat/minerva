package com.minerva.bridge

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.IBinder
import android.util.Log
import androidx.core.app.NotificationCompat
import org.json.JSONObject

/**
 * Foreground service that keeps the Minerva WebSocket connection alive
 * in the background. Also handles make_call commands since CallHandler
 * (InCallService) is only alive during active calls.
 */
class BridgeForegroundService : Service(), MinervaClient.Listener {

    companion object {
        private const val TAG = "BridgeForegroundService"
        private const val NOTIFICATION_ID = 1001
        private const val CHANNEL_ID = "minerva_bridge_channel"

        fun start(context: Context) {
            val intent = Intent(context, BridgeForegroundService::class.java)
            context.startForegroundService(intent)
        }

        fun stop(context: Context) {
            val intent = Intent(context, BridgeForegroundService::class.java)
            context.stopService(intent)
        }
    }

    override fun onCreate() {
        super.onCreate()
        Log.i(TAG, "Foreground service created")
        createNotificationChannel()
        MinervaClient.addListener(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        Log.i(TAG, "Foreground service started")

        val notification = buildNotification()
        startForeground(NOTIFICATION_ID, notification)

        // Connect to Minerva server
        if (!MinervaClient.connected) {
            MinervaClient.connect()
        }

        return START_STICKY
    }

    override fun onDestroy() {
        Log.i(TAG, "Foreground service destroyed")
        MinervaClient.removeListener(this)
        MinervaClient.disconnect()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    // -- MinervaClient.Listener --

    override fun onConnected() {}
    override fun onDisconnected() {}
    override fun onAudioReceived(pcmData: ByteArray) {}
    override fun onLog(message: String) {}

    override fun onCommandReceived(command: String, extras: JSONObject) {
        when (command) {
            "make_call" -> {
                val to = extras.optString("to", "")
                if (to.isNotEmpty()) {
                    makeCall(to)
                } else {
                    Log.w(TAG, "make_call command missing 'to' field")
                }
            }
        }
    }

    private fun makeCall(phoneNumber: String) {
        val intent = Intent(Intent.ACTION_CALL).apply {
            data = Uri.parse("tel:$phoneNumber")
            flags = Intent.FLAG_ACTIVITY_NEW_TASK
        }
        try {
            startActivity(intent)
            Log.i(TAG, "Initiating call to $phoneNumber")
        } catch (e: SecurityException) {
            Log.e(TAG, "Permission denied for making call: ${e.message}")
        }
    }

    private fun createNotificationChannel() {
        val channel = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.notification_channel_name),
            NotificationManager.IMPORTANCE_LOW
        ).apply {
            description = getString(R.string.notification_channel_description)
            setShowBadge(false)
        }
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(channel)
    }

    private fun buildNotification(): Notification {
        val pendingIntent = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.notification_title))
            .setContentText(getString(R.string.notification_text))
            .setSmallIcon(android.R.drawable.stat_sys_phone_call)
            .setContentIntent(pendingIntent)
            .setOngoing(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()
    }
}
