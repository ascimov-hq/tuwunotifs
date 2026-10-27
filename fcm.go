package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

var ErrUnregistered = errors.New("fcm token is no longer valid")

type Message struct {
	Token   string            `json:"token"`
	Data    map[string]string `json:"data,omitempty"`
	Android *AndroidConfig    `json:"android,omitempty"`
	APNS    *APNSConfig       `json:"apns,omitempty"`
}

type AndroidConfig struct {
	Priority string `json:"priority"`
}

type APNSConfig struct {
	Headers map[string]string `json:"headers"`
	Payload map[string]any    `json:"payload"`
}

type FCM struct {
	client   *http.Client
	url      string
	attempts int
	backoff  time.Duration
}

func NewFCM(ctx context.Context, credentialsFile, projectID string) (*FCM, string, error) {
	raw, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, "", err
	}
	creds, err := google.CredentialsFromJSONWithType(ctx, raw, google.ServiceAccount, fcmScope)
	if err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", credentialsFile, err)
	}
	if projectID == "" {
		projectID = creds.ProjectID
	}
	if projectID == "" {
		return nil, "", fmt.Errorf("fcm_project_id is not set and %s has no project_id", credentialsFile)
	}
	client := oauth2.NewClient(ctx, creds.TokenSource)
	client.Timeout = 15 * time.Second
	url := "https://fcm.googleapis.com/v1/projects/" + projectID + "/messages:send"
	return newFCM(client, url), projectID, nil
}

func newFCM(client *http.Client, url string) *FCM {
	return &FCM{client: client, url: url, attempts: 3, backoff: time.Second}
}

type fcmError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			ErrorCode string `json:"errorCode"`
		} `json:"details"`
	} `json:"error"`
}

func (e fcmError) errorCode() string {
	for _, d := range e.Error.Details {
		if d.ErrorCode != "" {
			return d.ErrorCode
		}
	}
	return e.Error.Status
}

func (f *FCM) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(map[string]Message{"message": m})
	if err != nil {
		return err
	}
	var last error
	for attempt := 1; attempt <= f.attempts; attempt++ {
		retry, wait, err := f.send(ctx, body)
		if !retry {
			return err
		}
		last = err
		if attempt == f.attempts {
			break
		}
		if wait == 0 {
			wait = f.backoff * time.Duration(attempt)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return last
}

func (f *FCM) send(ctx context.Context, body []byte) (retry bool, wait time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url, bytes.NewReader(body))
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		var authErr *oauth2.RetrieveError
		return ctx.Err() == nil && !errors.As(err, &authErr), 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		return false, 0, nil
	}
	var fe fcmError
	_ = json.Unmarshal(raw, &fe)
	code := fe.errorCode()
	switch {
	case resp.StatusCode == http.StatusNotFound, code == "UNREGISTERED", code == "SENDER_ID_MISMATCH":
		return false, 0, fmt.Errorf("%w: %s", ErrUnregistered, code)
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return true, retryAfter(resp.Header.Get("Retry-After")), fmt.Errorf("fcm %d %s: %s", resp.StatusCode, code, fe.Error.Message)
	default:
		return false, 0, fmt.Errorf("fcm %d %s: %s", resp.StatusCode, code, fe.Error.Message)
	}
}

func retryAfter(v string) time.Duration {
	s, err := strconv.Atoi(v)
	if err != nil || s <= 0 {
		return 0
	}
	return min(time.Duration(s)*time.Second, 10*time.Second)
}
