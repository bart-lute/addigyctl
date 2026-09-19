// Command swagger2openapi converts the Swagger 2.0 document published by
// Addigy into OpenAPI 3, which is what oapi-codegen consumes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
)

func main() {
	in := flag.String("in", "api/godoc_swagger.json", "path to the Swagger 2.0 document")
	out := flag.String("out", "api/openapi3.json", "path to write the OpenAPI 3 document")
	server := flag.String("server", "https://api.addigy.com/api/v2", "server URL (the Addigy spec has no host)")
	flag.Parse()

	if err := run(*in, *out, *server); err != nil {
		fmt.Fprintln(os.Stderr, "swagger2openapi:", err)
		os.Exit(1)
	}
}

func run(in, out, server string) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}

	var doc2 openapi2.T
	if err := json.Unmarshal(data, &doc2); err != nil {
		return fmt.Errorf("parsing %s: %w", in, err)
	}

	doc3, err := openapi2conv.ToV3(&doc2)
	if err != nil {
		return fmt.Errorf("converting to OpenAPI 3: %w", err)
	}
	doc3.Servers = openapi3.Servers{{URL: server}}

	b, err := json.MarshalIndent(doc3, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", out)
	return nil
}
