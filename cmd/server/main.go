package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

const (
	BUFFER_SIZE = 1024
	BACKLOG     = 128
)

var totalConnections map[int]bool
var mu sync.Mutex     // For safely modifying totalConnections
var wg sync.WaitGroup // WaitGroup to wait for connection handlers
var isShuttingDown bool

// kqueue syscall wrapper
func createKqueue() (int, error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return -1, err
	}
	return kq, nil
}

// registerFile for kqueue
func registerFile(kq int, fd int) error {
	kevent := syscall.Kevent_t{
		Ident:  uint64(fd),
		Filter: syscall.EVFILT_READ,
		Flags:  syscall.EV_ADD | syscall.EV_ENABLE,
		Data:   0,
		Udata:  nil,
	}

	_, err := syscall.Kevent(kq, []syscall.Kevent_t{kevent}, nil, nil)
	return err
}

// eventLoop processes kqueue events
func eventLoop(ctx context.Context, fd int, kq int) {
	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down event loop due to context cancellation")
			return
		default:
			// Process kqueue events
			events := make([]syscall.Kevent_t, 100)
			n, err := syscall.Kevent(kq, nil, events, nil)
			if err != nil {
				log.Fatalf("Error waiting for events: %v", err)
			}

			for i := 0; i < n; i++ {
				if events[i].Ident == uint64(fd) {
					// Check if we are shutting down
					if isShuttingDown {
						log.Println("Server is shutting down, not accepting new connections")
						return
					}
					// Accept a new connection
					connFD, _, err := syscall.Accept(fd)
					if err != nil {
						log.Printf("Error accepting connection: %v", err)
						continue
					}

					// Set the accepted connection to non-blocking
					if err := syscall.SetNonblock(connFD, true); err != nil {
						log.Printf("Error setting connection %d to non-blocking: %v", connFD, err)
						syscall.Close(connFD)
						continue
					}

					// Add connection to totalConnections map
					mu.Lock()
					totalConnections[connFD] = true
					mu.Unlock()

					// Register the new connection with kqueue
					if err := registerFile(kq, connFD); err != nil {
						log.Printf("Error registering connection with kqueue: %v", err)
						syscall.Close(connFD) // Close connection on error
						mu.Lock()
						delete(totalConnections, connFD)
						mu.Unlock()
						continue
					}
					wg.Add(1) // Increment WaitGroup counter

					// Start a goroutine to handle the new connection
					go handleConnection(ctx, connFD, kq)
				}
			}
		}
	}
}

func main() {
	// Create TCP socket using syscall
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		log.Fatalf("Error creating socket: %v", err)
	}
	defer syscall.Close(fd)

	// Set socket options
	var opt int = 1
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, opt); err != nil {
		log.Fatalf("Error setting socket options: %v", err)
	}

	if err := syscall.SetNonblock(fd, true); err != nil {
		log.Fatalf("Error setting non-blocking mode: %v", err)
	}

	// Bind socket to an address
	address := &syscall.SockaddrInet4{Port: 8080}
	copy(address.Addr[:], net.ParseIP("127.0.0.1").To4())

	if err := syscall.Bind(fd, address); err != nil {
		log.Fatalf("Error binding socket: %v", err)
	}

	// Listen for incoming connections
	if err := syscall.Listen(fd, BACKLOG); err != nil {
		log.Fatalf("Error listening on socket: %v", err)
	}

	// Create kqueue
	kq, err := createKqueue()
	if err != nil {
		log.Fatalf("Error creating kqueue: %v", err)
	}

	// Register listener file descriptor with kqueue
	if err := registerFile(kq, fd); err != nil {
		log.Fatalf("Error registering file with kqueue: %v", err)
	}

	// Create a context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure context is cancelled on exit

	// Handle termination signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Received termination signal, shutting down...")
		isShuttingDown = true // Set shutdown flag

		cancel()           // Signal shutdown
		closeConnections() // Close all active connections

		log.Println("All connections closed, exiting")
		os.Exit(0)
	}()

	totalConnections = make(map[int]bool)

	// Start the event loop in a goroutine
	go eventLoop(ctx, fd, kq)

	// Keep the main function running, you can add other tasks here if needed
	<-ctx.Done()
	wg.Wait() // Wait for all connections to finish
	log.Println("Main function exiting due to context cancellation")

}

// handleConnection reads data from the connection
func handleConnection(ctx context.Context, fd int, kq int) {
	defer wg.Done()         // Decrement WaitGroup counter
	defer syscall.Close(fd) // Ensure the connection is closed when done

	events := make([]syscall.Kevent_t, 1)
	buffer := make([]byte, BUFFER_SIZE)

	for {
		select {
		case <-ctx.Done():
			//log.Printf("Context cancelled, closing connection %d", fd)
			return
		default:
			// Wait for the socket to be ready for reading using kqueue
			n, err := syscall.Kevent(kq, nil, events, nil)
			if err != nil {
				log.Printf("Error in kqueue event: %v", err)
				return
			}

			// If there is data to read
			if n > 0 {
				n, err := syscall.Read(fd, buffer)
				if err != nil {
					if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK || err == syscall.EBADF {
						continue // Resource temporarily unavailable, no data to read
					}
					log.Printf("Error while reading: %v", err)
					return
				}

				if n > 0 {
					// Echo back the data
					_, err := syscall.Write(fd, []byte(convertStringToUpper(string(buffer[:n]))))
					if err != nil {
						log.Printf("Error writing to connection: %v", err)
						return
					}
				} else {
					// Client has disconnected
					mu.Lock()
					log.Printf("Client %d disconnected", fd)
					if err := syscall.Close(fd); err != nil {
						log.Printf("client %d already closed", fd)
					}
					delete(totalConnections, fd)
					mu.Unlock()
					return // Exit when the client disconnects
				}
			}
		}
	}
}

func convertStringToUpper(lowerData string) string {
	return strings.ToUpper(lowerData)
}

// Close all connections safely
func closeConnections() {
	mu.Lock()
	defer mu.Unlock()

	for fd := range totalConnections {
		// Close the connection
		syscall.Close(fd)
		// Remove from connection map
		delete(totalConnections, fd)
	}
}
