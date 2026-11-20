package inbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Scalar keeps the forwarder's original string/number value, including timestamps
// whose timezone or formatting is unknown. Objects and arrays are rejected.
type Scalar json.RawMessage

func (s *Scalar) UnmarshalJSON(data []byte) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	switch value.(type) {
	case nil, string, json.Number:
		*s = append((*s)[:0], data...)
		return nil
	default:
		return errors.New("expected a string, number, or null")
	}
}

func (s Scalar) MarshalJSON() ([]byte, error) {
	if len(s) == 0 {
		return []byte("null"), nil
	}
	return s, nil
}

func (s Scalar) Text() string {
	if len(s) == 0 || string(s) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(s, &value) == nil {
		return value
	}
	return string(s)
}

type Incoming struct {
	From          string `json:"from"`
	Text          string `json:"text"`
	SentStamp     Scalar `json:"sentStamp"`
	ReceivedStamp Scalar `json:"receivedStamp"`
	SIM           Scalar `json:"sim"`
}

func (in Incoming) Validate() error {
	if strings.TrimSpace(in.From) == "" {
		return errors.New("from is required")
	}
	if strings.TrimSpace(in.Text) == "" {
		return errors.New("text is required")
	}
	if len(in.From) > 512 {
		return errors.New("from must be at most 512 bytes")
	}
	if len(in.Text) > 65536 {
		return errors.New("text must be at most 65536 bytes")
	}
	if len(in.SIM.Text()) > 128 {
		return errors.New("sim must be at most 128 bytes")
	}
	if len(in.SentStamp) > 256 || len(in.ReceivedStamp) > 256 {
		return errors.New("timestamps must be at most 256 bytes")
	}
	return nil
}

func (in Incoming) fingerprint() []byte {
	// Without either timestamp, identical messages may be separate deliveries.
	if in.SentStamp.Text() == "" && in.ReceivedStamp.Text() == "" {
		return nil
	}
	data, _ := json.Marshal([]string{in.From, in.Text, in.SentStamp.Text(), in.ReceivedStamp.Text(), in.SIM.Text()})
	sum := sha256.Sum256(data)
	return sum[:]
}

type Message struct {
	// String IDs avoid losing BIGINT precision in JavaScript.
	ID            string          `json:"id"`
	From          string          `json:"from"`
	Text          string          `json:"text"`
	SentStamp     json.RawMessage `json:"sentStamp"`
	ReceivedStamp json.RawMessage `json:"receivedStamp"`
	SIM           string          `json:"sim"`
	CreatedAt     time.Time       `json:"createdAt"`
}

type Filter struct {
	Query  string
	SIM    string
	From   string
	Before int64
	Limit  int
}

type SIM struct {
	ID           string `json:"id"`
	Alias        string `json:"alias"`
	MessageCount int64  `json:"messageCount"`
}

type Conversation struct {
	LatestMessage Message `json:"latestMessage"`
	MessageCount  int64   `json:"messageCount"`
}

type ConversationPage struct {
	Conversations []Conversation `json:"conversations"`
	NextCursor    string         `json:"nextCursor,omitempty"`
	Total         int64          `json:"total"`
	MessageTotal  int64          `json:"messageTotal"`
	Today         int64          `json:"today"`
	SIMs          []string       `json:"sims"`
}

type Page struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"nextCursor,omitempty"`
	Total      int64     `json:"total"`
	Today      int64     `json:"today"`
	SIMs       []string  `json:"sims"`
}

func jsonValue(value Scalar) string {
	if len(value) == 0 {
		return "null"
	}
	return string(value)
}

func parseCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 {
		return 0, errors.New("invalid cursor")
	}
	if strconv.FormatInt(n, 10) != value {
		return 0, errors.New("invalid cursor")
	}
	return n, nil
}
