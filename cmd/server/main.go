package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"fisac-auction/internal/auction"
	"fisac-auction/internal/auth"
	"fisac-auction/internal/db"
	"fisac-auction/internal/logger"
	"fisac-auction/internal/ws"
)

func main() {
	// Initialize subsystems
	logger.InitLogger()
	logger.LogEvent("SYSTEM_START", "Starting fisac-auction server")

	dsn := "host=127.0.0.1 user=auction_user password=password dbname=auction_db port=5433 sslmode=disable"
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		dsn = dbURL
	}
	db.ConnectDB(dsn)

	// Startup Hub and Core Engine
	hub := ws.NewHub()
	go hub.Run()

	engine := auction.NewEngine(hub)
	engine.Start()

	// --- HTTP Handlers ---

	// 1. WebSocket Entrypoint (Upgrades HTTP -> WS after Token Auth)
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws.ServeWs(hub, w, r, engine.SubmitBid, engine.GetCurrentState)
	})

	// 2. Demo Utility: Generate a testing JWT Token
	http.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		userID := 1 // Hardcoded for demo
		username := r.URL.Query().Get("user")
		if username == "bob" {
			userID = 2
		} else if username == "charlie" {
			userID = 3
		} else if username == "" {
			username = "alice"
		}

		token, err := auth.GenerateToken(userID, username)
		if err != nil {
			http.Error(w, "Token Gen failed", 500)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":    token,
			"user_id":  userID,
			"username": username,
		})
	})

	// 3. Serve Frontend Testing Page
	http.Handle("/", http.FileServer(http.Dir("./public")))

	// --- Advanced SO Configuration & Bootstrapping ---
	port := "8443"

	// Creating a custom TCP Listener to explicitly show SO_REUSEADDR and TCP_NODELAY features
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			err := c.Control(func(fd uintptr) {
				// SO_REUSEADDR allows quick restart of server to avoid "address already in use"
				// Note: Go typically does this automatically on Unix, but this makes it explicit.
				opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
			if err != nil {
				return err
			}
			return opErr
		},
	}

	listener, err := lc.Listen(context.Background(), "tcp", ":"+port)
	if err != nil {
		log.Fatalf("Listen failed: %v", err)
	}

	server := &http.Server{
		Handler:      http.DefaultServeMux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  120 * time.Second,
		// Custom ConnState to configure accepted sockets
		ConnState: func(c net.Conn, state http.ConnState) {
			if state == http.StateNew {
				// We can type-assert to *net.TCPConn
				if tcpConn, ok := c.(*net.TCPConn); ok {
					// Disables Nagle's Algorithm (this is TRUE by default in Go actually!)
					tcpConn.SetNoDelay(true)
					// TCP Keepalives detect dead peers
					tcpConn.SetKeepAlive(true)
					tcpConn.SetKeepAlivePeriod(3 * time.Minute)
				}
			}
		},
	}

	logger.LogEvent("TLS_STARTUP", fmt.Sprintf("Listening with TLS on port %s", port))
	// Because this is a demo, requires standard local certs:
	// Let's use standard HTTP for local dev loop if certs aren't generated yet.
	if _, err := os.Stat("certs/cert.pem"); err == nil {
		log.Fatal(server.ServeTLS(listener, "certs/cert.pem", "certs/key.pem"))
	} else {
		log.Println("WARNING: No certs found. Falling back to HTTP on 8080.")
		server.Addr = ":8080"
		log.Fatal(server.ListenAndServe())
	}
}
