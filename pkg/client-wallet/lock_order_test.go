package wallet

import (
	"context"
	"errors"
	"sync"
	"testing"

	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	"github.com/stretchr/testify/require"
)

// errStopRead ends a spend right after it starts reading wallet state, so
// the test observes only the lock order and needs no server.
var errStopRead = errors.New("stop after the first read")

// lockProbe records, at the first read of wallet state, whether the
// wallet's txLock was held by the spend doing the read.
type lockProbe struct {
	lock *sync.RWMutex

	mu       sync.Mutex
	observed []bool
}

// record notes whether the lock is held and stops the spend.
func (p *lockProbe) record() error {
	held := !p.lock.TryLock()
	if !held {
		p.lock.Unlock()
	}

	p.mu.Lock()
	p.observed = append(p.observed, held)
	p.mu.Unlock()

	return errStopRead
}

// probeIdentity is an unlocked identity whose key listing, the first step
// of every VTXO read, reports to the probe.
type probeIdentity struct {
	clientlib.Identity
	probe *lockProbe
}

func (i probeIdentity) IsLocked() bool { return false }

func (i probeIdentity) ListKeys(context.Context) ([]clientlib.KeyRef, error) {
	return nil, i.probe.record()
}

// probeIndexer reports the control-asset lookup ReissueAsset makes before
// it reads VTXOs.
type probeIndexer struct {
	clientlib.Indexer
	probe *lockProbe
}

func (i probeIndexer) GetAsset(context.Context, string) (*clientlib.AssetInfo, error) {
	return nil, i.probe.record()
}

// TestSpendsReadUnderTxLock pins that every offchain spend reads the VTXOs
// it spends while holding txLock. A spend that reads first and locks later
// can wait out a batch (Settle) that spends those very VTXOs, then submit
// them and be rejected with VTXO_ALREADY_SPENT.
func TestSpendsReadUnderTxLock(t *testing.T) {
	spends := []struct {
		name  string
		spend func(*wallet) error
	}{{
		name: "SendOffChain",
		spend: func(w *wallet) error {
			_, err := w.SendOffChain(context.Background(), nil)
			return err
		},
	}, {
		name: "IssueAsset",
		spend: func(w *wallet) error {
			_, err := w.IssueAsset(context.Background(), 1, nil, nil)
			return err
		},
	}, {
		name: "ReissueAsset",
		spend: func(w *wallet) error {
			_, err := w.ReissueAsset(context.Background(), "asset", 1)
			return err
		},
	}, {
		name: "BurnAsset",
		spend: func(w *wallet) error {
			_, err := w.BurnAsset(context.Background(), "asset", 1)
			return err
		},
	}}
	for _, tc := range spends {
		t.Run(tc.name, func(t *testing.T) {
			lock := &sync.RWMutex{}
			probe := &lockProbe{lock: lock}
			w := &wallet{
				identity: probeIdentity{probe: probe},
				indexer:  probeIndexer{probe: probe},
				txLock:   lock,
			}

			require.ErrorIs(t, tc.spend(w), errStopRead)
			require.Equal(t, []bool{true}, probe.observed)

			// The spend released the lock on its way out.
			require.True(t, lock.TryLock())
			lock.Unlock()
		})
	}
}
