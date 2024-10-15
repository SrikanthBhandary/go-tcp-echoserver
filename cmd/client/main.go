package main

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	ADDRESS             = "127.0.0.1:8080"
	CLIENT_COUNT        = 1000  // Number of concurrent clients
	MESSAGES_PER_CLIENT = 10000 // Number of messages each client will send
)

func main() {
	var wg sync.WaitGroup

	// Wait for a short time to allow the server to start
	time.Sleep(1 * time.Second)

	// Launch multiple clients
	for i := 0; i < CLIENT_COUNT; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			// Connect to the server
			conn, err := net.Dial("tcp", ADDRESS)
			if err != nil {
				fmt.Printf("Client %d: Error connecting to server: %v\n", clientID, err)
				return
			}
			defer conn.Close()

			// Send multiple messages
			for j := 0; j < MESSAGES_PER_CLIENT; j++ {
				message := fmt.Sprintf("Client %d Message %d", clientID, j)
				_, err := conn.Write([]byte(message))
				if err != nil {
					fmt.Printf("Client %d: Error sending message: %v\n", clientID, err)
					return
				}

				// Read the response
				buffer := make([]byte, 1024) // Use a larger buffer if needed
				n, err := conn.Read(buffer)
				if err != nil {
					fmt.Printf("Client %d: Error reading response: %v\n", clientID, err)
					return
				}

				response := strings.TrimSpace(string(buffer[:n]))
				expectedResponse := strings.ToUpper(message)

				if response != expectedResponse {
					fmt.Printf("Client %d: expected response %q, got %q\n", clientID, expectedResponse, response)
				} else {
					fmt.Printf("Client %d: received expected response %q\n", clientID, response)
				}
			}
		}(i)
	}

	wg.Wait() // Wait for all clients to finish
	fmt.Println("All clients have finished.")
}
