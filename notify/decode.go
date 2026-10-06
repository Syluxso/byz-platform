package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Syluxso/byz-platform/downstream"
)

// downstreamDecode turns a raw response into out, or a *downstream.Error for
// non-2xx, mirroring downstream.JSON for requests that needed extra headers.
func downstreamDecode(resp *http.Response, method, url string, out any) error {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &downstream.Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw)), Method: method, URL: url}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}
