package unroll

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/psbt/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/stretchr/testify/require"
)

var errTestBroadcast = fmt.Errorf("broadcast failed")

func TestUnroll(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		for _, tc := range []struct {
			name             string
			vtxoBranches     []int
			expectedPackages int
		}{
			{name: "one package per branch", vtxoBranches: []int{0, 1}, expectedPackages: 2},
			{name: "shared branch", vtxoBranches: []int{0, 0, 1}, expectedPackages: 2},
		} {
			t.Run(tc.name, func(t *testing.T) {
				args, explorer := newTestUnrollArgs(t, tc.vtxoBranches, false)

				res, err := Unroll(t.Context(), args)
				require.NoError(t, err)
				require.Len(t, res, tc.expectedPackages)
				requireBroadcastPackages(t, explorer, res)
			})
		}
	})

	t.Run("invalid", func(t *testing.T) {
		for _, tc := range []struct {
			name             string
			vtxoBranches     []int
			failBroadcastAt  int
			expectedPackages int
		}{
			{
				name:             "first broadcast fails",
				vtxoBranches:     []int{0, 1},
				failBroadcastAt:  1,
				expectedPackages: 0,
			},
			{
				name:             "later broadcast fails",
				vtxoBranches:     []int{0, 1, 2},
				failBroadcastAt:  3,
				expectedPackages: 2,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				args, explorer := newTestUnrollArgs(t, tc.vtxoBranches, false)
				explorer.failBroadcastAt = tc.failBroadcastAt

				res, err := Unroll(t.Context(), args)
				require.ErrorIs(t, err, errTestBroadcast)
				require.Len(t, res, tc.expectedPackages)
				requireBroadcastPackages(t, explorer, res)
			})
		}
	})
}

func TestBumpUnroll(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		for _, tc := range []struct {
			name          string
			vtxoBranches  []int
			inMempool     bool
			expectedBumps int
		}{
			{name: "nothing pending", vtxoBranches: []int{0, 1}, expectedBumps: 0},
			{
				name:          "pending branches",
				vtxoBranches:  []int{0, 1},
				inMempool:     true,
				expectedBumps: 2,
			},
			{
				name:          "shared pending branch",
				vtxoBranches:  []int{0, 0, 1},
				inMempool:     true,
				expectedBumps: 2,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				args, explorer := newTestUnrollArgs(t, tc.vtxoBranches, tc.inMempool)

				res, err := BumpUnroll(t.Context(), args)
				require.NoError(t, err)
				require.Len(t, res, tc.expectedBumps)
				requireBroadcastPackages(t, explorer, res)

				for _, pkg := range res {
					require.Equal(t, explorer.mempool[pkg.ParentTxid], pkg.ParentTx)
				}
			})
		}
	})

	t.Run("invalid", func(t *testing.T) {
		t.Run("later broadcast fails", func(t *testing.T) {
			args, explorer := newTestUnrollArgs(t, []int{0, 1}, true)
			explorer.failBroadcastAt = 2

			res, err := BumpUnroll(t.Context(), args)
			require.ErrorIs(t, err, errTestBroadcast)
			require.Len(t, res, 1)
			requireBroadcastPackages(t, explorer, res)
		})
	})
}

// unrollStubExplorer reports the txs in mempool as unconfirmed and any other as
// offchain. Broadcast records the packages it accepts and fails the
// failBroadcastAt-th call (1-based, 0 never fails).
type unrollStubExplorer struct {
	clientlib.Explorer
	mempool         map[string]string
	failBroadcastAt int
	broadcasts      int
	packages        [][]string
}

func (e *unrollStubExplorer) GetTxBlockTime(txid string) (bool, int64, error) {
	if _, ok := e.mempool[txid]; !ok {
		return false, 0, fmt.Errorf("tx %s not found", txid)
	}
	return false, 0, nil
}

func (e *unrollStubExplorer) GetTxHex(txid string) (string, error) {
	tx, ok := e.mempool[txid]
	if !ok {
		return "", fmt.Errorf("tx %s not found", txid)
	}
	return tx, nil
}

func (e *unrollStubExplorer) Broadcast(txs ...string) (string, error) {
	e.broadcasts++
	if e.broadcasts == e.failBroadcastAt {
		return "", errTestBroadcast
	}
	e.packages = append(e.packages, txs)
	return "", nil
}

// stubIndexer serves the chain of each vtxo and the psbt of each branch tx.
type stubIndexer struct {
	clientlib.Indexer
	chains     map[string][]clientlib.ChainWithExpiry
	virtualTxs map[string]string
}

func (i stubIndexer) GetVtxoChain(
	_ context.Context, outpoint clientlib.Outpoint, _ ...clientlib.PageOption,
) (*clientlib.VtxoChainResponse, error) {
	return &clientlib.VtxoChainResponse{Chain: i.chains[outpoint.Txid]}, nil
}

func (i stubIndexer) GetVirtualTxs(
	_ context.Context, txids []string, _ ...clientlib.PageOption,
) (*clientlib.VirtualTxsResponse, error) {
	txs := make([]string, 0, len(txids))
	for _, txid := range txids {
		txs = append(txs, i.virtualTxs[txid])
	}
	return &clientlib.VirtualTxsResponse{Txs: txs}, nil
}

// newTestUnrollArgs returns args unrolling one vtxo per entry of vtxoBranches,
// each entry being the index of the branch tx the vtxo hangs from. The branch
// txs are offchain, or in the mempool if inMempool is set. It also returns the
// explorer, to inspect the broadcasts.
func newTestUnrollArgs(
	t *testing.T, vtxoBranches []int, inMempool bool,
) (UnrollArgs, *unrollStubExplorer) {
	t.Helper()

	args, _ := newTestBumpAnchorArgs(t, []uint64{100_000}, 1)

	explorer := &unrollStubExplorer{
		Explorer: args.Explorer,
		mempool:  make(map[string]string),
	}
	indexer := stubIndexer{
		chains:     make(map[string][]clientlib.ChainWithExpiry),
		virtualTxs: make(map[string]string),
	}

	parents := make([]*wire.MsgTx, 0)
	for i, branch := range vtxoBranches {
		for len(parents) <= branch {
			parent, ptx := newTestBranchTx(t, len(parents))
			indexer.virtualTxs[parent.TxID()] = ptx
			if inMempool {
				explorer.mempool[parent.TxID()] = serializeTx(t, parent)
			}
			parents = append(parents, parent)
		}

		vtxo := clientlib.Vtxo{
			Outpoint: clientlib.Outpoint{Txid: testHash(t, 100+i).String()},
			Amount:   1000,
		}
		args.Vtxos = append(args.Vtxos, vtxo)
		indexer.chains[vtxo.Txid] = []clientlib.ChainWithExpiry{{
			Txid: parents[branch].TxID(),
			Type: clientlib.IndexerChainedTxTypeTree,
		}}
	}

	args.Explorer = explorer
	args.Indexer = indexer
	// Nothing in the flow verifies signatures, so a placeholder key-spend
	// signature is enough for the child inputs to finalize.
	args.SignTx = func(_ context.Context, tx string) (string, error) {
		ptx, err := psbt.NewFromRawBytes(strings.NewReader(tx), true)
		if err != nil {
			return "", err
		}
		for i := 1; i < len(ptx.Inputs); i++ {
			ptx.Inputs[i].TaprootKeySpendSig = make([]byte, 64)
		}
		return ptx.B64Encode()
	}

	return args, explorer
}

// newTestBranchTx returns a branch tx with an anchor output, both finalized
// and as the base64 psbt the indexer serves. Its input carries a placeholder
// key-spend signature.
func newTestBranchTx(t *testing.T, index int) (*wire.MsgTx, string) {
	t.Helper()

	ptx, err := psbt.New(
		[]*wire.OutPoint{{Hash: testHash(t, index)}},
		[]*wire.TxOut{{Value: 1000, PkScript: make([]byte, 34)}, txutils.AnchorOutput()},
		3, 0, []uint32{wire.MaxTxInSequenceNum},
	)
	require.NoError(t, err)
	ptx.Inputs[0].TaprootKeySpendSig = make([]byte, 64)

	b64, err := ptx.B64Encode()
	require.NoError(t, err)

	tx := ptx.UnsignedTx.Copy()
	tx.TxIn[0].Witness = wire.TxWitness{ptx.Inputs[0].TaprootKeySpendSig}
	return tx, b64
}

// requireBroadcastPackages checks that res holds exactly the broadcast
// packages, each child spending its parent's anchor.
func requireBroadcastPackages(
	t *testing.T, explorer *unrollStubExplorer, res []UnrollRes,
) {
	t.Helper()

	require.Len(t, explorer.packages, len(res))
	for i, pkg := range res {
		require.Equal(t, []string{pkg.ParentTx, pkg.ChildTx}, explorer.packages[i])

		var parent, child wire.MsgTx
		require.NoError(t, parent.Deserialize(hex.NewDecoder(strings.NewReader(pkg.ParentTx))))
		require.NoError(t, child.Deserialize(hex.NewDecoder(strings.NewReader(pkg.ChildTx))))
		require.Equal(t, parent.TxID(), pkg.ParentTxid)
		require.Equal(t, child.TxID(), pkg.ChildTxid)

		anchor, err := txutils.FindAnchorOutpoint(&parent)
		require.NoError(t, err)
		require.Equal(t, *anchor, child.TxIn[0].PreviousOutPoint)
	}
}

func testHash(t *testing.T, n int) chainhash.Hash {
	t.Helper()

	hash, err := chainhash.NewHashFromStr(fmt.Sprintf("%064x", n+1))
	require.NoError(t, err)
	return *hash
}

func serializeTx(t *testing.T, tx *wire.MsgTx) string {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, tx.Serialize(&buf))
	return hex.EncodeToString(buf.Bytes())
}
