package jquantsbulk

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDailyAndMasterUseIsolatedAllowedPathsAndKeys(t *testing.T) {
	for _, dataset := range []Dataset{Daily, Master} {
		t.Run(string(dataset), func(t *testing.T) {
			c, calls := fixtureClient(t, 200)
			c.dataset = dataset
			base := c.http.Transport
			c.http.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				resp, err := base.RoundTrip(req)
				if req.URL.Path == "/v2/bulk/list" {
					if req.URL.Query().Get("endpoint") != dataset.endpoint() {
						t.Error("wrong dataset requested")
					}
					body := `{"data":[{"Key":"` + strings.TrimPrefix(dataset.endpoint(), "/") + `/historical/2026/data_202606.csv.gz","Size":3,"LastModified":"2026-07-01T00:00:00Z"}]}`
					resp.Body = io.NopCloser(strings.NewReader(body))
					resp.ContentLength = int64(len(body))
				}
				return resp, err
			})
			if err := c.capture(context.Background(), t.TempDir(), false); err != nil || *calls != 3 || len(c.report.Files) != 1 || c.report.Files[0].SHA256 == "" {
				t.Fatal("typed capture failed", err)
			}
			for _, other := range []Dataset{Minute, Daily, Master} {
				if other == dataset {
					continue
				}
				if validDatasetKey(strings.TrimPrefix(other.endpoint(), "/")+"/202606.csv.gz", c.month, dataset) {
					t.Fatal("cross-dataset key admitted")
				}
				before := *calls
				if _, err := c.metadata(context.Background(), "/v2/bulk/list", url.Values{"endpoint": {other.endpoint()}, "from": {c.month}, "to": {c.month}}); err == nil || *calls != before {
					t.Fatal("cross-dataset request reached API")
				}
			}
		})
	}
}

func TestDatasetValidationAndCalendarIsolation(t *testing.T) {
	for _, value := range []string{"", "Minute", "../master", "/equities/master", "trades"} {
		if _, err := ValidateDataset(value); err == nil {
			t.Fatal("unknown dataset admitted")
		}
	}
	for _, dataset := range []Dataset{Daily, Master} {
		c, calls := fixtureClient(t, 200)
		c.dataset = dataset
		if _, err := c.metadata(context.Background(), "/v2/markets/calendar", url.Values{"from": {"2026-06-01"}, "to": {"2026-06-30"}}); err == nil || *calls != 0 {
			t.Fatal("calendar mixed with non-minute capture")
		}
		if _, err := CaptureDataset(context.Background(), "", "2026-06", t.TempDir(), dataset, false, true); err == nil {
			t.Fatal("calendar mode admitted")
		}
	}
}
