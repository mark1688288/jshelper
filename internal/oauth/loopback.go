package oauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	successHTML = "授權完成，可以關閉這個分頁並回到 terminal。"
	failHTML    = "授權沒有完成，可以關閉這個分頁並回到 terminal。"
)

type callback struct {
	code string
	err  error
}

type loopServer struct {
	ln   net.Listener
	srv  *http.Server
	done chan callback
	stop sync.Once
}

func randomState() (string, error) {
	buf := make([]byte, 32)
	n, err := rand.Read(buf)
	if err != nil || n != len(buf) {
		return "", errors.New("docs-cli: error: oauth state")
	}
	return hex.EncodeToString(buf), nil
}

func sameState(got, want string) bool {
	if len(got) != len(want) || len(want) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func listenLoopback(state string) (*loopServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("docs-cli: error: %s", mask(err.Error()))
	}
	loop := &loopServer{ln: ln, done: make(chan callback, 1)}
	var once sync.Once
	send := func(c callback) {
		once.Do(func() {
			loop.done <- c
		})
	}
	loop.srv = &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handleCallback(w, r, state, send) }),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		err := loop.srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			send(callback{err: fmt.Errorf("docs-cli: error: %s", mask(err.Error()))})
		}
	}()
	return loop, nil
}

func handleCallback(w http.ResponseWriter, r *http.Request, state string, send func(callback)) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	code := q.Get("code")
	oauthErr := q.Get("error")
	if code == "" && oauthErr == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if oauthErr != "" {
		writeHTML(w, http.StatusBadRequest, failHTML)
		send(callback{err: callbackError(oauthErr, q.Get("error_description"))})
		return
	}
	if !sameState(q.Get("state"), state) {
		writeHTML(w, http.StatusBadRequest, failHTML)
		send(callback{err: errors.New("docs-cli: error: oauth state mismatch")})
		return
	}
	writeHTML(w, http.StatusOK, successHTML)
	send(callback{code: code})
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func callbackError(oauthErr, description string) error {
	if proto := protocolError(oauthErr + " " + description); proto != nil {
		return proto
	}
	if oauthErr != "" && oauthErrorCode(oauthErr) {
		return fmt.Errorf("docs-cli: error: %s", oauthErr)
	}
	return errors.New("docs-cli: error: oauth callback failed")
}

func oauthErrorCode(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

func (l *loopServer) redirectURL() string {
	addr := l.ln.Addr().(*net.TCPAddr)
	return fmt.Sprintf("http://127.0.0.1:%d", addr.Port)
}

func (l *loopServer) port() int {
	return l.ln.Addr().(*net.TCPAddr).Port
}

func (l *loopServer) shutdown() {
	l.stop.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = l.srv.Shutdown(ctx)
		_ = l.ln.Close()
	})
}
