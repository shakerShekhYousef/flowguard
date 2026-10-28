package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// Webhook receiver for FlowGuard's webhook scenarios. FlowGuard plays the
// provider and POSTs synthetic order events here; GET /_test/orders/{id}
// exposes the resulting business state for assert.state.
//
// Every event, valid in both modes:
//
//	{"id": "evt_1", "type": "order.paid", "sequence": 2,
//	 "data": {"order_id": "ord_1", "amount": 100}}
//
// sequence is optional (a positive integer, increasing per order); amount
// is required, and positive, for order.paid only. Unknown fields are
// ignored, as real receivers do. An order is created by the first event
// that names it, whatever its type, so events may arrive in any order.
//
// Buggy mode (default) applies every event as it arrives: a duplicate
// order.paid charges again, and a late or stale event overwrites newer
// state. Safe mode (-safe) applies these rules in order, and answers 200
// with "applied": false when it ignores an event:
//
//  1. duplicate: an event id already processed has no effect;
//  2. stale: a sequence at or below the order's last_sequence is ignored;
//  3. cancelled: a cancelled order is final, so later events are ignored;
//  4. already paid: an order is charged at most once;
//  5. order.created on an existing order leaves its status unchanged.

const (
	typeCreated   = "order.created"
	typePaid      = "order.paid"
	typeCancelled = "order.cancelled"

	statusCreated   = "created"
	statusPaid      = "paid"
	statusCancelled = "cancelled"

	maxEventBytes = 1 << 20
)

// idPattern keeps event and order ids short, printable and usable in a URL
// path segment.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

type event struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Sequence *int64 `json:"sequence"`
	Data     *struct {
		OrderID string `json:"order_id"`
		Amount  *int64 `json:"amount"`
	} `json:"data"`
}

type order struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	Charges         int      `json:"charges"`
	ProcessedEvents []string `json:"processed_events"`
	LastSequence    int64    `json:"last_sequence"`
}

// decodeEvent reads exactly one JSON object and validates it, returning
// the HTTP status for a bad event: 400 for malformed JSON or a wrongly typed
// field, 413 for an oversized body, 422 for a missing or invalid value.
func decodeEvent(r *http.Request, w http.ResponseWriter) (event, int, error) {
	var e event
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEventBytes))
	if err := dec.Decode(&e); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return e, http.StatusRequestEntityTooLarge, fmt.Errorf("event is larger than %d bytes", maxEventBytes)
		}
		return e, http.StatusBadRequest, fmt.Errorf("invalid JSON: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return e, http.StatusBadRequest, errors.New("invalid JSON: unexpected data after the event object")
	}

	switch {
	case !idPattern.MatchString(e.ID):
		return e, http.StatusUnprocessableEntity, errors.New("id is required: 1-128 characters from A-Z a-z 0-9 _ . : -")
	case e.Type == "":
		return e, http.StatusUnprocessableEntity, errors.New("type is required")
	case e.Type != typeCreated && e.Type != typePaid && e.Type != typeCancelled:
		return e, http.StatusUnprocessableEntity, fmt.Errorf("unsupported event type %q: want order.created, order.paid or order.cancelled", e.Type)
	case e.Sequence != nil && *e.Sequence < 1:
		return e, http.StatusUnprocessableEntity, errors.New("sequence must be a positive integer")
	case e.Data == nil:
		return e, http.StatusUnprocessableEntity, errors.New("data is required")
	case !idPattern.MatchString(e.Data.OrderID):
		return e, http.StatusUnprocessableEntity, errors.New("data.order_id is required: 1-128 characters from A-Z a-z 0-9 _ . : -")
	case e.Type == typePaid && (e.Data.Amount == nil || *e.Data.Amount < 1):
		return e, http.StatusUnprocessableEntity, errors.New("data.amount is required for order.paid and must be a positive integer")
	}
	return e, 0, nil
}

func (s *store) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	e, status, err := decodeEvent(r, w)
	if err != nil {
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}

	s.mu.Lock()
	reason := s.applyEvent(e)
	s.mu.Unlock()

	body := map[string]any{"received": true, "applied": reason == ""}
	if reason != "" {
		body["reason"] = reason
	}
	writeJSON(w, http.StatusOK, body)
}

// applyEvent updates order state and returns why the event was ignored, or
// "" if it was applied. The caller holds s.mu.
func (s *store) applyEvent(e event) (ignored string) {
	if s.safe && s.seenEvents[e.ID] {
		return "duplicate"
	}

	o, ok := s.orders[e.Data.OrderID]
	if !ok {
		o = &order{ID: e.Data.OrderID, Status: statusCreated, ProcessedEvents: []string{}}
		s.orders[o.ID] = o
	}

	if s.safe {
		s.seenEvents[e.ID] = true
		switch {
		case e.Sequence != nil && *e.Sequence <= o.LastSequence:
			return "stale"
		case o.Status == statusCancelled:
			return "order cancelled"
		case e.Type == typePaid && o.Status == statusPaid:
			return "already paid"
		}
	}

	switch e.Type {
	case typeCreated:
		// Buggy mode lets a late order.created roll the status back.
		if !s.safe {
			o.Status = statusCreated
		}
	case typePaid:
		o.Status = statusPaid
		o.Charges++
	case typeCancelled:
		o.Status = statusCancelled
	}
	o.ProcessedEvents = append(o.ProcessedEvents, e.ID)
	if e.Sequence != nil && (!s.safe || *e.Sequence > o.LastSequence) {
		o.LastSequence = *e.Sequence
	}
	return ""
}

func (s *store) orderState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	o, ok := s.orders[r.PathValue("id")]
	var snapshot order
	if ok {
		snapshot = *o
		snapshot.ProcessedEvents = append([]string{}, o.ProcessedEvents...)
	}
	s.mu.Unlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "order not found"})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
