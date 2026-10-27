package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fcmStub struct {
	mu       sync.Mutex
	messages []map[string]any
	reply    func(token string, call int) (int, string)
	calls    map[string]int
}

func newStub(t *testing.T, reply func(token string, call int) (int, string)) (*fcmStub, *FCM) {
	s := &fcmStub{reply: reply, calls: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message map[string]any `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("bad fcm body: %v", err)
		}
		token, _ := body.Message["token"].(string)
		s.mu.Lock()
		s.messages = append(s.messages, body.Message)
		s.calls[token]++
		call := s.calls[token]
		s.mu.Unlock()
		status, resp := 200, `{"name":"projects/p/messages/1"}`
		if s.reply != nil {
			status, resp = s.reply(token, call)
		}
		w.WriteHeader(status)
		w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	f := newFCM(srv.Client(), srv.URL)
	f.backoff = time.Millisecond
	return s, f
}

func post(t *testing.T, h http.Handler, fixture string) (int, map[string]any) {
	raw, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_matrix/push/v1/notify", strings.NewReader(string(raw))))
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

var testConfig = Config{AppIDs: []string{"com.ascimov.app.android", "com.ascimov.app.ios"}, Alert: "New message"}

func TestNotifyBuildsMessage(t *testing.T) {
	stub, fcm := newStub(t, nil)
	code, out := post(t, NewGateway(fcm, testConfig).Handler(), "notify.json")
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}
	if r := out["rejected"].([]any); len(r) != 0 {
		t.Fatalf("unexpected rejected %v", r)
	}
	if len(stub.messages) != 1 {
		t.Fatalf("want 1 fcm message, got %d", len(stub.messages))
	}
	m := stub.messages[0]
	data := m["data"].(map[string]any)
	if data["event_id"] != "$ev1" || data["room_id"] != "!room:matrix.intbox.ai" || data["unread"] != "3" {
		t.Fatalf("bad data %v", data)
	}
	if m["android"].(map[string]any)["priority"] != "HIGH" {
		t.Fatalf("bad android %v", m["android"])
	}
	apns := m["apns"].(map[string]any)
	aps := apns["payload"].(map[string]any)["aps"].(map[string]any)
	if aps["mutable-content"] != float64(1) || aps["badge"] != float64(3) || aps["thread-id"] != "!room:matrix.intbox.ai" {
		t.Fatalf("bad aps %v", aps)
	}
	if aps["alert"].(map[string]any)["body"] != "New message" {
		t.Fatalf("bad alert %v", aps["alert"])
	}
	if apns["headers"].(map[string]any)["apns-push-type"] != "alert" {
		t.Fatalf("bad headers %v", apns["headers"])
	}
	if _, ok := m["notification"]; ok {
		t.Fatal("android must get a data-only message")
	}
}

func TestNotifyRejectsDeadAndUnknown(t *testing.T) {
	_, fcm := newStub(t, func(token string, _ int) (int, string) {
		if token == "dead" {
			return 404, `{"error":{"code":404,"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`
		}
		return 200, `{}`
	})
	code, out := post(t, NewGateway(fcm, testConfig).Handler(), "notify_mixed.json")
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}
	got := out["rejected"].([]any)
	if len(got) != 2 || got[0] != "stranger" || got[1] != "dead" {
		t.Fatalf("rejected = %v", got)
	}
}

func TestNotifyRetriesThenSucceeds(t *testing.T) {
	stub, fcm := newStub(t, func(_ string, call int) (int, string) {
		if call < 3 {
			return 503, `{"error":{"code":503,"status":"UNAVAILABLE"}}`
		}
		return 200, `{}`
	})
	code, _ := post(t, NewGateway(fcm, testConfig).Handler(), "notify.json")
	if code != 200 || len(stub.messages) != 3 {
		t.Fatalf("status %d after %d calls", code, len(stub.messages))
	}
}

func TestNotifyFailureIsBadGateway(t *testing.T) {
	_, fcm := newStub(t, func(string, int) (int, string) {
		return 400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"bad payload"}}`
	})
	code, out := post(t, NewGateway(fcm, testConfig).Handler(), "notify.json")
	if code != http.StatusBadGateway {
		t.Fatalf("status %d: %v", code, out)
	}
}

func TestNotifyBadgeOnly(t *testing.T) {
	stub, fcm := newStub(t, nil)
	code, _ := post(t, NewGateway(fcm, testConfig).Handler(), "notify_badge.json")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	m := stub.messages[0]
	aps := m["apns"].(map[string]any)["payload"].(map[string]any)["aps"].(map[string]any)
	if _, ok := aps["alert"]; ok || aps["badge"] != float64(0) {
		t.Fatalf("bad badge-only aps %v", aps)
	}
	if m["android"].(map[string]any)["priority"] != "NORMAL" {
		t.Fatalf("bad android %v", m["android"])
	}
}

func TestNotifyBadJSON(t *testing.T) {
	_, fcm := newStub(t, nil)
	rec := httptest.NewRecorder()
	NewGateway(fcm, testConfig).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/_matrix/push/v1/notify", strings.NewReader("{")))
	if rec.Code != 400 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestLoadConfig(t *testing.T) {
	c, err := loadConfig("testdata/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:8096" || len(c.AppIDs) != 2 || c.Alert != "New message" {
		t.Fatalf("bad config %+v", c)
	}
}
