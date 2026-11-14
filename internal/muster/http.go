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

	"github.com/ClearedStack/trawl/internal/errs"
)

const (
	pathLease        = "/v1/lease"
	pathHeartbeat    = "/v1/heartbeat"
	pathComplete     = "/v1/complete"
	pathFail         = "/v1/fail"
	pathNeedsSubtool = "/v1/needs_subtool"
	pathItems        = "/v1/items"

	// maxRequest is the largest request body the hub accepts.
	maxRequest = 64 << 10
	// maxResponse bounds how much of a hub answer is read.
	maxResponse = 256 << 10
)

// The request bodies. Each holds exactly the fields of its call.

type leaseRequest struct {
	Contract string `json:"contract"`
}

type heartbeatRequest struct {
	Contract string `json:"contract"`
	Lease    string `json:"lease"`
}

type completeRequest struct {
	Contract string `json:"contract"`
	Lease    string `json:"lease"`
	State    string `json:"state"`
	Result   any    `json:"result,omitempty"`
}

type failRequest struct {
	Contract string    `json:"contract"`
	Lease    string    `json:"lease"`
	Code     errs.Code `json:"code"`
}

type needsSubtoolRequest struct {
	Contract string `json:"contract"`
	Lease    string `json:"lease"`
}

type itemsRequest struct {
	Contract string `json:"contract"`
	Source   string `json:"source"`
	Title    string `json:"title"`
	Address  string `json:"address"`
	Result   any    `json:"result,omitempty"`
}

type itemsResponse struct {
	ID        string `json:"id"`
	Duplicate bool   `json:"duplicate"`
}

// Refusal is a refused call: the HTTP status with the code and cause from
// the hub's answer. The hub's sentence is not kept. Use errors.Is with
// ErrUnavailable, ErrConflict or ErrRefused to learn what to do about it.
type Refusal struct {
	Status int
	Code   errs.Code
	Cause  string
}

func (r *Refusal) Error() string {
	text := "hub refused the call with status " + http.StatusText(r.Status)
	if r.Code.Valid() {
		text += " " + string(r.Code)
	}
	if r.Cause != "" {
		text += " cause=" + r.Cause
	}
	return text
}

// Is sorts a refusal into one of the three errors of this package.
func (r *Refusal) Is(target error) bool {
	switch {
	case r.Status == http.StatusConflict:
		return target == ErrConflict
	case r.Status == http.StatusUnauthorized, r.Status == http.StatusForbidden,
		r.Status == http.StatusTooManyRequests, r.Status >= 500:
		// A token the hub does not accept yet, a busy hub and a failing
		// hub all call for the same thing: keep the work and try later.
		return target == ErrUnavailable
	default:
		return target == ErrRefused
	}
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

// Base returns the hub address this client was given.
func (h *HTTP) Base() string { return h.base }

// call posts in and decodes a 200 or 201 answer into out. Every failure
// becomes ErrUnavailable or a *Refusal, so no address or token can reach a
// caller.
func (h *HTTP) call(ctx context.Context, path string, in, out any) (int, error) {
	body, err := json.Marshal(in)
	if err != nil || len(body) > maxRequest {
		return 0, &Refusal{Status: http.StatusRequestEntityTooLarge}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
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
	if err != nil || len(data) > maxResponse {
		return 0, ErrUnavailable
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return 0, ErrUnavailable
			}
		}
		return resp.StatusCode, nil
	case http.StatusNoContent:
		return resp.StatusCode, nil
	}

	refusal := &Refusal{Status: resp.StatusCode}
	var answer struct {
		Error string `json:"error"`
		Cause string `json:"cause"`
	}
	if json.Unmarshal(data, &answer) == nil {
		if code := errs.Code(answer.Error); code.Valid() {
			refusal.Code = code
		}
		refusal.Cause = safeToken(answer.Cause)
	}
	return 0, refusal
}

// safeToken keeps a cause only if it is a short lowercase identifier, so
// that nothing the hub sends can carry data into a log line.
func safeToken(s string) string {
	if s == "" || len(s) > 40 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' && c != '-' {
			return ""
		}
	}
	return s
}

func (h *HTTP) Lease(ctx context.Context) (*Lease, error) {
	var lease Lease
	status, err := h.call(ctx, pathLease, leaseRequest{Contract: ContractVersion}, &lease)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK || lease.Lease == "" || lease.ID == "" {
		return nil, ErrUnavailable
	}
	return &lease, nil
}

func (h *HTTP) Heartbeat(ctx context.Context, lease string) error {
	_, err := h.call(ctx, pathHeartbeat, heartbeatRequest{Contract: ContractVersion, Lease: lease}, nil)
	return err
}

func (h *HTTP) Complete(ctx context.Context, lease, state string, result any) error {
	_, err := h.call(ctx, pathComplete, completeRequest{Contract: ContractVersion, Lease: lease, State: state, Result: result}, nil)
	return err
}

func (h *HTTP) Fail(ctx context.Context, lease string, code errs.Code) error {
	_, err := h.call(ctx, pathFail, failRequest{Contract: ContractVersion, Lease: lease, Code: code}, nil)
	return err
}

func (h *HTTP) NeedsSubtool(ctx context.Context, lease string) error {
	_, err := h.call(ctx, pathNeedsSubtool, needsSubtoolRequest{Contract: ContractVersion, Lease: lease}, nil)
	return err
}

func (h *HTTP) File(ctx context.Context, f Filing) (Filed, error) {
	var answer itemsResponse
	status, err := h.call(ctx, pathItems, itemsRequest{
		Contract: ContractVersion,
		Source:   f.Source,
		Title:    f.Title,
		Address:  f.Address,
		Result:   f.Result,
	}, &answer)
	if err != nil {
		return Filed{}, err
	}
	switch {
	case status == http.StatusCreated:
		return Filed{New: true, ID: answer.ID}, nil
	case status == http.StatusOK && answer.Duplicate:
		return Filed{New: false, ID: answer.ID}, nil
	default:
		// 200 without "duplicate": true, or 204: not an answer the
		// contract defines, so nothing is concluded from it.
		return Filed{}, ErrUnavailable
	}
}
