package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"pdfeditor/server/internal/storage"
	"pdfeditor/server/internal/ws"
)

// Use a real WebSocket handshake and frames over an in-memory connection.
// This keeps transport verification available where TCP binding is forbidden.
type eventListener struct {
	conn     net.Conn
	accepted bool
	done     chan struct{}
	once     sync.Once
}

func (l *eventListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *eventListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *eventListener) Addr() net.Addr { return l.conn.LocalAddr() }

func connectEvents(t *testing.T, svc *Service, docID string) *websocket.Conn {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	listener := &eventListener{conn: serverConn, done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", svc.hub.Handler(ws.Options{CurrentVersion: svc.CurrentVersion}))
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() { clientConn.Close(); serverConn.Close(); server.Close(); listener.Close() })
	clientConn.SetDeadline(time.Now().Add(5 * time.Second))
	conn, _, err := websocket.NewClient(clientConn, &url.URL{Scheme: "ws", Host: "events.test", Path: "/ws"}, nil, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]any{"v": 1, "type": "subscribe", "doc_id": docID}); err != nil {
		t.Fatal(err)
	}
	subscribed := readEvent(t, conn)
	if subscribed["type"] != "subscribed" {
		t.Fatalf("subscribe: %v", subscribed)
	}
	return conn
}
func readEvent(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var event map[string]any
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	return event
}

type coverFailureDriver struct{ storage.Driver }

func (d coverFailureDriver) Put(ctx context.Context, key string, data []byte) error {
	if strings.HasSuffix(key, "/cover.png") {
		return errors.New("cover storage failed")
	}
	return d.Driver.Put(ctx, key, data)
}

func TestRenderWebSocketEvents(t *testing.T) {
	for _, tc := range []struct {
		name, kind, shell string
		storageFailure    bool
		want              string
	}{
		{"pdf success", "pdf", validRender, false, "pdf_updated"},
		{"slideshow success", "slideshow", validRender, false, "pdf_updated"},
		{"render failure", "pdf", "echo 'render failed' >&2\nexit 1\n", false, "pdf_failed"},
		{"output storage failure", "pdf", validRender, true, "pdf_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, driver, st := testService(t, tc.shell)
			doc, err := st.CreateProject("script", "", "Events", tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if tc.storageFailure {
				svc.driver = coverFailureDriver{driver}
			}
			conn := connectEvents(t, svc, doc.ID)
			svc.run(doc.ID, 1, false)
			started := readEvent(t, conn)
			if started["type"] != "updating_pdf" || started["doc_id"] != doc.ID {
				t.Fatalf("start: %v", started)
			}
			completed := readEvent(t, conn)
			if completed["type"] != tc.want || completed["doc_id"] != doc.ID || completed["version"] != float64(1) || completed["v"] != float64(1) {
				t.Fatalf("completion: %v", completed)
			}
			if tc.want == "pdf_failed" {
				if completed["error"] == "" || completed["error"] == nil {
					t.Fatalf("missing error: %v", completed)
				}
			} else {
				if completed["project_type"] != tc.kind {
					t.Fatalf("project type: %v", completed)
				}
				fields := []string{"manifest_url", "cover_url", "pdf_url"}
				if tc.kind == "slideshow" {
					fields = append(fields, "slides_url")
				}
				for _, field := range fields {
					endpoint, ok := completed[field].(string)
					if !ok || endpoint == "" {
						t.Fatalf("missing %s: %v", field, completed)
					}
					if got := request(t, svc, "GET", endpoint, ""); got.Code != 200 {
						t.Fatalf("%s unavailable when event arrives: %d", field, got.Code)
					}
				}
				if tc.kind == "slideshow" {
					endpoint, ok := completed["pptx_url"].(string)
					if !ok {
						t.Fatalf("missing pptx_url: %v", completed)
					}
					if got := request(t, svc, "GET", endpoint, ""); got.Code != 200 {
						t.Fatalf("PPTX unavailable: %d", got.Code)
					}
				}
			}
		})
	}
}
