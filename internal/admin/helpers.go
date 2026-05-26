package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/amaozhao/lazirouter/internal/errs"
)

// pathInt extracts an integer path parameter using Go 1.22+ Request.PathValue.
func pathInt(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	if raw == "" {
		return 0, errs.Validation("missing path parameter: " + name)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errs.Validation("invalid integer path parameter: " + name)
	}
	return n, nil
}

func rawJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	return v
}

func marshalJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
