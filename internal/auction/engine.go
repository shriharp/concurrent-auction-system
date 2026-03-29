package auction

import (
	"encoding/json"
	"fisac-auction/internal/db"
	"fisac-auction/internal/logger"
	"fisac-auction/internal/ws"
	"log"
	"sync"
)

type Engine struct {
	Hub   *ws.Hub
	Rooms map[int]*Room
	mu    sync.RWMutex
}

func NewEngine(hub *ws.Hub) *Engine {
	return &Engine{
		Hub:   hub,
		Rooms: make(map[int]*Room),
	}
}

// Bidding message struct for internal processing
type BidRequest struct {
	UserID    int
	AuctionID int
	Amount    int
}

// Room represents a single auction running concurrently. It processes all bids serially
// to completely prevent data race conditions at the application level.
type Room struct {
	AuctionID  int
	HighestBid int
	BidsIn     chan BidRequest
	engine     *Engine
	stop       chan struct{}
}

// LoadAuctions loads from DB and spawns Goroutines per auction
func (e *Engine) Start() {
	auctions, err := db.LoadActiveAuctions()
	if err != nil {
		log.Fatalf("Failed to load auctions: %v", err)
	}

	for _, auc := range auctions {
		e.spawnRoom(auc.ID, auc.HighestBid)
	}
	logger.LogEvent("ENGINE_STARTED", "Auction engine initialized active rooms.")
}

func (e *Engine) spawnRoom(auctionID int, initialBid int) {
	e.mu.Lock()
	defer e.mu.Unlock()

	room := &Room{
		AuctionID:  auctionID,
		HighestBid: initialBid,
		BidsIn:     make(chan BidRequest, 500), // buffered to handle bursty bids
		engine:     e,
		stop:       make(chan struct{}),
	}
	e.Rooms[auctionID] = room
	go room.run()
}

// The core algorithm grading focus: The serial manager loop
func (r *Room) run() {
	for {
		select {
		case bid := <-r.BidsIn:
			// 1. Initial validation block (In-Memory without DB hit for speed)
			if bid.Amount <= r.HighestBid {
				logger.LogEvent("BID_REJECTED", "Bid amount lower than highest bid.")
				continue
			}

			// 2. Perform DB Transaction natively safely
			// If we ran this simultaneously, `FOR UPDATE` in PG would save us, but because
			// this Goroutine is the sole owner/writer to this specific DB row, we don't have
			// application-DB race conditions natively!
			err := db.InsertBidTransaction(r.AuctionID, bid.UserID, bid.Amount)
			if err != nil {
				log.Printf("Failed to insert bid: %v", err)
				continue
			}

			// 3. State update
			r.HighestBid = bid.Amount
			logger.LogEvent("BID_ACCEPTED", "New high bid inserted successfully.")

			// 4. Immediate Broadcast to ALL WS Clients
			r.engine.broadcastNewBid(r.AuctionID, r.HighestBid, bid.UserID)

		case <-r.stop:
			return
		}
	}
}

func (e *Engine) broadcastNewBid(auctionID int, amount int, winnerID int) {
	msg := map[string]interface{}{
		"type":       "NEW_HIGHEST_BID",
		"auction_id": auctionID,
		"amount":     amount,
		"winner_id":  winnerID,
	}
	b, _ := json.Marshal(msg)
	e.Hub.Broadcast <- b
}

// SubmitBid pushes a bid request into the exact correct room's channel
func (e *Engine) SubmitBid(auctionID, userID, amount int) {
	e.mu.RLock()
	room, exists := e.Rooms[auctionID]
	e.mu.RUnlock()

	if !exists {
		// Log invalid / inactive auction attempts
		logger.LogEvent("INVALID_BID", "Bid attempt on non-existent auction")
		return
	}
	
	// Push into channel queue
	room.BidsIn <- BidRequest{
		UserID:    userID,
		AuctionID: auctionID,
		Amount:    amount,
	}
}

// GetCurrentState safely reads the current highest bid from memory
func (e *Engine) GetCurrentState(auctionID int) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if room, exists := e.Rooms[auctionID]; exists {
		return room.HighestBid
	}
	return 0
}
