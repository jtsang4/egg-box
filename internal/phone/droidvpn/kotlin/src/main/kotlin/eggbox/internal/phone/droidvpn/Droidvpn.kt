package eggbox.internal.phone.droidvpn

import android.app.Activity
import android.app.PendingIntent
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.graphics.drawable.Icon
import android.net.ConnectivityManager
import android.net.IpPrefix
import android.net.LinkProperties
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.os.Process
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import androidx.activity.ComponentActivity
import androidx.activity.result.ActivityResultLauncher
import androidx.activity.result.contract.ActivityResultContracts
import dev.mygo.*
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.NetworkInterface
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.coroutines.resume
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout

// Android's VPN service for the core. The Go side (package droidvpn) asks for
// the interface with the settings the core computed; this side asks the
// person once, runs the VpnService that makes the interface, keeps the
// core's sockets out of it and reports the device's networks.

fun makeService(): Service = AndroidVpn

internal object AndroidVpn : Service {
    private val context: Context get() = Native.context
    private val connectivity: ConnectivityManager get() = context.getSystemService(ConnectivityManager::class.java)
    private val mainHandler = Handler(Looper.getMainLooper())
    private val revoked = CopyOnWriteArrayList<(String) -> Unit>()
    private val systemStarts = CopyOnWriteArrayList<() -> Unit>()
    private val tileToggles = CopyOnWriteArrayList<() -> Unit>()

    /** The system started the VPN before Go listened for it. */
    @Volatile
    private var pendingSystemStart = false

    /** The Quick Settings tile was tapped before Go listened for it. */
    private val pendingTileToggle = AtomicBoolean(false)

    @Volatile
    private var tileState = TileState(state = "inactive", subtitle = "")

    @Volatile
    private var tileStateKnown = false

    /** Whether the application wants the VPN up: a start by anyone else is refused. */
    @Volatile
    var wanted = false

    override suspend fun prepare(): Boolean {
        val intent = VpnService.prepare(context) ?: return true
        val activity = Native.activity as? ComponentActivity
            ?: throw NativeException("droidvpn", 1, "the application has no window to ask from")
        return withContext(Dispatchers.Main) {
            suspendCancellableCoroutine { cont ->
                var launcher: ActivityResultLauncher<Intent>? = null
                launcher = activity.activityResultRegistry.register(
                    "eggbox.vpn.prepare." + System.nanoTime(),
                    ActivityResultContracts.StartActivityForResult(),
                ) { result ->
                    launcher?.unregister()
                    if (cont.isActive) cont.resume(result.resultCode == Activity.RESULT_OK || VpnService.prepare(context) == null)
                }
                cont.invokeOnCancellation { launcher.unregister() }
                launcher.launch(intent)
            }
        }
    }

    override suspend fun open(s: TunSettings): Long {
        if (VpnService.prepare(context) != null) {
            throw NativeException("droidvpn", 2, "the application may not be the device's VPN")
        }
        wanted = true
        val service = EggBoxVpnService.running ?: withContext(Dispatchers.Main) {
            val started = CompletableDeferred<EggBoxVpnService>()
            EggBoxVpnService.waiting = started
            context.startService(Intent(context, EggBoxVpnService::class.java))
            try {
                withTimeout(10_000) { started.await() }
            } finally {
                EggBoxVpnService.waiting = null
            }
        }
        return service.establish(s).fd.toLong()
    }

    override fun stop() {
        wanted = false
        EggBoxVpnService.running?.let {
            it.release()
            it.stopSelf()
        }
    }

    override fun protect(fd: Long): Boolean = EggBoxVpnService.running?.protect(fd.toInt()) ?: true

    @Suppress("DEPRECATION")
    override fun networks(): List<Network> = connectivity.allNetworks.mapNotNull { n ->
        val caps = connectivity.getNetworkCapabilities(n) ?: return@mapNotNull null
        if (caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) return@mapNotNull null
        val lp = connectivity.getLinkProperties(n) ?: return@mapNotNull null
        val name = lp.interfaceName ?: return@mapNotNull null
        Network(
            name = name,
            index = indexOf(name),
            mtu = (if (Build.VERSION.SDK_INT >= 29 && lp.mtu > 0) lp.mtu else 1500).toLong(),
            addresses = lp.linkAddresses.map { "${host(it.address)}/${it.prefixLength}" },
            kind = kindOf(caps),
            dns = lp.dnsServers.map(::host),
            gateways = gateways(lp),
            metered = !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED),
        )
    }

    override fun watchDefault(f: (DefaultNetwork) -> kotlin.Unit): AutoCloseable {
        val thread = HandlerThread("eggbox-network").apply { start() }
        val handler = Handler(thread.looper)
        var current: android.net.Network? = null
        fun report(n: android.net.Network?) {
            val lp = n?.let { connectivity.getLinkProperties(it) }
            val caps = n?.let { connectivity.getNetworkCapabilities(it) }
            val name = lp?.interfaceName
            val d = if (n == null || name == null) {
                DefaultNetwork("", -1, false, false)
            } else {
                DefaultNetwork(
                    name, indexOf(name),
                    caps?.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) == false, false,
                )
            }
            EggBoxVpnService.running?.setUnderlyingNetworks(n?.let { arrayOf(it) })
            try {
                f(d)
            } catch (_: Exception) {
            }
        }
        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: android.net.Network) {
                current = network
                report(network)
            }

            override fun onCapabilitiesChanged(network: android.net.Network, caps: NetworkCapabilities) {
                if (network == current) report(network)
            }

            override fun onLinkPropertiesChanged(network: android.net.Network, lp: LinkProperties) {
                if (network == current) report(network)
            }

            override fun onLost(network: android.net.Network) {
                if (network == current) {
                    current = null
                    report(null)
                }
            }
        }
        // A request for the internet, which excludes VPNs: the device's default
        // network is this application's own VPN while it runs.
        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_RESTRICTED)
            .build()
        if (Build.VERSION.SDK_INT >= 31) {
            connectivity.registerBestMatchingNetworkCallback(request, callback, handler)
        } else {
            connectivity.requestNetwork(request, callback, handler)
        }
        return AutoCloseable {
            try {
                connectivity.unregisterNetworkCallback(callback)
            } catch (_: Exception) {
            }
            thread.quitSafely()
        }
    }

    override fun onRevoked(f: (String) -> kotlin.Unit): AutoCloseable {
        revoked.add(f)
        return AutoCloseable { revoked.remove(f) }
    }

    override fun onSystemStart(f: () -> kotlin.Unit): AutoCloseable {
        systemStarts.add(f)
        if (pendingSystemStart) {
            pendingSystemStart = false
            try {
                f()
            } catch (_: Exception) {
            }
        }
        return AutoCloseable { systemStarts.remove(f) }
    }

    override fun onTileToggle(f: () -> kotlin.Unit): AutoCloseable {
        tileToggles.add(f)
        if (pendingTileToggle.compareAndSet(true, false)) {
            try {
                f()
            } catch (_: Exception) {
            }
        }
        return AutoCloseable { tileToggles.remove(f) }
    }

    override fun setTileState(state: TileState) {
        updateTileState(context.applicationContext, state, markKnown = true)
    }

    internal fun currentTileState(): TileState {
        if (tileStateKnown) return tileState
        if (tileState.state == "connecting" || tileState.state == "disconnecting") return tileState
        return if (EggBoxVpnService.running != null && wanted) {
            TileState(state = "active", subtitle = "")
        } else {
            TileState(state = "inactive", subtitle = "")
        }
    }

    internal fun updateTileState(c: Context, state: TileState, markKnown: Boolean) {
        tileState = state
        if (markKnown) tileStateKnown = true
        mainHandler.post { EggBoxTileService.listening?.applyState(state) }
        try {
            TileService.requestListeningState(c, ComponentName(c, EggBoxTileService::class.java))
        } catch (_: Exception) {
        }
    }

    /**
     * The person tapped EggBox's Quick Settings tile. Starts Go headlessly
     * when the process is not running yet, otherwise toggles immediately.
     */
    internal fun tileToggled(c: Context) {
        val cur = currentTileState()
        if (cur.state == "connecting" || cur.state == "disconnecting") return
        val optimistic = if (cur.state == "active") {
            TileState(state = "disconnecting", subtitle = "")
        } else {
            TileState(state = "connecting", subtitle = "")
        }
        updateTileState(c.applicationContext, optimistic, markKnown = false)
        if (tileToggles.isEmpty()) {
            pendingTileToggle.set(true)
            HostImpl.startHeadless(c.applicationContext)
            return
        }
        for (f in tileToggles) {
            try {
                f()
            } catch (_: Exception) {
            }
        }
    }

    /**
     * The system started the VPN by itself (always-on VPN). Go may not run:
     * the system can start the process for the service alone, and Go starts
     * with the application's window otherwise. Main thread.
     */
    fun systemStarted(c: Context) {
        wanted = true
        if (systemStarts.isEmpty()) {
            // Told once Go listens; started here if nothing started it yet.
            pendingSystemStart = true
            HostImpl.startHeadless(c.applicationContext)
            return
        }
        for (f in systemStarts) {
            try {
                f()
            } catch (_: Exception) {
            }
        }
    }

    /** The system ended the VPN. */
    fun revoke(reason: String) {
        wanted = false
        for (f in revoked) {
            try {
                f(reason)
            } catch (_: Exception) {
            }
        }
    }

    override fun owner(protocol: Long, source: String, sourcePort: Long, destination: String, destinationPort: Long): ConnectionOwner {
        if (Build.VERSION.SDK_INT < 29) throw NativeUnsupportedException()
        val uid = connectivity.getConnectionOwnerUid(
            protocol.toInt(),
            InetSocketAddress(InetAddress.getByName(source), sourcePort.toInt()),
            InetSocketAddress(InetAddress.getByName(destination), destinationPort.toInt()),
        )
        if (uid == Process.INVALID_UID) throw NativeException("droidvpn", 3, "no application owns the connection")
        val packages = context.packageManager.getPackagesForUid(uid)?.toList() ?: emptyList()
        return ConnectionOwner(uid.toLong(), packages)
    }

    private fun indexOf(name: String): Long = try {
        NetworkInterface.getByName(name)?.index?.toLong() ?: 0
    } catch (_: Exception) {
        0
    }

    private fun kindOf(caps: NetworkCapabilities): String = when {
        caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
        caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
        caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
        else -> "other"
    }

    private fun gateways(lp: LinkProperties): List<String> =
        lp.routes.filter { it.isDefaultRoute && it.gateway != null && !it.gateway!!.isAnyLocalAddress }.map { host(it.gateway!!) }

    /** An address without its scope ("fe80::1%wlan0" is "fe80::1"). */
    private fun host(a: InetAddress): String = a.hostAddress!!.substringBefore('%')
}

/**
 * The VpnService. The system binds it while the VPN runs, which keeps the
 * application's process, and the core in it, alive in the background.
 */
class EggBoxVpnService : VpnService() {
    companion object {
        @Volatile
        var running: EggBoxVpnService? = null

        @Volatile
        internal var waiting: CompletableDeferred<EggBoxVpnService>? = null
    }

    private var tun: ParcelFileDescriptor? = null

    override fun onCreate() {
        super.onCreate()
        running = this
        waiting?.complete(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == SERVICE_INTERFACE) {
            // The system starts the VPN by itself: always-on VPN, as it is
            // switched on and at boot. The application connects.
            AndroidVpn.systemStarted(this)
        } else if (!AndroidVpn.wanted && tun == null) {
            stopSelf()
        }
        return START_NOT_STICKY
    }

    /** Makes the VPN interface, in place of the one before. */
    @Synchronized
    fun establish(s: TunSettings): ParcelFileDescriptor {
        val b = Builder().setSession(s.session).setMtu(s.mtu.toInt())
        for (a in s.addresses) {
            val (addr, len) = prefix(a)
            b.addAddress(addr, len)
        }
        for (r in s.routes) {
            val (addr, len) = prefix(r)
            b.addRoute(addr, len)
        }
        if (Build.VERSION.SDK_INT >= 33) {
            for (r in s.excludedRoutes) {
                val (addr, len) = prefix(r)
                b.excludeRoute(IpPrefix(addr, len))
            }
        }
        for (d in s.dns) b.addDnsServer(d)
        if (Build.VERSION.SDK_INT >= 29) b.setMetered(false)
        val next = b.establish() ?: throw NativeException("droidvpn", 2, "the application may not be the device's VPN")
        tun?.close()
        tun = next
        return next
    }

    /** Closes the VPN interface. */
    @Synchronized
    fun release() {
        tun?.close()
        tun = null
    }

    override fun onRevoke() {
        AndroidVpn.revoke("turned off in the system settings, or replaced by another VPN")
        release()
        stopSelf()
    }

    override fun onDestroy() {
        release()
        if (running === this) running = null
        super.onDestroy()
    }

    private fun prefix(s: String): Pair<InetAddress, Int> {
        val slash = s.lastIndexOf('/')
        val addr = InetAddress.getByName(s.substring(0, slash))
        return addr to s.substring(slash + 1).toInt()
    }
}

/**
 * Quick Settings tile in Android's control shade: taps toggle the VPN directly
 * (including from the lock screen once VPN consent was granted), and long
 * presses open EggBox through ACTION_QS_TILE_PREFERENCES.
 */
class EggBoxTileService : TileService() {
    companion object {
        @Volatile
        internal var listening: EggBoxTileService? = null
    }

    override fun onTileAdded() {
        super.onTileAdded()
        applyState(AndroidVpn.currentTileState())
    }

    override fun onStartListening() {
        super.onStartListening()
        listening = this
        applyState(AndroidVpn.currentTileState())
    }

    override fun onStopListening() {
        if (listening === this) listening = null
        super.onStopListening()
    }

    override fun onClick() {
        super.onClick()
        if (VpnService.prepare(this) != null) {
            // Granting VPN permission requires a visible Activity.
            if (isLocked) {
                unlockAndRun { openApp() }
            } else {
                openApp()
            }
            return
        }
        AndroidVpn.tileToggled(this)
    }

    internal fun applyState(s: TileState) {
        val tile = qsTile ?: return
        tile.label = getString(R.string.eggbox_tile_label)
        tile.icon = Icon.createWithResource(this, R.drawable.ic_eggbox_tile)
        tile.state = when (s.state) {
            "active", "connecting" -> Tile.STATE_ACTIVE
            else -> Tile.STATE_INACTIVE
        }
        if (Build.VERSION.SDK_INT >= 29) {
            tile.subtitle = s.subtitle.ifEmpty { null }
        }
        tile.updateTile()
    }

    private fun openApp() {
        val intent = packageManager.getLaunchIntentForPackage(packageName)?.apply {
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        } ?: return
        if (Build.VERSION.SDK_INT >= 34) {
            val pi = PendingIntent.getActivity(
                this,
                0,
                intent,
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
            startActivityAndCollapse(pi)
        } else {
            @Suppress("DEPRECATION")
            startActivityAndCollapse(intent)
        }
    }
}
