package db

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/lib/pq"
)

var DB *sql.DB

// ConnectDB initializes the PostgreSQL connection
func ConnectDB(dsn string) {
	var err error
	DB, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}

	// Ping with retry since docker compose might take a few seconds
	for i := 0; i < 5; i++ {
		err = DB.Ping()
		if err == nil {
			log.Println("Database connection established")
			return
		}
		log.Printf("DB not ready... retrying in 2s (%v)", err)
		time.Sleep(2 * time.Second)
	}
	log.Fatalf("Failed to connect to DB after retries")
}

// LogEvent writes a system event to the audit_logs table
// This acts as our persistent logging mechanism alongside syslog
func LogEvent(eventType, description string) {
	if DB == nil {
		return
	}
	_, err := DB.Exec("INSERT INTO audit_logs (event_type, description) VALUES ($1, $2)", eventType, description)
	if err != nil {
		log.Printf("Failed to write to audit log: %v", err)
	}
}

// LoadAuctions loads all active auctions
func LoadActiveAuctions() ([]AuctionData, error) {
	rows, err := DB.Query("SELECT id, item_name, highest_bid, status FROM auctions WHERE status = 'ACTIVE'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var auctions []AuctionData
	for rows.Next() {
		var a AuctionData
		err := rows.Scan(&a.ID, &a.ItemName, &a.HighestBid, &a.Status)
		if err != nil {
			continue
		}
		auctions = append(auctions, a)
	}
	return auctions, nil
}

type AuctionData struct {
	ID         int
	ItemName   string
	HighestBid int
	Status     string
}

// InsertBidTransaction inserts a bid and updates the highest bid in a single SQL transaction.
// It uses FOR UPDATE to ensure no race conditions at the database level.
func InsertBidTransaction(auctionID int, userID int, amount int) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Lock the auction row
	var currentHighest int
	err = tx.QueryRow("SELECT highest_bid FROM auctions WHERE id = $1 FOR UPDATE", auctionID).Scan(&currentHighest)
	if err != nil {
		return fmt.Errorf("failed to lock auction: %v", err)
	}

	// 2. Validate amount again
	if amount <= currentHighest {
		return fmt.Errorf("bid too low")
	}

	// 3. Update auction
	_, err = tx.Exec("UPDATE auctions SET highest_bid = $1, highest_bidder_id = $2 WHERE id = $3", amount, userID, auctionID)
	if err != nil {
		return fmt.Errorf("failed to update auction: %v", err)
	}

	// 4. Insert bid
	_, err = tx.Exec("INSERT INTO bids (auction_id, user_id, amount) VALUES ($1, $2, $3)", auctionID, userID, amount)
	if err != nil {
		return fmt.Errorf("failed to insert bid: %v", err)
	}

	return tx.Commit()
}
