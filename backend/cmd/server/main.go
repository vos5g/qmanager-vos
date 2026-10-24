package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"qmanager-backend/pkg/api"
	"qmanager-backend/pkg/at"
	"qmanager-backend/pkg/daemon"
	"qmanager-backend/pkg/tlsgen"
	"qmanager-backend/pkg/wg"
	"qmanager-backend/web"
)

// serveWithPortRetry - v1.5.6: kalau port 80/443 udah dipegang proses lain
// (lighttpd legacy yang muncul lagi abis reboot modem), bunuh lighttpd lalu
// coba bind ulang. Sebelumnya web mati abis reboot sampai user SSH restart.
func serveWithPortRetry(srv *http.Server, tlsCert, tlsKey string) error {
	addr := srv.Addr
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if strings.Contains(err.Error(), "address already in use") {
			log.Printf("[Bind] Port %s dipakai proses lain - kill lighttpd legacy, retry...", addr)
			_ = exec.Command("/bin/sh", "-c", "killall lighttpd 2>/dev/null; sleep 1").Run()
			ln, err = net.Listen("tcp", addr)
		}
		if err != nil {
			return err
		}
	}
	log.Printf("QManager Go Backend listening on %s", addr)
	if tlsCert != "" {
		return srv.ServeTLS(ln, tlsCert, tlsKey)
	}
	return srv.Serve(ln)
}

func main() {
	// vosclean: the WireGuard tunnel service runs from this same binary as its
	// own systemd unit (vos-wg), so a QManager restart doesn't drop the tunnel.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "wg-run":
			os.Exit(wg.Run())
		case "wg-show":
			if err := wg.Show(os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "wg-cleanup":
			wg.Cleanup()
			wg.FlushConntrack()
			return
		}
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "80"
	}

	tlsPort := os.Getenv("TLS_PORT")
	if tlsPort == "" {
		tlsPort = "443"
	}

	webRoot := os.Getenv("WEB_ROOT")
	tlsEnabled := os.Getenv("TLS_ENABLED") != "false" // enabled by default unless explicitly disabled

	mux := http.NewServeMux()

	// Initialize AT Client
	atClient := at.NewClient("/dev/smd11")

	// Start Background Poller Daemon (runs continuously without spawning subshells)
	poller := daemon.NewPoller(atClient, 10*time.Second)
	poller.Start()
	log.Println("[Daemon] Background Status Poller started (10s interval)")

	// Start Background Watchdog Daemon (monitors connectivity natively)
	// v1.5.4: fix "15 menit baru konek" - watchdog over-escalation.
	//  - probe 15s, multi-host (1.1.1.1 + 8.8.8.8 via defaultPing)
	//  - soft recover di 2 gagal, TAPI soft backoff 180s (jangan spam CFUN -
	//    modem butuh waktu tenang buat re-attach sendiri abis provider refresh IP)
	//  - hard reboot BARU di 10 gagal (~2.5 menit LOS beneran) + cooldown 300s:
	//    refresh IP biasa (30-60s) gak akan pernah kena reboot
	//  - single-flight (recovery gak numpuk)
	// vosclean: the watchdog follows the Watchdog page (interval, threshold,
	// enabled steps) and recovers with actions that work on this stick:
	// data-connection restart, radio restart, then a capped stick restart.
	// Its probe bypasses WireGuard, so a VPN outage can't trigger it.
	watchdog := daemon.NewWatchdog("1.1.1.1", 10*time.Second, 5)
	watchdog.SetConfig(api.WatchdogConfig)
	recovery := daemon.NewRecovery(atClient, api.WatchdogConfig)
	watchdog.SetCheckHook(recovery.OnCheck)
	watchdog.Start()
	api.StartDataUsage()
	api.StartNotifier()
	log.Println("[Daemon] Background Watchdog started (settings from the Watchdog page)")

	// Register API Routes
	server := api.NewServer(atClient)
	server.RegisterRoutes(mux)

	// Restore persisted "remember me" sessions from disk (survives reboots).
	api.LoadSessionsFromDisk()

	// API Health Check
	mux.HandleFunc("/cgi-bin/quecmanager/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","arch":"%s","os":"%s"}`, runtime.GOARCH, runtime.GOOS)
	})

	// Static File Server: Embedded FS or Disk Fallback
	if webRoot != "" {
		log.Printf("[Web] Serving static assets from disk path: %s", webRoot)
		diskFS := http.Dir(webRoot)
		fileServer := http.FileServer(diskFS)
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := filepath.Join(webRoot, filepath.Clean(r.URL.Path))
			info, err := os.Stat(path)
			if err != nil && os.IsNotExist(err) {
				http.ServeFile(w, r, filepath.Join(webRoot, "index.html"))
				return
			}
			if err == nil && info.IsDir() {
				http.ServeFile(w, r, filepath.Join(path, "index.html"))
				return
			}
			fileServer.ServeHTTP(w, r)
		}))
	} else {
		log.Println("[Web] Serving static assets from embedded binary (embed.FS all:out)")
		mux.Handle("/", web.ServeEmbeddedWeb())
	}

	httpServer := &http.Server{
		Addr:    ":" + port,
		Handler: auditHandler(mux),
	}

	// Live bandwidth WebSocket server (Go-native, replaces websocat bridge).
	// Reads ws_port from qmanager.conf bridge_monitor; defaults to 8838.
	server.StartBandwidthWebSocket(0)

	var tlsServer *http.Server

	// Setup HTTPS TLS Certificates if enabled
	if tlsEnabled {
		certPath, keyPath, err := tlsgen.EnsureCertificates("")
		if err != nil {
			log.Printf("[TLS] Warning: Failed to initialize TLS certs: %v", err)
		} else {
			log.Printf("[TLS] HTTPS Enabled on port %s (cert: %s)", tlsPort, certPath)
			tlsServer = &http.Server{
				Addr:    ":" + tlsPort,
				Handler: auditHandler(mux),
			}
			go func() {
				if err := serveWithPortRetry(tlsServer, certPath, keyPath); err != nil && err != http.ErrServerClosed {
					log.Printf("[TLS] HTTPS server stopped: %v", err)
				}
			}()
		}
	}

	// Channel to listen for OS signals (SIGINT, SIGTERM)
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := serveWithPortRetry(httpServer, "", ""); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()

	// Block until signal is received
	sig := <-stopChan
	log.Printf("[System] Received OS signal: %v. Initiating graceful shutdown...", sig)

	// Create a deadline context for shutdown (5 seconds max)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Stop background daemons
	watchdog.Stop()
	api.SaveDataUsage()

	// Shutdown HTTPS server if running
	if tlsServer != nil {
		if err := tlsServer.Shutdown(ctx); err != nil {
			log.Printf("[TLS] Warning: HTTPS shutdown error: %v", err)
		}
	}

	// Shutdown HTTP server
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("[HTTP] Warning: HTTP shutdown error: %v", err)
	}

	log.Println("[System] QManager Go Core Daemon stopped cleanly.")
}

// auditHandler logs every state-changing request (method, path, client) to
// /data/vosclean/log/qm-requests.log, so actions such as a reboot can later be
// traced back to the UI. Bodies are not logged (they may hold passwords).
func auditHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			const auditLog = "/data/vosclean/log/qm-requests.log"
			if st, err := os.Stat(auditLog); err == nil && st.Size() > 256*1024 {
				_ = os.Rename(auditLog, auditLog+".1") // keep two generations, at most ~512 KB on flash
			}
			if f, err := os.OpenFile(auditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
				fmt.Fprintf(f, "%s %s %s from %s\n", time.Now().UTC().Format("2006-01-02 15:04:05"), r.Method, r.URL.Path, r.RemoteAddr)
				f.Close()
			}
		}
		next.ServeHTTP(w, r)
	})
}
