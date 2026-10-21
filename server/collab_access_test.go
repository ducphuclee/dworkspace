package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func dialCollab(t *testing.T, srv *httptest.Server, cookie, pageID string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/collab/"+pageID, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{cookie}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ws.SetReadLimit(wsReadLimit)
	return ws
}

// readUntilSynced reads the join replay and reports how many update frames
// came before {"synced":true}, or the error that ended it.
func readUntilSynced(ws *websocket.Conn) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	frames := 0
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return frames, err
		}
		if typ == websocket.MessageText && strings.Contains(string(data), `"synced":true`) {
			return frames, nil
		}
		if typ == websocket.MessageBinary && len(data) > 0 && data[0] == frameUpdate {
			frames++
		}
	}
}

// A page with more stored updates than the outgoing queue held could not be
// opened in any editor: the join replay overflowed the queue and the socket
// was closed as a slow consumer, every time, for everybody. Anybody with write
// access could get a page there by sending a few hundred tiny updates and
// ignoring the request to compact.
func TestAPageWithManyUpdatesStillOpens(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "owner@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	page := s.makePage(t, ws, uid, "", "Busy page", `{}`)
	const n = 1200
	for i := 1; i <= n; i++ {
		if _, err := s.db.Exec(`INSERT INTO yjs_updates (page_id, seq, data) VALUES (?, ?, ?)`, page, i, []byte{0, 0}); err != nil {
			t.Fatalf("seed update: %v", err)
		}
	}
	srv := httptest.NewServer(s)
	defer srv.Close()
	conn := dialCollab(t, srv, cookie, page)
	defer conn.CloseNow()
	frames, err := readUntilSynced(conn)
	if err != nil {
		t.Fatalf("the join replay ended after %d of %d updates: %v", frames, n, err)
	}
	if frames != n {
		t.Errorf("replayed %d updates, want %d", frames, n)
	}
}

// An open editor kept receiving every edit after its reader lost the page:
// writes were checked per frame, what arrives was not.
func TestAnEditorClosesWhenItsReaderLosesThePage(t *testing.T) {
	old := collabRecheck
	collabRecheck = 50 * time.Millisecond
	defer func() { collabRecheck = old }()

	s := testServer(t)
	owner, ownerCookie := signedIn(t, s, "owner@example.test")
	member, memberCookie := signedIn(t, s, "member@example.test")
	ws := s.firstWorkspaceOf(t, owner)
	s.addMember(t, ws, member, "member")
	page := s.makePage(t, ws, owner, "", "Plans", `{}`)
	srv := httptest.NewServer(s)
	defer srv.Close()

	conn := dialCollab(t, srv, memberCookie, page)
	defer conn.CloseNow()
	if _, err := readUntilSynced(conn); err != nil {
		t.Fatalf("join: %v", err)
	}
	// Still allowed: the connection stays.
	time.Sleep(200 * time.Millisecond)
	if rec := requestAs(t, s, ownerCookie, "PATCH", "/api/pages/"+page, `{"visibility":"private"}`); rec.Code != 200 {
		t.Fatalf("make private: %d %s", rec.Code, rec.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue
		}
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("the editor was not closed for losing the page: %v", err)
		}
		return
	}
}
