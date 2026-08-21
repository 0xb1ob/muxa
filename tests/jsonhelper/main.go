// muxa-test-json is built only by the test suite. It implements the
// json-type/json-keys/json-get/json-values mini-jq helpers that shell
// tests need; they must not ship in the production muxa-broker binary.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(runCLI(os.Args[1:]))
}

func runCLI(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "muxa-test-json: json-type|json-keys|json-get|json-values")
		return 2
	}
	switch args[0] {
	case "json-type":
		return cliJSONType()
	case "json-keys":
		return cliJSONKeys(args[1:])
	case "json-get":
		return cliJSONGet(args[1:])
	case "json-values":
		return cliJSONValues(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "muxa-test-json: unknown command %q\n", args[0])
		return 2
	}
}

func decodeJSONStdin() (any, int) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "muxa-test-json: read stdin: %v\n", err)
		return nil, 1
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		fmt.Fprintf(os.Stderr, "muxa-test-json: json: %v\n", err)
		return nil, 1
	}
	return v, 0
}

func cliJSONType() int {
	v, rc := decodeJSONStdin()
	if rc != 0 {
		return rc
	}
	switch v.(type) {
	case []any:
		fmt.Println("array")
	case map[string]any:
		fmt.Println("object")
	default:
		fmt.Println("other")
	}
	return 0
}

func cliJSONKeys(want []string) int {
	v, rc := decodeJSONStdin()
	if rc != 0 {
		return rc
	}
	need := make(map[string]bool, len(want))
	for _, k := range want {
		need[k] = true
	}
	check := func(m map[string]any) bool {
		if len(m) != len(need) {
			return false
		}
		for k := range m {
			if !need[k] {
				return false
			}
		}
		return true
	}
	switch t := v.(type) {
	case []any:
		if len(t) == 0 {
			return 1
		}
		for _, item := range t {
			m, ok := item.(map[string]any)
			if !ok || !check(m) {
				return 1
			}
		}
		return 0
	case map[string]any:
		if check(t) {
			return 0
		}
		return 1
	default:
		return 1
	}
}

func printField(m map[string]any, field string) int {
	val, ok := m[field]
	if !ok {
		return 1
	}
	if val == nil {
		return 3
	}
	switch x := val.(type) {
	case string:
		fmt.Println(x)
	case json.Number:
		fmt.Println(x.String())
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return 1
		}
		fmt.Println(string(b))
	}
	return 0
}

func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func matchRow(m map[string]any, id string) bool {
	if s, ok := m["name"].(string); ok && s == id {
		return true
	}
	if s, ok := m["to"].(string); ok && s == id {
		return true
	}
	return false
}

func cliJSONGet(args []string) int {
	v, rc := decodeJSONStdin()
	if rc != 0 {
		return rc
	}
	switch t := v.(type) {
	case map[string]any:
		if len(args) == 1 {
			return printField(t, args[0])
		}
		if len(args) == 2 && matchRow(t, args[0]) {
			return printField(t, args[1])
		}
		if len(args) == 2 {
			return printField(t, args[1])
		}
		fmt.Fprintln(os.Stderr, "muxa-test-json json-get: FIELD or NAME FIELD")
		return 2
	case []any:
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "muxa-test-json json-get: NAME FIELD for a JSON array")
			return 2
		}
		for _, item := range t {
			m, ok := asObject(item)
			if !ok {
				continue
			}
			if matchRow(m, args[0]) {
				return printField(m, args[1])
			}
		}
		return 1
	default:
		return 1
	}
}

func cliJSONValues(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "muxa-test-json json-values: FIELD")
		return 2
	}
	v, rc := decodeJSONStdin()
	if rc != 0 {
		return rc
	}
	arr, ok := v.([]any)
	if !ok {
		fmt.Fprintln(os.Stderr, "muxa-test-json json-values: expected a JSON array")
		return 1
	}
	field := args[0]
	for _, item := range arr {
		m, ok := asObject(item)
		if !ok {
			continue
		}
		val, ok := m[field]
		if !ok || val == nil {
			fmt.Println()
			continue
		}
		if s, ok := val.(string); ok {
			fmt.Println(s)
			continue
		}
		b, err := json.Marshal(val)
		if err != nil {
			return 1
		}
		fmt.Println(string(b))
	}
	return 0
}
