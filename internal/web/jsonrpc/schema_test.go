package jsonrpc_test

import (
	"context"
	"testing"

	"github.com/swaggest/assertjson"
	"github.com/swaggest/usecase"

	"tyr/internal/web/jsonrpc"
)

func TestOpenAPI_Collect(t *testing.T) {
	apiSchema := jsonrpc.OpenAPI{}
	apiSchema.Reflector().SpecEns().Info.Title = "JSON-RPC Example"
	apiSchema.Reflector().SpecEns().Info.Version = "v1.2.3"

	apiSchema.Reflector().SpecEns().Info.WithDescription("This app showcases a trivial JSON-RPC API.")

	h := &jsonrpc.Handler{}
	h.OpenAPI = &apiSchema

	type inp struct {
		Name string `json:"name"`
	}

	type out struct {
		Len int `json:"len"`
	}

	u := usecase.NewIOI(new(inp), new(out), func(ctx context.Context, input, output any) error {
		output.(*out).Len = len(input.(*inp).Name)

		return nil
	})
	u.SetTitle("Test")
	u.SetDescription("Test Description")
	u.SetName("nameLength")

	h.Add(u)

	assertjson.EqualMarshal(t, []byte(`{
	  "openapi":"3.0.3",
	  "info":{
		"title":"JSON-RPC Example",
		"description":"This app showcases a trivial JSON-RPC API.",
		"version":"v1.2.3"
	  },
	  "paths":{
		"nameLength":{
		  "post":{
			"summary":"Test","description":"Test Description",
      "security": [
        {
          "api-key": [
          ]
        }
      ],
			"operationId":"nameLength",
			"requestBody":{
			  "content":{
				"application/json":{"schema":{"$ref":"#/components/schemas/JsonrpcTestInp"}}
			  }
			},
			"responses":{
			  "200":{
				"description":"OK",
				"content":{
				  "application/json":{"schema":{"$ref":"#/components/schemas/JsonrpcTestOut"}}
				}
			  }
			}
		  }
		}
	  },
	  "components":{
		"schemas":{
		  "JsonrpcTestInp":{"type":"object","properties":{"name":{"type":"string"}}},
		  "JsonrpcTestOut":{"type":"object","properties":{"len":{"type":"integer"}}}
		}
	  },
	  "x-envelope":"jsonrpc-2.0"
	}`), apiSchema.Reflector().SpecEns())
}
