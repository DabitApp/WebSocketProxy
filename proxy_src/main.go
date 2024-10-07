package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"sync"

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
	addr    = flag.String("addr", "localhost:8080", "http service address")
	authKey = flag.String("auth", "", "authentication key")
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type proxyConnection struct {
	tcpConn net.Conn
	target  string
	mu      sync.Mutex
}

func (pc *proxyConnection) connect() (net.Conn, error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.tcpConn != nil {
		return nil, nil // Already connected
	}

	conn, err := net.Dial("tcp", pc.target)
	if err != nil {
		return nil, err
	}
	log.Println("dial tcp: ", pc.target)

	pc.tcpConn = conn
	return conn, nil
}

func (pc *proxyConnection) disconnect() {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.tcpConn != nil {
		pc.tcpConn.Close()
		pc.tcpConn = nil
	}
}

func (pc *proxyConnection) write(data []byte) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.tcpConn == nil {
		return nil // Ignore if not connected
	}

	_, err := pc.tcpConn.Write(data)
	return err
}

func main() {
	flag.Parse()
	log.Println("proxy server start at ", *addr)
	http.HandleFunc("/", handleWebSocket)
	log.Fatal(http.ListenAndServe(*addr, nil))
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

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade error:", err)
		return
	}
	defer conn.Close()
	
	log.Println("connection started: ", r.RemoteAddr, r.RequestURI)

	pc := &proxyConnection{target: target}
	defer pc.disconnect()
	initializeTCP(conn, pc)

	// Handle WebSocket messages
	for {
		messageType, p, err := conn.ReadMessage()
		if err != nil {
			log.Println("read error:", err)
			return
		}

		if messageType != websocket.BinaryMessage || len(p) < 1 {
			continue
		}

		cmd := p[0]
		payload := p[1:]

		switch cmd {
		case cmdConnect:
			initializeTCP(conn, pc)
		case cmdDisconnect:
			pc.disconnect()
			conn.WriteMessage(websocket.BinaryMessage, []byte{stateDisconnect})
		case cmdSend:
			err := pc.write(payload)
			if err != nil {
				log.Println("tcp write error:", err)
				pc.disconnect()
				conn.WriteMessage(websocket.BinaryMessage, []byte{stateDisconnect})
			}
		}
	}
}

func initializeTCP(wsConn *websocket.Conn, pc *proxyConnection) {
    tcpConn, err := pc.connect()
	if err != nil {
		log.Println("tcp connect error:", err)
		wsConn.WriteMessage(websocket.BinaryMessage, []byte{stateDisconnect})
	} else if tcpConn == nil {
	    return
	} else {
		wsConn.WriteMessage(websocket.BinaryMessage, []byte{stateOK})
		go handleTCPRead(wsConn, pc)
	}
}

func handleTCPRead(wsConn *websocket.Conn, pc *proxyConnection) {
	buffer := make([]byte, 4096)
	for {
		pc.mu.Lock()
		if pc.tcpConn == nil {
			pc.mu.Unlock()
			return
		}
		tcpConn := pc.tcpConn
		pc.mu.Unlock()

		n, err := tcpConn.Read(buffer)
		if err != nil {
			log.Println("tcp read error:", err)
			pc.disconnect()
			wsConn.WriteMessage(websocket.BinaryMessage, []byte{stateDisconnect})
			return
		}

		message := append([]byte{stateNormal}, buffer[:n]...)
		err = wsConn.WriteMessage(websocket.BinaryMessage, message)
		if err != nil {
			log.Println("websocket write error:", err)
			return
		}
	}
}
