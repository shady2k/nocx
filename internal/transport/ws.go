package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
)

type WSServer struct {
	log      log.Logger
	server   *http.Server
	port     int
	listener net.Listener
	upgrader websocket.Upgrader
}

type resizeMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func NewWSServer(logger log.Logger) *WSServer {
	return &WSServer{
		log: logger,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (s *WSServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/session", s.handleSession)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("ws listen: %w", err)
	}
	s.listener = listener
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return fmt.Errorf("ws listen: not a TCP address")
	}
	s.port = tcpAddr.Port

	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 0, // disable for WS upgrade
	}
	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.log.Error("ws server error", "error", err)
		}
	}()

	s.log.Info("ws server started", "port", s.port)
	return nil
}

func (s *WSServer) Stop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *WSServer) Port() int {
	return s.port
}

func (s *WSServer) handleSession(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error("ws upgrade", "error", err)
		return
	}
	defer func() { _ = conn.Close() }()

	pt, err := pty.NewLocal(s.log, pty.Config{
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		s.log.Error("pty spawn", "error", err)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"failed to spawn shell"}`))
		return
	}
	defer func() { _ = pt.Close() }()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	errCh := make(chan error, 2)

	go s.ptyToWS(ctx, pt, conn, errCh)
	go s.wsToPTY(ctx, pt, conn, errCh)

	select {
	case err := <-errCh:
		if err != nil && err != io.EOF {
			s.log.Error("ws session error", "error", err)
		}
	case <-pt.Done():
		s.log.Info("pty process exited")
	}
}

func (s *WSServer) ptyToWS(ctx context.Context, pt *pty.LocalPty, conn *websocket.Conn, errCh chan<- error) {
	buf := make([]byte, 32768)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := pt.Read(buf)
		if err != nil {
			if err != io.EOF {
				errCh <- err
			}
			return
		}

		if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
			errCh <- err
			return
		}
	}
}

func (s *WSServer) wsToPTY(ctx context.Context, pt *pty.LocalPty, conn *websocket.Conn, errCh chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msgType, data, err := conn.ReadMessage()
		if err != nil {
			if err != io.EOF {
				errCh <- err
			}
			return
		}

		switch msgType {
		case websocket.BinaryMessage:
			if _, err := pt.Write(data); err != nil {
				errCh <- err
				return
			}
		case websocket.TextMessage:
			var msg resizeMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.Type == "resize" {
				_ = pt.Resize(context.Background(), msg.Cols, msg.Rows, 0, 0)
			}
		}
	}
}
