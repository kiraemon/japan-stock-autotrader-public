package jquantsbulk

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCaptureDatasetAndCalendarUseOnlyRequestedMetadata(t *testing.T) {
	const month = "2026-06"
	for _, tc := range []struct {
		name                  string
		dataset               Dataset
		calendar              bool
		path, query, response string
	}{
		{"daily list", Daily, false, "/v2/bulk/list", "endpoint=%2Fequities%2Fbars%2Fdaily&from=2026-06&to=2026-06",
			`{"data":[{"Key":"equities/bars/daily/historical/2026/daily_202606.csv.gz","Size":3,"LastModified":"2026-07-01T00:00:00Z"}]}`},
		{"calendar", Minute, true, "/v2/markets/calendar", "from=2026-06-01&to=2026-06-30", `{"data":[]}`},
		{"invalid calendar JSON", Minute, true, "/v2/markets/calendar", "from=2026-06-01&to=2026-06-30", `{"data":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != tc.path || r.URL.RawQuery != tc.query ||
					r.Host != "api.jquants.com" || r.Header.Get("x-api-key") != "synthetic-key" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = fmt.Fprint(w, tc.response)
			}))
			t.Cleanup(server.Close)
			transport := server.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig.ServerName = "127.0.0.1"
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "api.jquants.com:443" {
					return nil, fmt.Errorf("unexpected synthetic destination: %s", address)
				}
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			original := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = original })

			report, err := CaptureDataset(t.Context(), "synthetic-key", month, t.TempDir(), tc.dataset, !tc.calendar, tc.calendar)
			if tc.name == "invalid calendar JSON" {
				if err == nil || report.Complete || report.Calendar != nil || report.Requests != 1 {
					t.Fatalf("invalid calendar was published: %+v, %v", report, err)
				}
				return
			}
			if err != nil || !report.Complete || report.Requests != 1 || report.LastStatus != http.StatusOK ||
				report.CalendarOnly != tc.calendar || report.ListOnly == tc.calendar || report.Captured.Before(report.Started) {
				t.Fatalf("unexpected record: %+v, %v", report, err)
			}
			if tc.calendar {
				if string(report.Calendar) != tc.response || len(report.Files) != 0 {
					t.Fatalf("calendar was mixed with downloads: %+v", report)
				}
			} else if report.Dataset != Daily || len(report.Files) != 1 || report.Files[0].Size != 3 || report.Files[0].Local != "" {
				t.Fatalf("list-only response or dataset changed: %+v", report)
			}
		})
	}
}
