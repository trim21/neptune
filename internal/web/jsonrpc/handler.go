// Copyright 2024 trim21 <trim21.me@gmail.com>
// Copyright 2021 Viacheslav Poturaev
// SPDX-License-Identifier: MIT
// https://github.com/swaggest/jsonrpc/blob/master/LICENSE

package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"

	"github.com/bytedance/sonic"
	"github.com/go-playground/validator/v10"
	"github.com/rs/zerolog/log"
	"github.com/swaggest/openapi-go"
	"github.com/swaggest/usecase"

	"neptune/internal/pkg/mempool"
)

var rpcPool = mempool.New()
var resPool = mempool.New()

// ErrorCode is an JSON-RPC 2.0 error code.
type ErrorCode int

// Standard error codes.
const (
	CodeParseError     = ErrorCode(-32700)
	CodeInvalidRequest = ErrorCode(-32600)
	CodeMethodNotFound = ErrorCode(-32601)
	CodeInvalidParams  = ErrorCode(-32602)
	CodeInternalError  = ErrorCode(-32603)
)

const ver = "2.0"

// Handler serves JSON-RPC 2.0 methods with HTTP.
type Handler struct {
	OpenAPI   *OpenAPI
	Validator *validator.Validate

	// MaxBodySize limits the request body size in bytes. 0 means no limit.
	MaxBodySize int64

	methods     map[string]method
	Middlewares []usecase.Middleware

	SkipParamsValidation bool
}

type method struct {
	// failingUseCase allows to pass input decoding error through use case middlewares.
	failingUseCase usecase.Interactor

	useCase usecase.Interactor

	inputBufferType reflect.Type

	outputBufferType reflect.Type
	inputIsPtr       bool
}

func (h *method) setupInputBuffer() {
	h.inputBufferType = nil

	var withInput usecase.HasInputPort
	if !usecase.As(h.useCase, &withInput) {
		return
	}

	h.inputBufferType = reflect.TypeOf(withInput.InputPort())
	if h.inputBufferType != nil {
		if h.inputBufferType.Kind() == reflect.Ptr {
			h.inputBufferType = h.inputBufferType.Elem()
			h.inputIsPtr = true
		}
	}
}

func (h *method) setupOutputBuffer() {
	h.outputBufferType = nil

	var withOutput usecase.HasOutputPort
	if !usecase.As(h.useCase, &withOutput) {
		return
	}

	h.outputBufferType = reflect.TypeOf(withOutput.OutputPort())
	if h.outputBufferType != nil {
		if h.outputBufferType.Kind() == reflect.Ptr {
			h.outputBufferType = h.outputBufferType.Elem()
		}
	}
}

type errCtxKey struct{}

// Add registers use case interactor as JSON-RPC method.
func (h *Handler) Add(u usecase.Interactor) {
	if h.methods == nil {
		h.methods = make(map[string]method)
	}

	var withName usecase.HasName

	if !usecase.As(u, &withName) {
		panic("use case name is required")
	}

	var fu usecase.Interactor = usecase.Interact(func(ctx context.Context, input, output any) error {
		return ctx.Value(errCtxKey{}).(error)
	})

	u = usecase.Wrap(u, h.Middlewares...)
	fu = usecase.Wrap(fu, h.Middlewares...)

	m := method{
		useCase:        u,
		failingUseCase: fu,
	}
	m.setupInputBuffer()
	m.setupOutputBuffer()

	_, exists := h.methods[withName.Name()]
	if exists {
		panic(fmt.Sprintf("method %s exists", withName.Name()))
	}

	h.methods[withName.Name()] = m

	if h.OpenAPI != nil {
		err := h.OpenAPI.Collect(withName.Name(), u, func(op openapi.OperationContext) error {
			op.AddSecurity("api-key")
			return nil
		})
		if err != nil {
			panic("failed to add to OpenAPI schema: " + err.Error())
		}
	}
}

// Request is an JSON-RPC request item.
type Request struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  sonic.NoCopyRawMessage `json:"params"`
	ID      sonic.NoCopyRawMessage `json:"id,omitempty"`
}

// Response is an JSON-RPC response item.
type Response struct {
	JSONRPC string                 `json:"jsonrpc"`
	Result  sonic.NoCopyRawMessage `json:"result,omitempty"`
	Error   *Error                 `json:"error,omitempty"`
	ID      sonic.NoCopyRawMessage `json:"id"`
}

// Error describes JSON-RPC error structure.
type Error struct {
	Data    any       `json:"data,omitempty"`
	Message string    `json:"message"`
	Code    ErrorCode `json:"code"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset: utf-8")

	ctx := r.Context()

	var (
		req  Request
		resp Response
	)

	bodyBuf, err := readAllLimited(r.Body, h.MaxBodySize)
	defer rpcPool.Put(bodyBuf)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			h.fail(w, err, CodeParseError)
		} else {
			h.fail(w, fmt.Errorf("failed to read request body: %w", err), CodeParseError)
		}
		return
	}

	if err := sonic.ConfigFastest.Unmarshal(bodyBuf.B, &req); err != nil {
		h.fail(w, fmt.Errorf("failed to unmarshal request: %w", err), CodeParseError)
		return
	}

	resp.ID = req.ID
	resp.JSONRPC = ver

	if req.JSONRPC != ver {
		h.fail(w, fmt.Errorf("invalid jsonrpc value: %q", req.JSONRPC), CodeInvalidRequest)

		return
	}

	h.invoke(ctx, req, &resp)

	var buf = resPool.Get()
	defer resPool.Put(buf)

	enc := sonic.ConfigFastest.NewEncoder(buf)
	if err := enc.Encode(resp); err != nil {
		h.fail(w, err, CodeInternalError)

		return
	}

	if _, err := w.Write(buf.B); err != nil {
		h.fail(w, err, CodeInternalError)
	}
}

func (h *Handler) invoke(ctx context.Context, req Request, resp *Response) {
	var input, output any

	m, found := h.methods[req.Method]
	if !found {
		resp.Error = &Error{
			Code:    CodeMethodNotFound,
			Message: "method not found: " + req.Method,
		}

		return
	}

	if m.inputBufferType != nil {
		iv := reflect.New(m.inputBufferType)
		input = iv.Interface()

		if !h.decode(ctx, m, req, resp, input) {
			return
		}

		if !m.inputIsPtr {
			input = iv.Elem().Interface()
		}
	}

	if m.outputBufferType != nil {
		output = reflect.New(m.outputBufferType).Interface()
	}

	if err := m.useCase.Interact(ctx, input, output); err != nil {
		h.errResp(resp, "operation failed: "+err.Error(), CodeInternalError, err)

		return
	}

	h.encode(resp, output)
}

func (h *Handler) encode(resp *Response, output any) {
	data, err := sonic.ConfigFastest.Marshal(output)
	if err != nil {
		resp.Error = &Error{
			Code:    CodeInternalError,
			Message: "failed to marshal result: " + err.Error(),
		}

		return
	}

	resp.Result = data
}

func (h *Handler) decode(ctx context.Context, m method, req Request, resp *Response, input any) bool {
	if err := sonic.ConfigFastest.Unmarshal(req.Params, input); err != nil {
		if m.failingUseCase != nil {
			err = m.failingUseCase.Interact(context.WithValue(ctx, errCtxKey{}, err), nil, nil)
		}

		h.errResp(resp, "failed to unmarshal parameters", CodeInvalidParams, err)

		return false
	}

	if h.Validator != nil && !h.SkipParamsValidation {
		if err := h.Validator.Struct(input); err != nil {
			if m.failingUseCase != nil {
				err = m.failingUseCase.Interact(context.WithValue(ctx, errCtxKey{}, err), nil, nil)
			}

			h.errResp(resp, "invalid parameters", CodeInvalidParams, err)

			return false
		}
	}

	return true
}

func (h *Handler) errResp(resp *Response, msg string, code ErrorCode, err error) {
	resp.Error = &Error{
		Code:    code,
		Message: msg,
	}

	//goland:noinspection GoTypeAssertionOnErrors
	if e, ok := err.(ErrWithAppCode); ok {
		resp.Error.Code = ErrorCode(e.AppErrCode())
		resp.Error.Message = err.Error()

		log.Warn().Err(err).Str("code", fmt.Sprintf("%d", resp.Error.Code)).Msg(msg)

		return
	}

	resp.Error.Data = err.Error()

	if code == CodeInternalError {
		log.Error().Err(err).Msg(msg)
	} else {
		log.Warn().Err(err).Int("code", int(code)).Msg(msg)
	}
}

func (h *Handler) fail(w http.ResponseWriter, err error, code ErrorCode) {
	log.Error().Err(err).Int("code", int(code)).Msg("JSON-RPC error")

	resp := Response{
		JSONRPC: ver,
		Error: &Error{
			Code:    code,
			Message: err.Error(),
		},
	}

	buf := resPool.Get()
	defer resPool.Put(buf)

	if err := sonic.ConfigFastest.NewEncoder(buf).Encode(resp); err != nil {
		log.Error().Err(err).Msg("failed to marshal JSON-RPC error response")
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	if _, err := w.Write(buf.B); err != nil {
		log.Error().Err(err).Msg("failed to write JSON-RPC error response")
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}
}

var errBodyTooLarge = errors.New("request body too large")

func readAllLimited(r io.Reader, limit int64) (*mempool.Buffer, error) {
	if limit <= 0 {
		buf := rpcPool.Get()
		_, err := buf.ReadFrom(r)
		return buf, err
	}

	buf := mempool.GetWithCapFromPool(&rpcPool, int(limit)+1)
	n, err := io.ReadFull(r, buf.B)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return buf, err
	}
	if int64(n) > limit {
		return buf, errBodyTooLarge
	}
	buf.B = buf.B[:n]
	return buf, nil
}
