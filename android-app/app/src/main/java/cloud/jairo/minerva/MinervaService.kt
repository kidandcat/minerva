package cloud.jairo.minerva

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.os.Build
import android.os.IBinder
import android.util.Log
import androidx.core.app.NotificationCompat
import mobile.Mobile
import org.json.JSONObject

class MinervaService : Service() {

    companion object {
        private const val TAG = "MinervaService"
        private const val CHANNEL_ID = "minerva_service"
        private const val NOTIFICATION_ID = 1

        var isRunning = false
            private set
    }

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        Log.d(TAG, "onStartCommand")

        // Start foreground immediately
        startForeground(NOTIFICATION_ID, createNotification())

        // Load config from SharedPreferences
        val prefs = getSharedPreferences("minerva", MODE_PRIVATE)
        val telegramToken = prefs.getString("telegram_token", "") ?: ""
        val adminId = prefs.getLong("admin_id", 0)
        val openrouterKey = prefs.getString("openrouter_key", "") ?: ""

        if (telegramToken.isBlank() || openrouterKey.isBlank() || adminId == 0L) {
            Log.e(TAG, "Missing required config")
            stopSelf()
            return START_NOT_STICKY
        }

        // Build config JSON
        val config = JSONObject().apply {
            put("telegram_token", telegramToken)
            put("admin_id", adminId)
            put("openrouter_key", openrouterKey)
            put("database_path", "${filesDir.absolutePath}/minerva.db")
            put("webhook_port", 0) // No webhook on mobile
        }

        // Start Minerva in background thread
        Thread {
            try {
                Log.d(TAG, "Starting Minerva...")
                val result = Mobile.start(config.toString())
                if (result.isNotEmpty()) {
                    Log.e(TAG, "Failed to start Minerva: $result")
                    stopSelf()
                } else {
                    Log.d(TAG, "Minerva started successfully")
                    isRunning = true
                }
            } catch (e: Exception) {
                Log.e(TAG, "Error starting Minerva", e)
                stopSelf()
            }
        }.start()

        return START_STICKY
    }

    override fun onDestroy() {
        Log.d(TAG, "onDestroy")
        try {
            Mobile.stop()
        } catch (e: Exception) {
            Log.e(TAG, "Error stopping Minerva", e)
        }
        isRunning = false
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                getString(R.string.notification_channel_name),
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = getString(R.string.notification_channel_description)
            }
            val manager = getSystemService(NotificationManager::class.java)
            manager.createNotificationChannel(channel)
        }
    }

    private fun createNotification(): Notification {
        val pendingIntent = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE
        )

        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle("Minerva")
            .setContentText("Running in background")
            .setSmallIcon(android.R.drawable.ic_dialog_info)
            .setContentIntent(pendingIntent)
            .setOngoing(true)
            .build()
    }
}
