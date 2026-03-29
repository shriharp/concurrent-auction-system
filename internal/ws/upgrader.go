package ws

import (
	"encoding/json"
	"fisac-auction/internal/auth"
	"fisac-auction/internal/logger"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Allowing all origins for simple HTML demo purposes
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ServeWs handles websocket requests from the peer.
func ServeWs(hub *Hub, w http.ResponseWriter, r *http.Request, submitBidFunc func(auctionID, userID, amount int), getCurrentState func(auctionID int) int) {
	// Authentication Phase (URL approach for WS)
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		http.Error(w, "Unauthorized: No token provided", http.StatusUnauthorized)
		return
	}

	userID, err := auth.ValidateToken(tokenStr)
	if err != nil {
		logger.LogEvent("AUTH_FAILED", "Invalid token attempt")
		http.Error(w, "Unauthorized: Invalid token", http.StatusUnauthorized)
		return
	}

	// Upgrade the connection
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	// Socket tuning is already performed partially by Gorilla WebSocket,
	// but TCP connections backing it have TCP_NODELAY = true set by default in Go HTTP server.

	client := &Client{
		Hub:           hub,
		Conn:          conn,
		Send:          make(chan []byte, 256),
		UserID:        userID,
		SubmitBidFunc: submitBidFunc,
	}
	client.Hub.Register <- client

	// Hardcoded push of Auction #1's state for the UI test
	if val := getCurrentState(1); val > 0 {
		msg := map[string]interface{}{
			"type":       "NEW_HIGHEST_BID",
			"auction_id": 1,
			"amount":     val,
			"winner_id":  0, // We hide the past winner to new peers
		}
		b, _ := json.Marshal(msg)
		client.Send <- b
	}

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go client.WritePump()
	go client.ReadPump()
}
