package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize: 16 << 10, WriteBufferSize: 16 << 10,
	CheckOrigin: func(r *http.Request) bool { return true }, // auth is by API key, not cookies
}

// utf8Splitter holds back an incomplete trailing UTF-8 sequence so every
// text frame we send is valid UTF-8 (browsers close the socket otherwise).
type utf8Splitter struct{ pending []byte }

func (u *utf8Splitter) feed(b []byte) string {
	buf := append(u.pending, b...)
	cut := len(buf)
	for k := 1; k <= 3 && k <= len(buf); k++ {
		c := buf[len(buf)-k]
		if c&0xC0 != 0x80 { // ASCII or lead byte
			if c >= 0xC0 && !utf8.FullRune(buf[len(buf)-k:]) {
				cut = len(buf) - k
			}
			break
		}
	}
	u.pending = append([]byte(nil), buf[cut:]...)
	return strings.ToValidUTF8(string(buf[:cut]), "\uFFFD")
}

// terminal bridges a browser WebSocket to a PTY in the box (via boxd).
// Text frames in = keystrokes; a JSON frame {"type":"resize","cols":N,"rows":M} resizes.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	p := pr(r)
	id := chi.URLParam(r, "id")
	if _, err := s.M.GetBox(r.Context(), p, id); err != nil {
		writeErr(w, r, err)
		return
	}
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	var wmu sync.Mutex
	send := func(mt int, data []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return ws.WriteMessage(mt, data)
	}
	split := &utf8Splitter{}
	done := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(done) }) }
	hostID, stream, err := s.M.OpenTerminal(r.Context(), p, id, cols, rows, func(data []byte, exited bool) {
		if len(data) > 0 {
			if txt := split.feed(data); txt != "" {
				send(websocket.TextMessage, []byte(txt))
			}
		}
		if exited {
			send(websocket.TextMessage, []byte("\r\n[boxaz] session ended\r\n"))
			finish()
		}
	})
	if err != nil {
		b, _ := json.Marshal(map[string]any{"error": err.Error()})
		send(websocket.TextMessage, append([]byte("\r\n[boxaz] "), b...))
		send(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "terminal unavailable"))
		return
	}
	defer s.M.CloseTerminal(hostID, stream)
	go func() {
		defer finish()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if len(msg) > 0 && msg[0] == '{' {
				var ctl struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
				}
				if json.Unmarshal(msg, &ctl) == nil && ctl.Type == "resize" {
					s.M.ResizeTerminal(hostID, stream, ctl.Cols, ctl.Rows)
					continue
				}
			}
			if err := s.M.Hub.SendStream(hostID, stream, msg); err != nil {
				return
			}
		}
	}()
	<-done
}
