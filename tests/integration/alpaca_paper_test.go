//go:build live

// Spec section 66 step 7: send valid orders to Alpaca Paper.
//
// Behind its own build tag because it places a real order at a real venue, using
// credentials nobody should need to run the ordinary suites. Everything else about the
// adapter is covered against a stub in adapters/alpaca; what a stub cannot prove is
// that the request we build is the request Alpaca accepts, which is the only question
// this file exists to answer.
//
//	ALPACA_BASE_URL=https://paper-api.alpaca.markets \
//	ALPACA_KEY_ID=... ALPACA_SECRET_KEY=... \
//	  go test -tags=live -count=1 -v ./tests/integration/ -run TestAlpacaPaper
//
// It skips loudly without credentials rather than passing, because a green run that
// proved nothing is worse than a skipped one.

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"agentic-assurance/adapters/alpaca"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/intent"
)

func paperAdapter(t *testing.T) *alpaca.Adapter {
	t.Helper()

	base := os.Getenv("ALPACA_BASE_URL")
	key := os.Getenv("ALPACA_KEY_ID")
	secret := os.Getenv("ALPACA_SECRET_KEY")
	if base == "" || key == "" || secret == "" {
		t.Skip("set ALPACA_BASE_URL, ALPACA_KEY_ID and ALPACA_SECRET_KEY (paper only)")
	}

	adapter, err := alpaca.New(alpaca.Config{
		BaseURL:   base,
		KeyID:     key,
		SecretKey: secret,
		// The instrument-to-symbol mapping belongs to the platform (spec section 13).
		// This test carries the one instrument it uses rather than loading the file,
		// so a mapping change cannot make it look like a venue failure.
		SymbolFor: func(instrumentID string) (string, bool) {
			if instrumentID == "instr_us_equity_00206R102" {
				return "AAPL", true
			}
			return "", false
		},
	})
	if err != nil {
		// Construction refuses a non-paper endpoint, which is the check that makes
		// this test safe to have in the repository at all.
		t.Fatalf("adapter: %v", err)
	}
	return adapter
}

// A resting limit order, placed and then cancelled.
//
// A limit far below the market rather than a market order: this must not fill, because
// a test that leaves a position behind changes the account every time it runs, and the
// question here is whether the venue accepts what we send, not what the price does.
func TestAlpacaPaperAcceptsAndReconcilesAnOrder(t *testing.T) {
	adapter := paperAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	quantity := 1.0
	limit := 1.00 // far below any real price for this symbol; it rests
	clientOrderID := fmt.Sprintf("live-%d", time.Now().UnixNano())

	order, err := adapter.SubmitOrder(ctx, broker.OrderRequest{
		ClientOrderID: clientOrderID,
		TenantID:      "tenant_live_check",
		InstrumentID:  "instr_us_equity_00206R102",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderLimit,
		Quantity:      &quantity,
		LimitPrice:    &limit,
		TimeInForce:   intent.TIFDay,
	})
	if err != nil {
		t.Fatalf("Alpaca Paper refused the order this platform builds: %v", err)
	}
	if order.BrokerOrderID == "" {
		t.Fatal("the venue accepted the order and returned no id; nothing could be reconciled")
	}
	t.Logf("accepted: broker_order_id=%s state=%s client_order_id=%s",
		order.BrokerOrderID, order.State, clientOrderID)

	// Reconciliation by client order id is what makes an ambiguous outcome
	// recoverable (spec section 19), and it is the half a stub cannot prove: the id
	// has to be one Alpaca actually stored.
	reconciled, err := adapter.Reconcile(ctx, clientOrderID)
	if err != nil {
		t.Fatalf("the order could not be reconciled by client order id: %v", err)
	}
	if reconciled.BrokerOrderID != order.BrokerOrderID {
		t.Errorf("reconciliation returned %s, want %s",
			reconciled.BrokerOrderID, order.BrokerOrderID)
	}
	t.Logf("reconciled: state=%s filled=%v", reconciled.State, reconciled.FilledQuantity)

	// Left behind, an open order accumulates on every run.
	if err := adapter.CancelOrder(ctx, clientOrderID); err != nil && !errors.Is(err, broker.ErrOrderNotFound) {
		t.Errorf("the order could not be cancelled and is still resting at the venue: %v", err)
	}
}

// An order for an instrument with no venue symbol never reaches the network. The
// platform owns instrument identity, and an adapter that guessed a symbol would be
// deciding which security a customer bought.
func TestAlpacaPaperRefusesAnUnmappedInstrument(t *testing.T) {
	adapter := paperAdapter(t)
	quantity := 1.0

	_, err := adapter.SubmitOrder(context.Background(), broker.OrderRequest{
		ClientOrderID: fmt.Sprintf("live-unmapped-%d", time.Now().UnixNano()),
		InstrumentID:  "instr_us_equity_NOT_MAPPED",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderMarket,
		Quantity:      &quantity,
		TimeInForce:   intent.TIFDay,
	})
	if !errors.Is(err, broker.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
