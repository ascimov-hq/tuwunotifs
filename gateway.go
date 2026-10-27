package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
)

type Sender interface {
	Send(ctx context.Context, m Message) error
}

type Notification struct {
	EventID string `json:"event_id"`
	RoomID  string `json:"room_id"`
	Prio    string `json:"prio"`
	Counts  struct {
		Unread *int `json:"unread"`
	} `json:"counts"`
	Devices []Device `json:"devices"`
}

type Device struct {
	AppID   string `json:"app_id"`
	Pushkey string `json:"pushkey"`
}

type Gateway struct {
	sender Sender
	apps   map[string]bool
	alert  string
}

func NewGateway(sender Sender, cfg Config) *Gateway {
	apps := make(map[string]bool, len(cfg.AppIDs))
	for _, id := range cfg.AppIDs {
		apps[id] = true
	}
	return &Gateway{sender: sender, apps: apps, alert: cfg.Alert}
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /_matrix/push/v1/notify", g.notify)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	return mux
}

func (g *Gateway) notify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Notification Notification `json:"notification"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"errcode": "M_BAD_JSON", "error": err.Error()})
		return
	}
	n := body.Notification
	rejected := []string{}
	failed := 0
	for _, d := range n.Devices {
		log := slog.With("app_id", d.AppID, "pushkey", shortKey(d.Pushkey), "event_id", n.EventID, "room_id", n.RoomID)
		if !g.apps[d.AppID] || d.Pushkey == "" {
			log.Warn("rejecting pushkey for unknown app")
			rejected = append(rejected, d.Pushkey)
			continue
		}
		err := g.sender.Send(r.Context(), g.message(n, d))
		switch {
		case err == nil:
			log.Info("push sent")
		case errors.Is(err, ErrUnregistered):
			log.Info("rejecting dead pushkey", "err", err)
			rejected = append(rejected, d.Pushkey)
		default:
			log.Error("push failed", "err", err)
			failed++
		}
	}
	if failed > 0 && failed == len(n.Devices) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"errcode": "M_UNKNOWN", "error": "fcm delivery failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"rejected": rejected})
}

func (g *Gateway) message(n Notification, d Device) Message {
	data := map[string]string{}
	aps := map[string]any{}
	if n.Counts.Unread != nil {
		data["unread"] = strconv.Itoa(*n.Counts.Unread)
		aps["badge"] = *n.Counts.Unread
	}
	if n.EventID == "" {
		return Message{
			Token:   d.Pushkey,
			Data:    data,
			Android: &AndroidConfig{Priority: "NORMAL"},
			APNS: &APNSConfig{
				Headers: map[string]string{"apns-push-type": "alert", "apns-priority": "5"},
				Payload: map[string]any{"aps": aps},
			},
		}
	}
	data["event_id"] = n.EventID
	data["room_id"] = n.RoomID
	androidPrio, apnsPrio := "HIGH", "10"
	if n.Prio == "low" {
		androidPrio, apnsPrio = "NORMAL", "5"
	}
	aps["alert"] = map[string]string{"body": g.alert}
	aps["mutable-content"] = 1
	aps["sound"] = "default"
	aps["thread-id"] = n.RoomID
	payload := map[string]any{"aps": aps}
	for k, v := range data {
		payload[k] = v
	}
	return Message{
		Token:   d.Pushkey,
		Data:    data,
		Android: &AndroidConfig{Priority: androidPrio},
		APNS: &APNSConfig{
			Headers: map[string]string{"apns-push-type": "alert", "apns-priority": apnsPrio, "apns-collapse-id": collapseID(n.EventID)},
			Payload: payload,
		},
	}
}

func collapseID(eventID string) string {
	if len(eventID) > 64 {
		return eventID[:64]
	}
	return eventID
}

func shortKey(k string) string {
	if len(k) > 12 {
		return k[:12] + "…"
	}
	return k
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
