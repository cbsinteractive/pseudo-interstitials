package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Struct to store the received message and timestamp
type MsgData struct {
	Received time.Time `json:"received"`
	Message  string    `json:"message"`
}

var (
	mu       sync.RWMutex // Package-level mutex
	messages []MsgData    // Package-level variable to store messages
)

func main() {
	// Start the UDP listener in a separate goroutine
	go startUDPListener()

	// Start the HTTP server on port 8084 with CORS handling
	http.HandleFunc("/status", corsMiddleware(statusHandler))
	fmt.Println("HTTP server listening on port 8084")
	if err := http.ListenAndServe(":8084", nil); err != nil {
		panic(err)
	}
}

func startUDPListener() {
	addr, err := net.ResolveUDPAddr("udp", ":1234")
	if err != nil {
		panic(err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	fmt.Println("UDP server listening on port 1234")

	for {
		buf := make([]byte, 1024)
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			fmt.Println("Error reading:", err)
			continue
		}

		message := string(buf[:n])
		fmt.Printf("Received message: %s from %s\n", message, addr)

		// Append the message directly to the messages slice
		mu.Lock()
		messages = append(messages, MsgData{
			Received: time.Now(), // Record the current time
			Message:  message,
		})
		mu.Unlock()

		// Respond with an "ACK"
		response := []byte("ACK")
		_, err = conn.WriteToUDP(response, addr)
		if err != nil {
			fmt.Println("Error writing:", err)
		}
	}
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	// Return just the array of MsgData
	mu.RLock()
	defer mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

// Middleware to handle CORS
func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Set the necessary headers for CORS
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		// Handle preflight (OPTIONS) request
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Call the next handler
		next.ServeHTTP(w, r)
	}
}
