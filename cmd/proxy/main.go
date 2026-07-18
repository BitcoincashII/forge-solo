package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	listenAddr   string
	backendAddr  string
	healthPort   int
	maxConns     int
	connTimeout  time.Duration

	activeConns  int64
	totalConns   int64
	bytesIn      int64
	bytesOut     int64
	startTime    = time.Now()

	shuttingDown int32
)

func init() {
	flag.StringVar(&listenAddr, "listen", ":3334", "Address to listen on")
	flag.StringVar(&backendAddr, "backend", "127.0.0.1:3336", "Backend stratum server address")
	flag.IntVar(&healthPort, "health-port", 3380, "Health check HTTP port")
	flag.IntVar(&maxConns, "max-conns", 50000, "Maximum concurrent connections")
	flag.DurationVar(&connTimeout, "timeout", 5*time.Minute, "Connection idle timeout")
}

func main() {
	flag.Parse()

	// Override from environment
	if env := os.Getenv("PROXY_LISTEN"); env != "" {
		listenAddr = env
	}
	if env := os.Getenv("PROXY_BACKEND"); env != "" {
		backendAddr = env
	}

	log.Printf("Forge Pool Stratum Proxy")
	log.Printf("Listen: %s -> Backend: %s", listenAddr, backendAddr)

	// Start health check server
	go startHealthServer()

	// Start proxy listener
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", listenAddr, err)
	}
	defer listener.Close()

	log.Printf("Proxy listening on %s", listenAddr)

	// Handle shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		atomic.StoreInt32(&shuttingDown, 1)
		listener.Close()
	}()

	// Accept connections
	for {
		conn, err := listener.Accept()
		if err != nil {
			if atomic.LoadInt32(&shuttingDown) == 1 {
				break
			}
			log.Printf("Accept error: %v", err)
			continue
		}

		if atomic.LoadInt64(&activeConns) >= int64(maxConns) {
			log.Printf("Max connections reached, rejecting from %s", conn.RemoteAddr())
			conn.Close()
			continue
		}

		atomic.AddInt64(&totalConns, 1)
		atomic.AddInt64(&activeConns, 1)
		go handleConnection(conn)
	}

	// Wait for connections to drain
	log.Printf("Waiting for %d connections to close...", atomic.LoadInt64(&activeConns))
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	for {
		select {
		case <-timeout:
			log.Println("Shutdown timeout, forcing exit")
			return
		case <-ticker.C:
			if atomic.LoadInt64(&activeConns) == 0 {
				log.Println("All connections closed")
				return
			}
		}
	}
}

func handleConnection(clientConn net.Conn) {
	defer func() {
		clientConn.Close()
		atomic.AddInt64(&activeConns, -1)
	}()

	clientAddr := clientConn.RemoteAddr().String()

	// Configure TCP connection for mining
	if tc, ok := clientConn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)                      // Disable Nagle for low-latency
		tc.SetKeepAlive(true)                    // Enable keepalive
		tc.SetKeepAlivePeriod(30 * time.Second)  // Check every 30 seconds
	}

	// Connect to backend
	backendConn, err := net.DialTimeout("tcp", backendAddr, 10*time.Second)
	if err != nil {
		log.Printf("Backend connection failed for %s: %v", clientAddr, err)
		return
	}
	defer backendConn.Close()

	// Configure backend connection
	if tc, ok := backendConn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)                      // Disable Nagle
		tc.SetKeepAlive(true)                    // Enable keepalive
		tc.SetKeepAlivePeriod(30 * time.Second)  // Check every 30 seconds
	}

	// Bidirectional proxy
	var wg sync.WaitGroup
	wg.Add(2)

	// Client -> Backend
	go func() {
		defer wg.Done()
		n, _ := copyWithTimeout(backendConn, clientConn, connTimeout)
		atomic.AddInt64(&bytesIn, n)
	}()

	// Backend -> Client
	go func() {
		defer wg.Done()
		n, _ := copyWithTimeout(clientConn, backendConn, connTimeout)
		atomic.AddInt64(&bytesOut, n)
	}()

	wg.Wait()
}

func copyWithTimeout(dst, src net.Conn, timeout time.Duration) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64

	for {
		// Set read deadline
		src.SetReadDeadline(time.Now().Add(timeout))

		n, err := src.Read(buf)
		if n > 0 {
			// Reset deadline on activity
			dst.SetWriteDeadline(time.Now().Add(30 * time.Second))

			written, werr := dst.Write(buf[:n])
			total += int64(written)
			if werr != nil {
				return total, werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return total, nil
			}
			return total, err
		}
	}
}

func startHealthServer() {
	mux := http.NewServeMux()

	// Health check endpoint for HAProxy
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&shuttingDown) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("shutting down"))
			return
		}

		// Check if we can connect to backend
		conn, err := net.DialTimeout("tcp", backendAddr, 5*time.Second)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("backend unreachable"))
			return
		}
		conn.Close()

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Stats endpoint
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		stats := map[string]interface{}{
			"active_connections": atomic.LoadInt64(&activeConns),
			"total_connections":  atomic.LoadInt64(&totalConns),
			"bytes_in":           atomic.LoadInt64(&bytesIn),
			"bytes_out":          atomic.LoadInt64(&bytesOut),
			"uptime_seconds":     time.Since(startTime).Seconds(),
			"backend":            backendAddr,
			"listen":             listenAddr,
			"max_connections":    maxConns,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	})

	// Ready endpoint (for Kubernetes-style probes)
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt64(&activeConns) >= int64(maxConns)*9/10 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("at capacity"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	})

	addr := fmt.Sprintf(":%d", healthPort)
	log.Printf("Health server on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("Health server error: %v", err)
	}
}
