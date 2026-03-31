package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Target configurations (Change port to 8443 and scheme to wss if testing with TLS certificates)
const (
	serverAddr = "localhost:8443"
	wsScheme   = "wss"
	httpScheme = "https"
	numClients = 50
	bidsPerClient = 20
)

var (
	successfulBids int32
	failedBids     int32
)

func main() {
	fmt.Printf("Starting Concurrency Load Test with %d Concurrent Clients...\n", numClients)

	// Since we might be hitting a demo server with self-signed certs, disable verification
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	var wg sync.WaitGroup
	startTime := time.Now()

	for i := 1; i <= numClients; i++ {
		wg.Add(1)
		go simulateClient(i, &wg)
	}

	wg.Wait()
	elapsed := time.Since(startTime)

	fmt.Println("\n--- Load Test Results ---")
	fmt.Printf("Total Time Elapsed: %v\n", elapsed)
	fmt.Printf("Total Concurrent Clients: %d\n", numClients)
	fmt.Printf("Total Bids Sent: %d\n", numClients*bidsPerClient)
	fmt.Printf("Server Accepted WebSocket Frames: %d\n", successfulBids)
	fmt.Printf("Failed Connections/Sends: %d\n", failedBids)
	fmt.Println("Check the PostgreSQL 'audit_logs' table to verify how many of these bids actually won the strict DB Row-Lock!")
}

func simulateClient(clientID int, wg *sync.WaitGroup) {
	defer wg.Done()

	// 1. Authenticate and get JWT Token
	authURL := fmt.Sprintf("%s://%s/auth/signup", httpScheme, serverAddr)
	payloadStr := fmt.Sprintf(`{"username":"testuser_%d","password":"password"}`, clientID)
	resp, err := http.Post(authURL, "application/json", strings.NewReader(payloadStr))
	if err != nil || resp.StatusCode != 200 {
		// If already exists, try signin
		if resp != nil {
			resp.Body.Close()
		}
		signinURL := fmt.Sprintf("%s://%s/auth/signin", httpScheme, serverAddr)
		resp, err = http.Post(signinURL, "application/json", strings.NewReader(payloadStr))
		if err != nil || resp.StatusCode != 200 {
			log.Printf("Client %d Auth failed: err=%v status=%d", clientID, err, resp.StatusCode)
			atomic.AddInt32(&failedBids, int32(bidsPerClient))
			if resp != nil { resp.Body.Close() }
			return
		}
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var authData struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &authData); err != nil {
		log.Printf("Client %d JSON parse failed: %v", clientID, err)
		atomic.AddInt32(&failedBids, int32(bidsPerClient))
		return
	}

	// 2. Connect to WebSocket
	u := url.URL{Scheme: wsScheme, Host: serverAddr, Path: "/ws", RawQuery: "token=" + authData.Token}
	dialer := websocket.DefaultDialer
	dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		log.Printf("Client %d WS Dial failed: %v", clientID, err)
		atomic.AddInt32(&failedBids, int32(bidsPerClient))
		return
	}
	defer conn.Close()

	// 3. Spam Concurrency Bids!
	for b := 1; b <= bidsPerClient; b++ {
		// Calculate a random looking bid amounts so they race against each other
		bidAmount := 1050 + (clientID * 10) + b

		payload := map[string]interface{}{
			"auction_id": 1,
			"amount":     bidAmount,
		}

		err := conn.WriteJSON(payload)
		if err != nil {
			atomic.AddInt32(&failedBids, 1)
		} else {
			atomic.AddInt32(&successfulBids, 1)
		}
		
		// Micro-sleep to prevent completely flooding local socket buffer instantly
		time.Sleep(10 * time.Millisecond)
	}
}
