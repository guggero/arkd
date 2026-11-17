package unroll

import "fmt"

// UnrollRes is the result of a single Unroll iteration (one parent + child 1C1P package).
type UnrollRes struct {
	ParentTx   string
	ParentTxid string
	ChildTx    string
	ChildTxid  string
}

var ErrWaitingForConfirmation = fmt.Errorf("waiting for confirmation(s), please retry later")

// ErrInsufficientOnchainFunds is returned when the bump address can't pay the CPFP child of an
// unroll package. Unrolling can't go on without it, so retrying is useless until it's funded.
var ErrInsufficientOnchainFunds = fmt.Errorf("insufficient onchain funds to pay unroll fees")
