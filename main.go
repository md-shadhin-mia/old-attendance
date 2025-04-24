package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io" // Added io import
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
		// It's often fine if .env doesn't exist, especially in production where env vars are set directly
		log.Println("Info: No .env file found or error loading it. Using environment variables directly.", err)
	}

	// Initial sync on startup
	log.Println("Performing initial sync...")
	performSync()

	// Set up ticker for periodic sync (every 5 minutes)
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	log.Println("Starting periodic sync every 5 minutes...")
	// Loop indefinitely, waiting for the ticker
	for range ticker.C {
		log.Println("Performing scheduled sync...")
		performSync()
	}
}

// performSync handles the process of connecting to devices, fetching logs, and sending them to the API
func performSync() {
	log.Println("Sync process started.")

	// Get configuration from environment variables
	deviceIPs := os.Getenv("DEVICE_IPS") // Comma-separated: "IP1:PORT1,IP2:PORT2"
	apiURL := os.Getenv("API_URL")
	orgID := os.Getenv("ORG_ID")
	// Optional: API Key if needed
	apiKey := os.Getenv("API_KEY")

	// Basic validation of required config
	if deviceIPs == "" || apiURL == "" || orgID == "" {
		log.Println("Error: Missing required environment variables (DEVICE_IPS, API_URL, ORG_ID). Sync aborted.")
		return
	}

	ipAddresses := strings.Split(deviceIPs, ",")
	if len(ipAddresses) == 0 || (len(ipAddresses) == 1 && ipAddresses[0] == "") {

		log.Println("Error: No device IPs configured in DEVICE_IPS. Sync aborted.")
		return
	}

	log.Printf("Found %d device(s) to sync.", len(ipAddresses))

	var allLogs []zk.AttendanceRecord
	var zkErrs []error
	var wg sync.WaitGroup
	var mu sync.Mutex // Mutex to protect shared slices (allLogs, zkErrs)

	// For this CLI version, we fetch all records each time.
	// A more stateful version might store the last successful sync time per device.
	lastChecked := time.Time{} // Zero time value fetches all records

	for _, ipPort := range ipAddresses {
		// Ensure ipPort is not empty string which can happen with trailing commas
		trimmedIpPort := strings.TrimSpace(ipPort)
		if trimmedIpPort == "" {
			continue
		}

		wg.Add(1)
		go func(deviceAddr string) {
			defer wg.Done()
			parts := strings.Split(deviceAddr, ":")
			if len(parts) != 2 {
				log.Printf("Error: Invalid device configuration format: '%s'. Expected IP:Port. Skipping.", deviceAddr)
				mu.Lock()
				zkErrs = append(zkErrs, fmt.Errorf("invalid device format: %s", deviceAddr))
				mu.Unlock()
				return
			}
			ip := parts[0]
			port := parts[1]
			log.Printf("Connecting to device %s:%s", ip, port)

			// Create a new ZKManager instance for each connection attempt
			zkManager, err := zk.NewZKManager(ip, port)
			if err != nil {
				log.Printf("Failed to create ZKManager for %s:%s: %v", ip, port, err)
				mu.Lock()
				zkErrs = append(zkErrs, fmt.Errorf("failed to create ZKManager for %s:%s: %w", ip, port, err))
				mu.Unlock()
				return
			}
			log.Printf("Fetching attendance logs from %s:%s", ip, port)
			newLogs, err := zkManager.GetAttendance(lastChecked)
			if err != nil {
				log.Printf("Failed to get attendance from %s:%s: %v", ip, port, err)
				mu.Lock()
				zkErrs = append(zkErrs, fmt.Errorf("failed to get attendance from %s:%s: %w", ip, port, err))
				mu.Unlock()
				return // Stop processing for this device on error
			}

			// Lock mutex before appending to the shared slice
			mu.Lock()
			if len(newLogs) > 0 {
				allLogs = append(allLogs, newLogs...)
				log.Printf("Found %d logs from %s:%s", len(newLogs), ip, port)
			} else {
				log.Printf("No new logs found from %s:%s", ip, port)
			}
			mu.Unlock()

		}(trimmedIpPort)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Log any errors encountered during device communication
	if len(zkErrs) > 0 {
		log.Printf("Encountered %d error(s) during ZK device communication:", len(zkErrs))
		for _, zkErr := range zkErrs {
			log.Println("- ", zkErr)
		}
		// Continue even if some devices failed, maybe partial data is better than none
	}

	// Send collected logs to the API if any were found
	if len(allLogs) > 0 {
		log.Printf("Total logs collected: %d. Sending to API: %s", len(allLogs), apiURL)
		err := sendLogsToAPI(allLogs, orgID, apiURL, apiKey) // Pass apiKey if needed
		if err != nil {
			log.Println("Error sending logs to API:", err)
		} else {
			log.Println("Successfully sent logs to API.")
			// Future enhancement: Update last sync timestamp here if implementing stateful sync
		}
	} else {
		log.Println("No logs collected from any device in this cycle.")
	}

	log.Println("Sync process finished.")
}

// sendLogsToAPI marshals the logs and sends them via HTTP POST to the configured API endpoint
func sendLogsToAPI(logs []zk.AttendanceRecord, orgID string, apiURL string, apiKey string) error {
	payload := AttendancePayload{
		OrgID: orgID,
		Logs:  logs,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal logs to JSON: %w", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create API request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Example for adding an API Key header (uncomment and adjust if needed)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	// Create an HTTP client with a timeout
	client := &http.Client{Timeout: 45 * time.Second} // Increased timeout for potentially large payloads

	// Execute the request
	resp, err := client.Do(req)
	if err != nil {
		// Network errors, timeouts, etc.
		return fmt.Errorf("failed to execute API request: %w", err)
	}
	defer resp.Body.Close() // Ensure the response body is closed

	// Check the response status code
	if resp.StatusCode >= 200 && resp.StatusCode < 300 { // Success range (2xx)
		log.Printf("API request successful (Status: %d)", resp.StatusCode)
		// Optionally read and log success response body if needed
		// bodyBytes, _ := io.ReadAll(resp.Body)
		// log.Printf("API Success Response: %s", string(bodyBytes))
		return nil
	} else {
		// Read the error response body for more details
		bodyBytes, readErr := io.ReadAll(resp.Body)
		bodyString := ""
		if readErr == nil {
			bodyString = string(bodyBytes)
		} else {
			bodyString = fmt.Sprintf("(could not read response body: %v)", readErr)
		}
		log.Printf("API request failed. Status: %d, Response: %s", resp.StatusCode, bodyString)
		return fmt.Errorf("API request failed with status code %d. Response: %s", resp.StatusCode, bodyString)
	}
}
