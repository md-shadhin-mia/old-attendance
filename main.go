package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"old-attendance/zk"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

// Define constants for headers and prefixes
const (
	contentTypeHeader   = "Content-Type"
	acceptHeader        = "Accept"
	authorizationHeader = "Authorization"
	jsonContentType     = "application/json"
	bearerPrefix        = "Bearer "

	// File to persist the last check timestamp
	lastCheckFile = "last_check.txt"
	// File to save the latest fetched logs
	logsFile = "latest_logs.json"
)

// AttendancePayload defines the structure for the data sent to the API
type AttendancePayload struct {
	OrgID string                `json:"org_id"`
	Logs  []zk.AttendanceRecord `json:"logs"`
}

func main() {
	// Load .env file from the current directory or the directory where the executable is run
	err := godotenv.Load()
	if err != nil {
		log.Println("Info: No .env file found or error loading it. Using environment variables directly.", err)
	}

	// Initial sync on startup
	log.Println("Performing initial sync...")
	performSync()

	// Set up ticker for periodic sync (interval taken from env or default to 5 minutes)
	intervalStr := os.Getenv("SYNC_INTERVAL") // int value minutes
	interval, err := time.ParseDuration(intervalStr + "m")
	if err != nil || interval <= 0 {
		interval = 5 * time.Minute
		log.Printf("Invalid or missing SYNC_INTERVAL, defaulting to %v", interval)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("Starting periodic sync every %v...", interval)

	for range ticker.C {
		log.Println("Performing scheduled sync...")
		performSync()
	}
}

// performSync handles connecting to devices, fetching logs, sending them to the API, and persisting state
func performSync() {
	log.Println("Sync process started.")

	// Load last checked time from disk
	lastChecked := getLastCheckTime()

	// Get configuration from environment variables
	deviceIPs := os.Getenv("DEVICE_IPS")
	apiURL := os.Getenv("API_URL")
	orgID := os.Getenv("ORG_ID")
	apiKey := os.Getenv("API_KEY")

	// Basic validation
	if deviceIPs == "" || apiURL == "" || orgID == "" {
		log.Println("Error: Missing required environment variables (DEVICE_IPS, API_URL, ORG_ID). Sync aborted.")
		return
	}

	ipAddresses := strings.Split(deviceIPs, ",")
	var allLogs []zk.AttendanceRecord
	var zkErrs []error
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, ipPort := range ipAddresses {
		addr := strings.TrimSpace(ipPort)
		if addr == "" {
			continue
		}
		wg.Add(1)
		go func(deviceAddr string) {
			defer wg.Done()
			parts := strings.Split(deviceAddr, ":")
			if len(parts) != 2 {
				mu.Lock()
				zkErrs = append(zkErrs, fmt.Errorf("invalid device format: %s", deviceAddr))
				mu.Unlock()
				return
			}
			ip, port := parts[0], parts[1]
			log.Printf("Connecting to device %s:%s", ip, port)

			zkManager, err := zk.NewZKManager(ip, port)
			if err != nil {
				mu.Lock()
				zkErrs = append(zkErrs, fmt.Errorf("failed to create ZKManager for %s:%s: %w", ip, port, err))
				mu.Unlock()
				return
			}

			newLogs, err := zkManager.GetAttendance(lastChecked)
			if err != nil {
				mu.Lock()
				zkErrs = append(zkErrs, fmt.Errorf("failed to get attendance from %s:%s: %w", ip, port, err))
				mu.Unlock()
				return
			}

			mu.Lock()
			if len(newLogs) > 0 {
				allLogs = append(allLogs, newLogs...)
				log.Printf("Found %d logs from %s:%s", len(newLogs), ip, port)
			} else {
				log.Printf("No new logs found from %s:%s", ip, port)
			}
			mu.Unlock()
		}(addr)
	}

	wg.Wait()

	if len(zkErrs) > 0 {
		log.Printf("Encountered %d error(s) during device communication:", len(zkErrs))
		for _, e := range zkErrs {
			log.Println("- ", e)
		}
	}

	if len(allLogs) > 0 {
		log.Printf("Total logs collected: %d. Sending to API: %s", len(allLogs), apiURL)
		err := sendLogsToAPI(allLogs, orgID, apiURL, apiKey)
		if err != nil {
			log.Println("Error sending logs to API:", err)
		} else {
			log.Println("Successfully sent logs to API.")
			// Persist logs locally
			if err := saveLogsToFile(allLogs); err != nil {
				log.Printf("Error saving logs to file: %v", err)
			}
			// Update last check timestamp
			if err := saveLastCheckTime(time.Now()); err != nil {
				log.Printf("Error saving last check time: %v", err)
			}
		}
	} else {
		log.Println("No logs collected from any device in this cycle.")
	}

	log.Println("Sync process finished.")
}

// sendLogsToAPI marshals the logs and sends them via HTTP POST
func sendLogsToAPI(logs []zk.AttendanceRecord, orgID, apiURL, apiKey string) error {
	// payload := AttendancePayload{OrgID: orgID, Logs: logs}
	jsonData, err := json.Marshal(logs)
	if err != nil {
		return fmt.Errorf("failed to marshal logs to JSON: %w", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create API request: %w", err)
	}
	req.Header.Set(contentTypeHeader, jsonContentType)
	req.Header.Set(acceptHeader, jsonContentType)
	if apiKey != "" {
		req.Header.Set(authorizationHeader, bearerPrefix+apiKey)
	}

	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("API request successful (Status: %d)", resp.StatusCode)
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
}

// getLastCheckTime reads the last check time from disk, or returns zero time
func getLastCheckTime() time.Time {
	data, err := os.ReadFile(lastCheckFile)
	if err != nil {
		log.Printf("No previous check time found: %v", err)
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		log.Printf("Invalid time in %s: %v", lastCheckFile, err)
		return time.Time{}
	}
	return t
}

// saveLastCheckTime writes the given time to disk
func saveLastCheckTime(t time.Time) error {
	return os.WriteFile(lastCheckFile, []byte(t.Format(time.RFC3339)), 0644)
}

// saveLogsToFile writes the logs to a JSON file
func saveLogsToFile(logs []zk.AttendanceRecord) error {
	data, err := json.MarshalIndent(logs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(logsFile, data, 0644)
}
