// Copyright 2024 trim21 <trim21.me@gmail.com>
// Copyright 2021 Viacheslav Poturaev
// SPDX-License-Identifier: MIT
// https://github.com/swaggest/jsonrpc/blob/master/LICENSE

package jsonrpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/samber/lo"
	"github.com/swaggest/openapi-go"
	"github.com/swaggest/openapi-go/openapi3"
	"github.com/swaggest/usecase"
)

// OpenAPI extracts OpenAPI documentation from HTTP handler and underlying use case interactor.
type OpenAPI struct {
	gen *openapi3.Reflector

	BasePath string // URL path to docs, default "/docs/".
	mu       sync.Mutex
}

// Reflector is an accessor to OpenAPI Reflector instance.
func (c *OpenAPI) Reflector() *openapi3.Reflector {
	if c.gen == nil {
		c.gen = &openapi3.Reflector{}
	}

	return c.gen
}

// Collect adds use case handler to documentation.
func (c *OpenAPI) Collect(
	name string,
	u usecase.Interactor,
	annotations ...func(ctx openapi.OperationContext) error,
) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	defer func() {
		if err != nil {
			err = fmt.Errorf("failed to reflect API schema for %s: %w", name, err)
		}
	}()

	reflector := c.Reflector()

	reflector.SpecEns().WithMapOfAnythingItem("x-envelope", "jsonrpc-2.0")

	op := lo.Must(reflector.NewOperationContext(http.MethodPost, name))

	var hasInput usecase.HasInputPort
	if usecase.As(u, &hasInput) {
		op.AddReqStructure(hasInput.InputPort())
	}

	var hasOutput usecase.HasOutputPort
	if usecase.As(u, &hasOutput) {
		op.AddRespStructure(hasOutput.OutputPort())
	}

	c.processUseCase(op, u)

	for _, setup := range annotations {
		err = setup(op)
		if err != nil {
			return err
		}
	}

	lo.Must0(reflector.AddOperation(op))

	return nil
}

func (c *OpenAPI) processUseCase(op openapi.OperationContext, u usecase.Interactor) {
	var (
		hasName        usecase.HasName
		hasTitle       usecase.HasTitle
		hasDescription usecase.HasDescription
		hasTags        usecase.HasTags
		hasDeprecated  usecase.HasIsDeprecated
	)

	if usecase.As(u, &hasName) {
		op.SetID(hasName.Name())
	}

	if usecase.As(u, &hasTitle) {
		op.SetSummary(hasTitle.Title())
	}

	if usecase.As(u, &hasTags) {
		op.SetTags(hasTags.Tags()...)
	}

	if usecase.As(u, &hasDescription) {
		op.SetDescription(hasDescription.Description())
	}

	if usecase.As(u, &hasDeprecated) && hasDeprecated.IsDeprecated() {
		op.SetIsDeprecated(true)
	}
}

func (c *OpenAPI) ServeHTTP(rw http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	document, err := json.MarshalIndent(c.Reflector().Spec, "", " ")
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
	}

	rw.Header().Set("Content-Type", "application/json; charset=utf8")

	_, err = rw.Write(document)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
	}
}
