package session

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/matheuscamposmt/oat/internal/claudestt"
)

// voiceServer answers each stream with one sentence and records the key term header.
func voiceServer(t *testing.T) (*claudestt.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var headers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get("x-config-keyterms"))
		mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageText && strings.Contains(string(data), "CloseStream") {
				break
			}
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"TranscriptText","data":"Falamos da RAIS."}`))
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"TranscriptEndpoint"}`))
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)
	creds := filepath.Join(t.TempDir(), ".credentials.json")
	body := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":%d}}`, time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(creds, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c := claudestt.New(creds, "oat/test")
	c.URL = "ws" + strings.TrimPrefix(srv.URL, "http")
	c.Quiet = 50 * time.Millisecond
	return c, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), headers...)
	}
}

func TestMeetingSendsTheKeyterms(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(fmt.Sprintf("live=%v", live), func(t *testing.T) {
			client, headers := voiceServer(t)
			o, _, op, _ := setup(t, "off", "[Out] Speaker")
			o.Client = client
			o.Keyterms = []string{"RAIS", "Entra ID"}
			if live {
				o.Live = func(ctx context.Context, lang string, keyterms []string, onText func(string)) (LiveStream, error) {
					st, err := client.Open(ctx, lang, keyterms, onText)
					if err != nil {
						return nil, err
					}
					return st, nil
				}
			}
			op.frames["alsa_input.mic"] = utterance()
			s, err := Start(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			close(op.gate)
			op.waitDrained(t, 2)
			s.Stop()
			var liveText bool
			waitFor(t, s, "StoppedEvent", func(ev any) bool {
				if e, ok := ev.(LiveEvent); ok && e.Text != "" {
					liveText = true
				}
				return isStopped(ev)
			})
			if liveText != live {
				t.Fatalf("live text %v, want %v", liveText, live)
			}
			if h := headers(); len(h) != 1 || h[0] != "RAIS,Entra ID" {
				t.Fatalf("headers %q", h)
			}
			m, err := o.Store.Load(s.Meeting().Snapshot().ID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(m.Meta.Keyterms, ",") != "RAIS,Entra ID" {
				t.Fatalf("meta keyterms %q", m.Meta.Keyterms)
			}
			if segs, _ := m.Segments(); len(segs) != 1 || segs[0].Text != "Falamos da RAIS." {
				t.Fatalf("segments %+v", segs)
			}
		})
	}
}
