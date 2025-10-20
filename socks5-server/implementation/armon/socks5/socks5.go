package socks5

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errUnrecognizedAddrType = errors.New("unrecognized address type")
)

const (
	socks5Version = uint8(5)
)

// Config is used to setup and configure a Server
type Config struct {
	// AuthMethods can be provided to implement custom authentication
	// By default, "auth-less" mode is enabled.
	// For password-based auth use UserPassAuthenticator.
	AuthMethods []Authenticator

	// If provided, username/password authentication is enabled,
	// by appending a UserPassAuthenticator to AuthMethods. If not provided,
	// and AUthMethods is nil, then "auth-less" mode is enabled.
	Credentials CredentialStore

	// Resolver can be provided to do custom name resolution.
	// Defaults to DNSResolver if not provided.
	Resolver NameResolver

	// Rules is provided to enable custom logic around permitting
	// various commands. If not provided, PermitAll is used.
	Rules RuleSet

	// Rewriter can be used to transparently rewrite addresses.
	// This is invoked before the RuleSet is invoked.
	// Defaults to NoRewrite.
	Rewriter AddressRewriter

	// BindIP is used for bind or udp associate
	BindIP net.IP

	// Logger can be used to provide a custom log target.
	// Defaults to stdout.
	Logger *log.Logger

	// Optional function for dialing out
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Server is reponsible for accepting connections and handling
// the details of the SOCKS5 protocol
type Server struct {
	config      *Config
	authMethods map[uint8]Authenticator
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	shutdown    int32 // atomic boolean: 0 = false, 1 = true
}

// New creates a new Server and potentially returns an error
func New(conf *Config) (*Server, error) {
	// Ensure we have at least one authentication method enabled
	if len(conf.AuthMethods) == 0 {
		if conf.Credentials != nil {
			conf.AuthMethods = []Authenticator{&UserPassAuthenticator{conf.Credentials}}
		} else {
			conf.AuthMethods = []Authenticator{&NoAuthAuthenticator{}}
		}
	}

	// Ensure we have a DNS resolver
	if conf.Resolver == nil {
		conf.Resolver = DNSResolver{}
	}

	// Ensure we have a rule set
	if conf.Rules == nil {
		conf.Rules = PermitAll()
	}

	// Ensure we have a log target
	if conf.Logger == nil {
		conf.Logger = log.New(os.Stdout, "", log.LstdFlags)
	}

	ctx, cancel := context.WithCancel(context.Background())
	server := &Server{
		config: conf,
		ctx:    ctx,
		cancel: cancel,
	}

	server.authMethods = make(map[uint8]Authenticator)

	for _, a := range conf.AuthMethods {
		server.authMethods[a.GetCode()] = a
	}

	return server, nil
}

// ListenAndServe is used to create a listener and serve on it
func (s *Server) ListenAndServe(network, addr string) error {
	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	return s.Serve(l)
}

// ListenAndServeWithContext is used to create a listener and serve on it with context
func (s *Server) ListenAndServeWithContext(ctx context.Context, network, addr string) error {
	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	return s.ServeWithContext(ctx, l)
}

// Serve is used to serve connections from a listener
func (s *Server) Serve(l net.Listener) error {
	return s.ServeWithContext(s.ctx, l)
}

// ServeWithContext is used to serve connections from a listener with context
func (s *Server) ServeWithContext(ctx context.Context, l net.Listener) error {
	defer l.Close()

	for {
		select {
		case <-ctx.Done():
			s.config.Logger.Printf("[INFO] socks: Server shutting down due to context cancellation")
			return ctx.Err()
		default:
		}

		// Set a timeout for Accept to allow periodic context checking
		if tcpListener, ok := l.(*net.TCPListener); ok {
			tcpListener.SetDeadline(time.Now().Add(time.Second))
		}

		conn, err := l.Accept()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // Timeout is expected, check context again
			}
			return err // Non-temporary error, likely means the listener is no longer usable
		}

		s.wg.Go(func() {
			s.ServeConn(conn)
		})
	}
}

// ServeConn is used to serve a single connection.
func (s *Server) ServeConn(conn net.Conn) error {
	defer conn.Close()

	// Check if server is shutting down
	if atomic.LoadInt32(&s.shutdown) == 1 {
		return fmt.Errorf("server is shutting down")
	}

	bufConn := bufio.NewReader(conn)

	// Read the version byte
	version := []byte{0}
	if _, err := bufConn.Read(version); err != nil {
		s.config.Logger.Printf("[ERR] socks: Failed to get version byte: %v", err)
		return err
	}

	// Ensure we are compatible
	if version[0] != socks5Version {
		err := fmt.Errorf("unsupported SOCKS version: %v", version)
		s.config.Logger.Printf("[ERR] socks: %v", err)
		return err
	}

	// Authenticate the connection
	authContext, err := s.authenticate(conn, bufConn)
	if err != nil {
		err = fmt.Errorf("failed to authenticate: %v", err)
		s.config.Logger.Printf("[ERR] socks: %v", err)
		return err
	}

	request, err := NewRequest(bufConn)
	if err != nil {
		if errors.Is(err, errUnrecognizedAddrType) {
			if err := sendReply(conn, addrTypeNotSupported, nil); err != nil {
				return fmt.Errorf("failed to send reply: %v", err)
			}
		}
		return fmt.Errorf("failed to read destination address: %v", err)
	}
	request.AuthContext = authContext
	if client, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		request.RemoteAddr = &AddrSpec{IP: client.IP, Port: client.Port}
	}

	// Process the client request
	if err := s.handleRequest(request, conn); err != nil {
		err = fmt.Errorf("failed to handle request: %v", err)
		s.config.Logger.Printf("[ERR] socks: %v", err)
		return err
	}

	return nil
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() error {
	if !atomic.CompareAndSwapInt32(&s.shutdown, 0, 1) {
		return fmt.Errorf("server is already shutting down")
	}

	// Cancel the context to signal shutdown
	s.cancel()

	// Wait for all connections to finish with a timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.config.Logger.Printf("[INFO] socks: Server shutdown completed")
		return nil
	case <-time.After(30 * time.Second):
		s.config.Logger.Printf("[WARN] socks: Server shutdown timeout, some connections may not have closed gracefully")
		return fmt.Errorf("shutdown timeout")
	}
}

// ShutdownWithTimeout gracefully shuts down the server with a custom timeout
func (s *Server) ShutdownWithTimeout(timeout time.Duration) error {
	if !atomic.CompareAndSwapInt32(&s.shutdown, 0, 1) {
		return fmt.Errorf("server is already shutting down")
	}

	// Cancel the context to signal shutdown
	s.cancel()

	// Wait for all connections to finish with the specified timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.config.Logger.Printf("[INFO] socks: Server shutdown completed")
		return nil
	case <-time.After(timeout):
		s.config.Logger.Printf("[WARN] socks: Server shutdown timeout after %v, some connections may not have closed gracefully", timeout)
		return fmt.Errorf("shutdown timeout after %v", timeout)
	}
}

// IsShutdown returns true if the server is shutting down or has been shut down
func (s *Server) IsShutdown() bool {
	return atomic.LoadInt32(&s.shutdown) == 1
}
