package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	stateOK         byte = 0
	stateDisconnect byte = 1
	stateNormal     byte = 2

	cmdConnect    byte = 0
	cmdDisconnect byte = 1
	cmdSend       byte = 2
)

var (
	ServiceName     = "dabit-ws-proxy"
	ShutdownTimeout = 10 * time.Second
	addr            = flag.String("addr", "localhost:8080", "http service address")
	authKey         = flag.String("auth", "", "authentication key")
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type proxyConnection struct {
	tcpConn net.Conn
	target  string
}

func (pc *proxyConnection) connect(ctx context.Context) (net.Conn, error) {
	if pc.tcpConn != nil {
		return nil, nil // Already connected
	}

	dialer := net.Dialer{
		Timeout: 20 * time.Second,
	}
	conn, err := dialer.DialContext(ctx, "tcp", pc.target)
	if err != nil {
		return nil, err
	}
	log.Println("dial tcp: ", pc.target)

	pc.tcpConn = conn
	return conn, nil
}

func (pc *proxyConnection) disconnect() {
	if pc.tcpConn != nil {
		pc.tcpConn.Close()
	}
}

func (pc *proxyConnection) write(data []byte) error {
	if pc.tcpConn == nil {
		return errors.New("not connected, abort the write")
	}

	_, err := pc.tcpConn.Write(data)
	return err
}

func runHttpServer(srv *http.Server) {
	log.Printf("%s starting at %s", ServiceName, srv.Addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	// Wait for signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println(ServiceName, "shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("server shutdown: %s\n", err)
	}
	log.Println(ServiceName, "shut down gracefully.")
}

func main() {
	flag.Parse()
	// Create a new ServeMux instead of using the default one
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleWebSocket)

	srv := &http.Server{
		Addr:    *addr,
		Handler: mux, // Explicitly set the handler
	}

	runHttpServer(srv)
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	auth := query.Get("auth")
	target := query.Get("target")

	if target == "" {
		http.Error(w, "Missing target parameter", http.StatusBadRequest)
		return
	}

	if *authKey != "" && auth != *authKey {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Handshake websocket connection
	wsClientConnection, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade error:", err)
		return
	}
	defer wsClientConnection.Close()

	// Initialize proxy connection
	proxy, err := initializeProxy(r, target)
	if err != nil {
		log.Println("initialize proxy error:", err)
		return
	}
	defer proxy.disconnect()

	// Write stateOK to websocket
	err = wsClientConnection.WriteMessage(websocket.BinaryMessage, []byte{stateOK})
	if err != nil {
		log.Println("websocket write error:", err)
		return
	}
	//handshake done, now we can start the background context
	clientContext, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Test code
	// go func() {
	// 	time.Sleep(30 * time.Second)
	// 	proxy.disconnect()
	// }()
	go handleTCPRead(cancel, wsClientConnection, proxy)
	go handleWebSocketProcess(cancel, wsClientConnection, proxy)
	go runningPingPong(clientContext, cancel, wsClientConnection)
	<-clientContext.Done()
	log.Println("client context done")
}

func initializeProxy(r *http.Request, target string) (*proxyConnection, error) {
	log.Println("proxy connection started: ", r.RemoteAddr, r.RequestURI)
	proxy := &proxyConnection{target: target}
	tcpConn, err := proxy.connect(r.Context())
	if err != nil {
		log.Println("tcp connect error:", err)
		return nil, err
	}
	if tcpConn == nil {
		return nil, errors.New("tcp connect error")
	}
	return proxy, nil
}

func runningPingPong(ctx context.Context, cancelContext context.CancelFunc, wsClientConnection *websocket.Conn) {
	ticker := time.NewTicker(20 * time.Second)
	defer cancelContext()
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			err := wsClientConnection.WriteMessage(websocket.PingMessage, []byte("ping"))
			if err != nil {
				log.Println("websocket ping error:", err)
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func handleTCPRead(cancelContext context.CancelFunc, wsClientConnection *websocket.Conn, proxy *proxyConnection) {
	defer cancelContext()
	buffer := make([]byte, 4096)
	for {
		if proxy.tcpConn == nil {
			return
		}

		n, err := proxy.tcpConn.Read(buffer)
		if err != nil {
			log.Println("tcp read error:", err)
			//wsClientConnection.WriteMessage(websocket.BinaryMessage, []byte{stateDisconnect})
			return
		}

		message := append([]byte{stateNormal}, buffer[:n]...)
		err = wsClientConnection.WriteMessage(websocket.BinaryMessage, message)
		if err != nil {
			log.Println("websocket write error:", err)
			return
		}
	}
}

func handleWebSocketProcess(cancelContext context.CancelFunc, wsClientConnection *websocket.Conn, proxy *proxyConnection) {
	defer cancelContext()
	for {
		messageType, p, err := wsClientConnection.ReadMessage()
		if err != nil {
			log.Println("websocketread error:", err)
			return
		}

		if messageType != websocket.BinaryMessage || len(p) < 1 {
			continue
		}

		cmd := p[0]
		payload := p[1:]

		// 20251015: now we only accept send proxy command
		switch cmd {
		case cmdConnect:
			//initializeTCP(r.Context(), wsClientConnection, proxy)
			continue
		case cmdDisconnect:
			// for simplicity, just abort the websocket connection
			return
		case cmdSend:
			err := proxy.write(payload)
			if err != nil {
				log.Println("tcp write error:", err)
				// for simplicity, just abort the websocket connection
				return
			}
		}
	}
}
