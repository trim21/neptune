package jsonrpc_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/swaggest/usecase"

	"tyr/internal/web/jsonrpc"
)

func TestHandler_Add(t *testing.T) {
	cnt := 0

	h := jsonrpc.Handler{}
	h.OpenAPI = &jsonrpc.OpenAPI{}
	h.Validator = validator.New()
	h.Middlewares = append(h.Middlewares, usecase.MiddlewareFunc(func(next usecase.Interactor) usecase.Interactor {
		return usecase.Interact(func(ctx context.Context, input, output any) error {
			cnt++

			return next.Interact(ctx, input, output)
		})
	}))

	type inp struct {
		A string `json:"a" validate:"required"`
		B int    `json:"b"`
	}

	type outp struct {
		A string `json:"a"`
		B int    `json:"b"`
	}

	u := usecase.NewIOI(new(inp), new(outp), func(ctx context.Context, input, output any) error {
		in, ok := input.(*inp)
		assert.True(t, ok)

		out, ok := output.(*outp)
		assert.True(t, ok)

		out.A = in.A
		out.B = in.B

		return nil
	})
	u.SetName("echo")

	h.Add(u)

	req, err := http.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"echo","params":{"a":"abc","b":5},"id":1}`)))
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.JSONEq(t, `{"jsonrpc":"2.0","result":{"b":5,"a":"abc"},"id":1}`, w.Body.String())
	require.Equal(t, 1, cnt)

	req, err = http.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"echo","params":{"a":"abc","b":"abc"},"id":1}`)))
	require.NoError(t, err)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.JSONEq(t, `{"jsonrpc":"2.0","error":{"code":-32602,"message":"failed to unmarshal parameters","data":"json: cannot unmarshal string into Go struct field inp.b of type int"},"id":1}`, w.Body.String())
	require.Equal(t, 2, cnt)

	req, err = http.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"echo","params":{"a":"a","b":9},"id":1}`)))
	require.NoError(t, err)

	req, err = http.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"echo","params":{"b":5},"id":1}`)))
	require.NoError(t, err)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.JSONEq(t, `{"jsonrpc":"2.0","error":{"code":-32602,"message":"invalid parameters","data":"Key: 'inp.A' Error:Field validation for 'A' failed on the 'required' tag"},"id":1}`, w.Body.String())
	require.Equal(t, 3, cnt)
}
