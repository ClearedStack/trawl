package muster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// The provisional wire format: every call is a POST of a JSON object to one
// of these paths. The contract version travels in a header, and the caller
// proves who it is with its Kubernetes ServiceAccount token as a bearer
// token.
const (
	pathLease        = "/v1/lease"
	pathHeartbeat    = "/v1/heartbeat"
	pathLookup       = "/v1/lookup"
	pathRate         = "/v1/rate"
	pathComplete     = "/v1/complete"
	pathFail         = "/v1/fail"
	pathNeedsSubtool = "/v1/needs_subtool"

	versionHeader = "Contract-Version"

	// maxResponse bounds how much of a hub response is read.
	maxResponse = 512 << 10
)

type leaseRequest struct {
	Worker string `json:"worker"`
	Lane   Lane   `json:"lane"`
}

type leaseResponse struct {
	Item *Item `json:"item"`
}

type heartbeatRequest struct {
	LeaseID string `json:"lease_id"`
}

type lookupRequest struct {
	Hashes []string `json:"hashes"`
}

type rateRequest struct {
	Key    string `json:"key"`
	Member string `json:"member"`
}

type rateResponse struct {
	Count int `json:"count"`
}

// HTTP is a Client that talks to the hub over cluster-private HTTP.
type HTTP struct {
	base      string
	tokenFile string
	client    *http.Client
}

var _ Client = (*HTTP)(nil)

// NewHTTP returns a client for the hub at base, for example
// "http://hub.made-up.svc:8080". tokenFile is the path of the
// ServiceAccount token; it is read again for every call because the token
// is rotated. An empty tokenFile sends no token.
func NewHTTP(base, tokenFile string, timeout time.Duration) (*HTTP, error) {
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, errors.New("the hub address must start with http:// or https://")
	}
	return &HTTP{
		base:      strings.TrimRight(base, "/"),
		tokenFile: tokenFile,
		client: &http.Client{
			Timeout: timeout,
			// The hub never redirects; following one could carry the
			// token somewhere else.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// call posts in and decodes the reply into out. It maps every outcome to
// one of the package errors, so no address or token can reach a caller.
func (h *HTTP) call(ctx context.Context, path string, in, out any) (status int, err error) {
	body, err := json.Marshal(in)
	if err != nil {
		return 0, ErrProtocol
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, ErrProtocol
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(versionHeader, ContractVersion)
	if h.tokenFile != "" {
		token, err := os.ReadFile(h.tokenFile)
		if err != nil {
			return 0, ErrUnavailable
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return 0, ErrUnavailable
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		if len(data) > maxResponse {
			return 0, ErrProtocol
		}
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return 0, ErrProtocol
			}
		}
		return resp.StatusCode, nil
	case resp.StatusCode == http.StatusNoContent:
		return resp.StatusCode, nil
	case resp.StatusCode == http.StatusConflict:
		return 0, ErrConflict
	case resp.StatusCode == http.StatusGone:
		return 0, ErrGone
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusTooManyRequests:
		return 0, ErrUnavailable
	default:
		return 0, ErrProtocol
	}
}

func (h *HTTP) Lease(ctx context.Context, lane Lane) (*Item, error) {
	var resp leaseResponse
	status, err := h.call(ctx, pathLease, leaseRequest{Worker: Worker, Lane: lane}, &resp)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent || resp.Item == nil {
		return nil, nil
	}
	if resp.Item.LeaseID == "" || resp.Item.ItemID == "" {
		return nil, ErrProtocol
	}
	return resp.Item, nil
}

func (h *HTTP) Heartbeat(ctx context.Context, leaseID string) error {
	_, err := h.call(ctx, pathHeartbeat, heartbeatRequest{LeaseID: leaseID}, nil)
	return err
}

func (h *HTTP) Lookup(ctx context.Context, hashes []string) (LookupResult, error) {
	var res LookupResult
	status, err := h.call(ctx, pathLookup, lookupRequest{Hashes: hashes}, &res)
	if err != nil {
		return LookupResult{}, err
	}
	if status != http.StatusOK || res.LastSeq < 0 {
		return LookupResult{}, ErrProtocol
	}
	if res.Known == nil {
		res.Known = map[string]string{}
	}
	return res, nil
}

func (h *HTTP) Rate(ctx context.Context, key, member string) (int, error) {
	var res rateResponse
	status, err := h.call(ctx, pathRate, rateRequest{Key: key, Member: member}, &res)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK || res.Count < 1 {
		return 0, ErrProtocol
	}
	return res.Count, nil
}

func (h *HTTP) Complete(ctx context.Context, c Completion) error {
	_, err := h.call(ctx, pathComplete, c, nil)
	return err
}

func (h *HTTP) Fail(ctx context.Context, f Failure) error {
	_, err := h.call(ctx, pathFail, f, nil)
	return err
}

func (h *HTTP) NeedsSubtool(ctx context.Context, f Failure) error {
	_, err := h.call(ctx, pathNeedsSubtool, f, nil)
	return err
}

// Handler serves the provisional wire format from the stand-in, so the
// HTTP client can be tested against it. When token is not empty, calls
// must carry it as a bearer token.
func (m *Memory) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	handle := func(path string, fn func(ctx context.Context, body []byte) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.Header.Get(versionHeader) != ContractVersion {
				http.Error(w, "", http.StatusBadRequest)
				return
			}
			if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "", http.StatusUnauthorized)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, maxResponse))
			if err != nil {
				http.Error(w, "", http.StatusBadRequest)
				return
			}
			out, err := fn(r.Context(), body)
			switch {
			case errors.Is(err, ErrUnavailable):
				http.Error(w, "", http.StatusServiceUnavailable)
			case errors.Is(err, ErrConflict):
				http.Error(w, "", http.StatusConflict)
			case errors.Is(err, ErrGone):
				http.Error(w, "", http.StatusGone)
			case err != nil:
				http.Error(w, "", http.StatusBadRequest)
			case out == nil:
				w.WriteHeader(http.StatusNoContent)
			default:
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(out)
			}
		})
	}
	decode := func(body []byte, v any) error {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			return ErrProtocol
		}
		return nil
	}

	handle(pathLease, func(ctx context.Context, body []byte) (any, error) {
		var req leaseRequest
		if err := decode(body, &req); err != nil {
			return nil, err
		}
		if req.Worker != Worker {
			return nil, ErrProtocol
		}
		item, err := m.Lease(ctx, req.Lane)
		if err != nil || item == nil {
			return nil, err
		}
		return leaseResponse{Item: item}, nil
	})
	handle(pathHeartbeat, func(ctx context.Context, body []byte) (any, error) {
		var req heartbeatRequest
		if err := decode(body, &req); err != nil {
			return nil, err
		}
		return nil, m.Heartbeat(ctx, req.LeaseID)
	})
	handle(pathLookup, func(ctx context.Context, body []byte) (any, error) {
		var req lookupRequest
		if err := decode(body, &req); err != nil {
			return nil, err
		}
		return m.Lookup(ctx, req.Hashes)
	})
	handle(pathRate, func(ctx context.Context, body []byte) (any, error) {
		var req rateRequest
		if err := decode(body, &req); err != nil {
			return nil, err
		}
		n, err := m.Rate(ctx, req.Key, req.Member)
		if err != nil {
			return nil, err
		}
		return rateResponse{Count: n}, nil
	})
	handle(pathComplete, func(ctx context.Context, body []byte) (any, error) {
		var req Completion
		if err := decode(body, &req); err != nil {
			return nil, err
		}
		return nil, m.Complete(ctx, req)
	})
	handle(pathFail, func(ctx context.Context, body []byte) (any, error) {
		var req Failure
		if err := decode(body, &req); err != nil {
			return nil, err
		}
		return nil, m.Fail(ctx, req)
	})
	handle(pathNeedsSubtool, func(ctx context.Context, body []byte) (any, error) {
		var req Failure
		if err := decode(body, &req); err != nil {
			return nil, err
		}
		return nil, m.NeedsSubtool(ctx, req)
	})
	return mux
}
