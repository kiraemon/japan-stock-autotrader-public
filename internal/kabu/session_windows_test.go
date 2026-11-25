package kabu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/instanceguard"
)

func TestSessionOwnsBothEnvironmentsUntilClosed(t *testing.T) {
	name := fmt.Sprintf(`Global\JapanStockAutotrader.Test.Session.%d.%d`, os.Getpid(), time.Now().UnixNano())
	first, err := open(Test, name)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		fmt.Fprint(w, `{"ResultCode":0,"Token":"test-only-token"}`)
	}))
	defer server.Close()
	first.base = server.URL + "/kabusapi"
	if second, err := open(Production, name); !errors.Is(err, instanceguard.ErrHeld) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second environment bypassed gate: %v", err)
	}
	first.Close()
	second, err := open(Production, name)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.Authenticate(context.Background(), "test-only-password"); err == nil {
		t.Fatal("closed client authenticated after losing ownership")
	}
	if requests != 0 {
		t.Fatal("closed client reached the mock network")
	}
}
