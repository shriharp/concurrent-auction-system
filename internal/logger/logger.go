package logger

import (
	"log"
	"log/syslog"
	"os"

	"fisac-auction/internal/db"
)

var SysLogger *log.Logger

// InitLogger initializes local stdout and tries to dial the local syslog daemon.
func InitLogger() {
	// Standard logger
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Syslog
	sysLog, err := syslog.NewLogger(syslog.LOG_NOTICE, log.LstdFlags)
	if err == nil {
		SysLogger = sysLog
		log.Println("Syslog connected successfully.")
	} else {
		log.Printf("Failed to connect to syslog: %v. Running without syslog.", err)
		SysLogger = log.New(os.Stdout, "SYSLOG_FALLBACK: ", log.LstdFlags)
	}
}

// LogEvent is a wrapper providing dual-logging (Syslog and Database persistent logs)
func LogEvent(eventType string, msg string) {
	fullMessage := eventType + " - " + msg
	log.Println(fullMessage)
	if SysLogger != nil {
		SysLogger.Println(fullMessage)
	}
	db.LogEvent(eventType, msg) // Persistent DB audit
}
