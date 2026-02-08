package cloud.jairo.minerva

import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import cloud.jairo.minerva.databinding.ActivityMainBinding

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private val prefs by lazy { getSharedPreferences("minerva", MODE_PRIVATE) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        // Load saved config
        loadConfig()

        // Save button
        binding.saveButton.setOnClickListener {
            saveConfig()
            Toast.makeText(this, "Config saved", Toast.LENGTH_SHORT).show()
        }

        // Start button
        binding.startButton.setOnClickListener {
            if (!validateConfig()) {
                Toast.makeText(this, "Please fill all required fields", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }

            saveConfig()
            startMinervaService()
        }

        // Stop button
        binding.stopButton.setOnClickListener {
            stopMinervaService()
        }

        // Update UI based on service state
        updateUI(MinervaService.isRunning)
    }

    override fun onResume() {
        super.onResume()
        updateUI(MinervaService.isRunning)
    }

    private fun loadConfig() {
        binding.telegramToken.setText(prefs.getString("telegram_token", ""))
        binding.adminId.setText(prefs.getLong("admin_id", 0).takeIf { it > 0 }?.toString() ?: "")
        binding.openrouterKey.setText(prefs.getString("openrouter_key", ""))
    }

    private fun saveConfig() {
        prefs.edit().apply {
            putString("telegram_token", binding.telegramToken.text.toString())
            putLong("admin_id", binding.adminId.text.toString().toLongOrNull() ?: 0)
            putString("openrouter_key", binding.openrouterKey.text.toString())
            apply()
        }
    }

    private fun validateConfig(): Boolean {
        return binding.telegramToken.text.toString().isNotBlank() &&
                binding.adminId.text.toString().isNotBlank() &&
                binding.openrouterKey.text.toString().isNotBlank()
    }

    private fun startMinervaService() {
        val intent = Intent(this, MinervaService::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(intent)
        } else {
            startService(intent)
        }
        updateUI(true)
    }

    private fun stopMinervaService() {
        val intent = Intent(this, MinervaService::class.java)
        stopService(intent)
        updateUI(false)
    }

    private fun updateUI(running: Boolean) {
        binding.startButton.isEnabled = !running
        binding.stopButton.isEnabled = running
        binding.statusText.text = if (running) getString(R.string.status_running) else getString(R.string.status_stopped)
        binding.statusText.setTextColor(
            if (running) getColor(android.R.color.holo_green_dark)
            else getColor(android.R.color.holo_red_dark)
        )
    }
}
